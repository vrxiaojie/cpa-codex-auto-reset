package reset

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/account"
	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/codex"
	pluginconfig "github.com/vrxiaojie/cpa-codex-auto-reset/internal/config"
	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/management"
	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/state"
)

const (
	CandidateWindow          = 6 * time.Hour
	ProtectionWindow         = 30 * time.Minute
	MinimumFailureBackoff    = 30 * time.Minute
	ManagementAuthCooldown   = 10 * time.Minute
	MaximumVerificationTries = 2
)

type Discovery interface {
	Discover() ([]account.Account, error)
}

type CodexClient interface {
	Usage(context.Context, codex.Credentials) (codex.Usage, error)
	Credits(context.Context, codex.Credentials, time.Time) (codex.CreditList, error)
	Consume(context.Context, codex.Credentials, string, string) (codex.ConsumeResult, error)
}

type LocalClearer interface {
	ResetQuota(context.Context, string) error
}

type Engine struct {
	config    pluginconfig.Config
	discovery Discovery
	codex     CodexClient
	clearer   LocalClearer
	store     *state.Store
	now       func() time.Time

	active  atomic.Bool
	started atomic.Bool
	scanMu  sync.Mutex
	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
}

func New(config pluginconfig.Config, discovery Discovery, codexClient CodexClient, clearer LocalClearer, store *state.Store) *Engine {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	engine := &Engine{
		config:    config,
		discovery: discovery,
		codex:     codexClient,
		clearer:   clearer,
		store:     store,
		now:       time.Now,
		locks:     make(map[string]*sync.Mutex),
		ctx:       lifecycleCtx,
		cancel:    lifecycleCancel,
		done:      make(chan struct{}),
	}
	engine.active.Store(true)
	return engine
}

func (e *Engine) Start(parent context.Context) {
	if e == nil || !e.config.Enabled || !e.config.Safe().Complete || !e.started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer close(e.done)
		stopParent := context.AfterFunc(parent, e.cancel)
		defer stopParent()
		ticker := time.NewTicker(time.Duration(e.config.ScanIntervalSeconds) * time.Second)
		defer ticker.Stop()
		_, _ = e.Scan(e.ctx, "startup")
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-ticker.C:
				_, _ = e.Scan(e.ctx, "scheduled")
			}
		}
	}()
}

func (e *Engine) Stop() {
	if e == nil || !e.active.Swap(false) {
		return
	}
	e.cancel()
	e.scanMu.Lock()
	e.scanMu.Unlock()
	if e.started.Load() {
		select {
		case <-e.done:
		case <-time.After(15 * time.Second):
		}
	}
}

func (e *Engine) Scan(ctx context.Context, trigger string) (state.ScanSummary, error) {
	if e == nil || !e.active.Load() {
		return state.ScanSummary{}, errors.New("reset engine is inactive")
	}
	if !e.scanMu.TryLock() {
		return state.ScanSummary{}, errors.New("scan already in progress")
	}
	defer e.scanMu.Unlock()
	if !e.active.Load() {
		return state.ScanSummary{}, errors.New("reset engine is inactive")
	}
	scanCtx, cancelScan := context.WithCancel(ctx)
	stopLifecycle := context.AfterFunc(e.ctx, cancelScan)
	defer func() {
		stopLifecycle()
		cancelScan()
	}()
	ctx = scanCtx
	started := e.now().UTC()
	summary := state.ScanSummary{StartedAt: started, Trigger: sanitizeTrigger(trigger)}
	if !e.config.Enabled || !e.config.Safe().Complete {
		summary.FinishedAt = e.now().UTC()
		summary.Error = "plugin configuration is incomplete"
		return summary, errors.New(summary.Error)
	}
	accounts, errDiscover := e.discovery.Discover()
	if errDiscover != nil {
		summary.FinishedAt = e.now().UTC()
		summary.Errors = 1
		summary.Error = "account discovery failed"
		_ = e.finishScan(summary)
		return summary, errors.New(summary.Error)
	}
	summary.Accounts = len(accounts)
	if errReconcile := state.ReconcileAccounts(e.store, accounts, e.config.DefaultParticipation, started); errReconcile != nil {
		summary.FinishedAt = e.now().UTC()
		summary.Errors = 1
		summary.Error = "persistent state is unavailable"
		return summary, errors.New(summary.Error)
	}
	for _, item := range accounts {
		if ctx.Err() != nil || !e.active.Load() {
			break
		}
		eligible, resetDone, errProcess := e.processAccount(ctx, summary.Trigger, item)
		if eligible {
			summary.Eligible++
		}
		if resetDone {
			summary.Reset++
		}
		if errProcess != nil {
			summary.Errors++
		}
	}
	summary.FinishedAt = e.now().UTC()
	if errFinish := e.finishScan(summary); errFinish != nil {
		return summary, errFinish
	}
	return summary, nil
}

func (e *Engine) processAccount(ctx context.Context, trigger string, item account.Account) (bool, bool, error) {
	lock := e.accountLock(item.Ref)
	lock.Lock()
	defer lock.Unlock()
	now := e.now().UTC()
	current, errLoad := e.store.Load()
	if errLoad != nil {
		return false, false, errLoad
	}
	accountState := current.Accounts[item.Ref]
	if accountState == nil {
		return false, false, errors.New("account state is missing")
	}
	if !accountState.Participating {
		_ = e.log(state.LogEntry{Time: now, Event: "account_skipped_not_participating", Trigger: trigger, AccountRef: item.Ref, Participating: false, Decision: "skip"})
		return false, false, nil
	}
	if item.Disabled {
		_ = e.log(state.LogEntry{Time: now, Event: "reset_deferred", Trigger: trigger, AccountRef: item.Ref, Participating: true, Decision: "disabled"})
		return false, false, nil
	}
	if current.ManagementCooldown != nil && current.ManagementCooldown.Until.After(now) {
		return false, false, nil
	}
	if accountState.PendingLocalClear != nil {
		e.retryLocalClear(ctx, trigger, item, accountState.PendingLocalClear)
		return false, false, nil
	}
	if accountState.PostResetCooldown != nil && accountState.PostResetCooldown.Until.After(now) {
		_ = e.log(state.LogEntry{Time: now, Event: "reset_suppressed_cooldown", Trigger: trigger, AccountRef: item.Ref, Participating: true, NextAttemptAt: accountState.PostResetCooldown.Until})
		return false, false, nil
	}
	if accountState.FailureBackoff != nil && accountState.FailureBackoff.Until.After(now) {
		return accountState.PendingAttempt != nil, false, nil
	}
	credentials := codex.Credentials{AccessToken: item.AccessToken, AccountID: item.AccountID}
	if accountState.PendingAttempt != nil {
		return true, e.resolvePending(ctx, trigger, item, credentials, accountState.PendingAttempt), nil
	}
	credits, errCredits := e.codex.Credits(ctx, credentials, now)
	if errCredits != nil {
		e.recordTransientFailure(item.Ref, trigger, "credit_list_failed", "", errCredits)
		return false, false, errCredits
	}
	e.updateCreditSnapshot(item.Ref, credits, now)
	credit, okCredit := firstUsableCredit(credits.Available, accountState.Tombstones)
	if !okCredit {
		return false, false, nil
	}
	if credit.ExpiresAt.Sub(now) > CandidateWindow {
		_ = e.log(state.LogEntry{Time: now, Event: "credit_discovered", Trigger: trigger, AccountRef: item.Ref, Participating: true, CreditRef: credit.Ref, Decision: "outside_candidate_window", NextAttemptAt: credit.ExpiresAt.Add(-CandidateWindow)})
		return false, false, nil
	}
	usage, errUsage := e.codex.Usage(ctx, credentials)
	if errUsage != nil {
		e.recordTransientFailure(item.Ref, trigger, "usage_failed", "", errUsage)
		return false, false, errUsage
	}
	e.updateUsageSnapshot(item.Ref, usage, now)
	protection := credit.ExpiresAt.Sub(now) <= ProtectionWindow
	if !usage.Blocked && usage.UsedPercent < float64(e.config.ResetThreshold) && !(protection && usage.UsedPercent > 0) {
		_ = e.log(state.LogEntry{Time: now, Event: "reset_deferred", Trigger: trigger, AccountRef: item.Ref, Participating: true, CreditRef: credit.Ref, Decision: "below_threshold"})
		return false, false, nil
	}
	recheckedCredits, errRecheck := e.codex.Credits(ctx, credentials, now)
	if errRecheck != nil {
		e.recordTransientFailure(item.Ref, trigger, "credit_recheck_failed", "", errRecheck)
		return true, false, errRecheck
	}
	recheckedCredit, okRechecked := firstUsableCredit(recheckedCredits.Available, accountState.Tombstones)
	if !okRechecked || recheckedCredit.ID != credit.ID {
		return true, false, nil
	}
	fingerprint := fingerprint(item.Ref, credit.Ref, usage, recheckedCredits.AvailableCount)
	if accountState.FailureBackoff != nil {
		if accountState.FailureBackoff.Until.After(now) {
			return true, false, nil
		}
		if accountState.FailureBackoff.Fingerprint == fingerprint {
			return true, false, nil
		}
	}
	if !e.active.Load() || ctx.Err() != nil {
		return true, false, nil
	}
	attempt, errAttempt := newAttempt(credit, fingerprint, usage, recheckedCredits.AvailableCount, now)
	if errAttempt != nil {
		return true, false, errAttempt
	}
	if errPersist := e.persistAttempt(item.Ref, attempt, now); errPersist != nil {
		return true, false, errPersist
	}
	result, errConsume := e.codex.Consume(ctx, credentials, credit.ID, attempt.IdempotencyKey)
	if errConsume != nil {
		e.markAmbiguous(item.Ref, trigger, attempt, errConsume)
		return true, false, errConsume
	}
	return true, e.handleConsumeResult(ctx, trigger, item, credentials, attempt, result), nil
}

func (e *Engine) resolvePending(ctx context.Context, trigger string, item account.Account, credentials codex.Credentials, attempt *state.Attempt) bool {
	now := e.now().UTC()
	credits, errCredits := e.codex.Credits(ctx, credentials, now)
	if errCredits != nil {
		e.bumpPendingVerification(item.Ref, attempt, "credit_verify_failed")
		return false
	}
	usage, errUsage := e.codex.Usage(ctx, credentials)
	if errUsage != nil {
		e.bumpPendingVerification(item.Ref, attempt, "usage_verify_failed")
		return false
	}
	creditStillAvailable := false
	for _, credit := range credits.Available {
		if credit.ID == attempt.CreditID {
			creditStillAvailable = true
			break
		}
	}
	if !creditStillAvailable {
		return e.completeSuccessfulReset(ctx, trigger, item, credentials, attempt, "verified_credit_consumed")
	}
	if attempt.VerificationCount >= MaximumVerificationTries {
		e.applyFailure(item.Ref, trigger, attempt.Fingerprint, "ambiguous_verification_exhausted", nil, false, attempt.CreditID)
		return false
	}
	if !e.active.Load() || ctx.Err() != nil {
		return false
	}
	result, errConsume := e.codex.Consume(ctx, credentials, attempt.CreditID, attempt.IdempotencyKey)
	if errConsume != nil {
		e.markAmbiguous(item.Ref, trigger, attempt, errConsume)
		return false
	}
	_ = usage
	return e.handleConsumeResult(ctx, trigger, item, credentials, attempt, result)
}

func (e *Engine) handleConsumeResult(ctx context.Context, trigger string, item account.Account, credentials codex.Credentials, attempt *state.Attempt, result codex.ConsumeResult) bool {
	switch result.Code {
	case codex.ConsumeReset:
		return e.completeSuccessfulReset(ctx, trigger, item, credentials, attempt, "reset")
	case codex.ConsumeAlreadyRedeemed:
		return e.completeSuccessfulReset(ctx, trigger, item, credentials, attempt, "already_redeemed")
	case codex.ConsumeNothingToReset:
		e.applyFailure(item.Ref, trigger, attempt.Fingerprint, "nothing_to_reset", nil, false, attempt.CreditID)
	case codex.ConsumeNoCredit:
		e.applyFailure(item.Ref, trigger, attempt.Fingerprint, "no_credit", nil, true, attempt.CreditID)
	}
	_ = credentials
	return false
}

func (e *Engine) completeSuccessfulReset(ctx context.Context, trigger string, item account.Account, credentials codex.Credentials, attempt *state.Attempt, outcome string) bool {
	now := e.now().UTC()
	verified := e.verifyReset(ctx, credentials, attempt, now)
	if verified {
		outcome += "_verified"
	} else {
		outcome += "_unverified"
	}
	cooldownUntil := now.Add(time.Duration(e.config.PostResetCooldownSeconds) * time.Second)
	errPersist := e.store.Update(func(current *state.State) error {
		accountState := current.Accounts[item.Ref]
		if accountState == nil || accountState.PendingAttempt == nil || accountState.PendingAttempt.IdempotencyKey != attempt.IdempotencyKey {
			return errors.New("pending attempt changed")
		}
		accountState.PendingAttempt = nil
		accountState.PostResetCooldown = &state.Cooldown{Until: cooldownUntil, Reason: outcome}
		accountState.FailureBackoff = nil
		accountState.LastFingerprint = attempt.Fingerprint
		accountState.LastResult = outcome
		accountState.LastErrorCode = ""
		accountState.PendingLocalClear = &state.PendingLocalClear{AuthIndex: item.AuthIndex, CreatedAt: now, NextRetryAt: now}
		current.AppendLog(state.LogEntry{Time: now, Event: eventForSuccess(outcome), Trigger: trigger, AccountRef: item.Ref, Participating: true, CreditRef: attempt.CreditRef, Outcome: outcome, AttemptIDRef: attempt.AttemptIDRef, NextAttemptAt: cooldownUntil})
		return nil
	})
	if errPersist != nil {
		return false
	}
	e.retryLocalClear(ctx, trigger, item, &state.PendingLocalClear{AuthIndex: item.AuthIndex, CreatedAt: now, NextRetryAt: now})
	return true
}

func (e *Engine) verifyReset(ctx context.Context, credentials codex.Credentials, attempt *state.Attempt, now time.Time) bool {
	credits, errCredits := e.codex.Credits(ctx, credentials, now)
	usage, errUsage := e.codex.Usage(ctx, credentials)
	if errCredits != nil || errUsage != nil {
		return false
	}
	creditPresent := false
	for _, credit := range credits.Available {
		if credit.ID == attempt.CreditID {
			creditPresent = true
			break
		}
	}
	creditChanged := !creditPresent || credits.AvailableCount < attempt.PreAvailableCount
	usageChanged := usage.UsedPercent < attempt.PreUsedPercent || (attempt.PreBlocked && !usage.Blocked)
	return creditChanged && usageChanged
}

func (e *Engine) retryLocalClear(ctx context.Context, trigger string, item account.Account, pending *state.PendingLocalClear) bool {
	now := e.now().UTC()
	if pending.NextRetryAt.After(now) {
		return false
	}
	errClear := e.clearer.ResetQuota(ctx, pending.AuthIndex)
	if errClear == nil {
		_ = e.store.Update(func(current *state.State) error {
			if accountState := current.Accounts[item.Ref]; accountState != nil {
				accountState.PendingLocalClear = nil
				current.AppendLog(state.LogEntry{Time: now, Event: "reset_succeeded", Trigger: trigger, AccountRef: item.Ref, Participating: true, Decision: "local_cooldown_cleared", Outcome: accountState.LastResult})
			}
			return nil
		})
		return true
	}
	next := now.Add(backoffDuration(pending.Attempts+1, time.Minute, 30*time.Minute, 0))
	_ = e.store.Update(func(current *state.State) error {
		accountState := current.Accounts[item.Ref]
		if accountState == nil {
			return nil
		}
		accountState.PendingLocalClear = &state.PendingLocalClear{AuthIndex: pending.AuthIndex, CreatedAt: pending.CreatedAt, NextRetryAt: next, Attempts: pending.Attempts + 1}
		var httpErr *management.HTTPError
		if errors.As(errClear, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
			current.ManagementCooldown = &state.Cooldown{Until: now.Add(ManagementAuthCooldown), Reason: "management_auth_failed"}
		}
		current.AppendLog(state.LogEntry{Time: now, Event: "local_cooldown_clear_pending", Trigger: trigger, AccountRef: item.Ref, Participating: true, Decision: "retry", NextAttemptAt: next, ErrorCode: "local_clear_failed"})
		return nil
	})
	return false
}

func (e *Engine) persistAttempt(accountRef string, attempt *state.Attempt, now time.Time) error {
	return e.store.Update(func(current *state.State) error {
		if !e.active.Load() {
			return errors.New("reset engine is inactive")
		}
		accountState := current.Accounts[accountRef]
		if accountState == nil || !accountState.Participating {
			return errors.New("account no longer participates")
		}
		if accountState.PendingAttempt != nil {
			return errors.New("account already has a pending attempt")
		}
		if accountState.PostResetCooldown != nil && accountState.PostResetCooldown.Until.After(now) {
			return errors.New("account entered cooldown")
		}
		accountState.PendingAttempt = attempt
		current.AppendLog(state.LogEntry{Time: now, Event: "reset_attempt_started", AccountRef: accountRef, Participating: true, CreditRef: attempt.CreditRef, Decision: "consume", AttemptIDRef: attempt.AttemptIDRef})
		return nil
	})
}

func (e *Engine) markAmbiguous(accountRef, trigger string, attempt *state.Attempt, cause error) {
	now := e.now().UTC()
	delay := transientBackoff(cause, attempt.VerificationCount+1, time.Duration(e.config.FailureBackoffSeconds)*time.Second)
	_ = e.store.Update(func(current *state.State) error {
		accountState := current.Accounts[accountRef]
		if accountState == nil || accountState.PendingAttempt == nil || accountState.PendingAttempt.IdempotencyKey != attempt.IdempotencyKey {
			return nil
		}
		accountState.PendingAttempt.Phase = "ambiguous"
		accountState.PendingAttempt.VerificationCount++
		accountState.FailureBackoff = &state.Backoff{Until: now.Add(delay), Fingerprint: attempt.Fingerprint, Level: accountState.PendingAttempt.VerificationCount, Reason: "ambiguous"}
		current.AppendLog(state.LogEntry{Time: now, Event: "reset_ambiguous", Trigger: trigger, AccountRef: accountRef, Participating: true, CreditRef: attempt.CreditRef, Outcome: "ambiguous", AttemptIDRef: attempt.AttemptIDRef, NextAttemptAt: now.Add(delay), ErrorCode: "consume_ambiguous"})
		return nil
	})
}

func (e *Engine) bumpPendingVerification(accountRef string, attempt *state.Attempt, reason string) {
	now := e.now().UTC()
	delay := transientBackoff(nil, attempt.VerificationCount+1, time.Duration(e.config.FailureBackoffSeconds)*time.Second)
	_ = e.store.Update(func(current *state.State) error {
		accountState := current.Accounts[accountRef]
		if accountState != nil && accountState.PendingAttempt != nil && accountState.PendingAttempt.IdempotencyKey == attempt.IdempotencyKey {
			accountState.PendingAttempt.VerificationCount++
			accountState.PendingAttempt.Phase = "ambiguous"
			accountState.FailureBackoff = &state.Backoff{Until: now.Add(delay), Fingerprint: attempt.Fingerprint, Level: accountState.PendingAttempt.VerificationCount, Reason: reason}
			accountState.LastResult = reason
		}
		return nil
	})
}

func (e *Engine) applyFailure(accountRef, trigger, fingerprint, outcome string, cause error, tombstone bool, creditID string) {
	now := e.now().UTC()
	base := time.Duration(e.config.FailureBackoffSeconds) * time.Second
	if base < MinimumFailureBackoff {
		base = MinimumFailureBackoff
	}
	delay := transientBackoff(cause, 1, base)
	_ = e.store.Update(func(current *state.State) error {
		accountState := current.Accounts[accountRef]
		if accountState == nil {
			return nil
		}
		creditRef, attemptRef := "", ""
		if accountState.PendingAttempt != nil {
			creditRef = accountState.PendingAttempt.CreditRef
			attemptRef = accountState.PendingAttempt.AttemptIDRef
		}
		accountState.PendingAttempt = nil
		accountState.FailureBackoff = &state.Backoff{Until: now.Add(delay), Fingerprint: fingerprint, Level: 1, Reason: outcome}
		accountState.LastFingerprint = fingerprint
		accountState.LastResult = outcome
		accountState.LastErrorCode = outcome
		if tombstone && creditID != "" {
			if accountState.Tombstones == nil {
				accountState.Tombstones = make(map[string]time.Time)
			}
			accountState.Tombstones[hashCreditID(creditID)] = now
		}
		current.AppendLog(state.LogEntry{Time: now, Event: "reset_deferred", Trigger: trigger, AccountRef: accountRef, Participating: true, CreditRef: creditRef, Decision: "backoff", Outcome: outcome, AttemptIDRef: attemptRef, NextAttemptAt: now.Add(delay), ErrorCode: outcome})
		return nil
	})
}

func (e *Engine) recordTransientFailure(accountRef, trigger, code, fingerprint string, cause error) {
	now := e.now().UTC()
	delay := transientBackoff(cause, 1, time.Duration(e.config.FailureBackoffSeconds)*time.Second)
	_ = e.store.Update(func(current *state.State) error {
		if accountState := current.Accounts[accountRef]; accountState != nil {
			accountState.FailureBackoff = &state.Backoff{Until: now.Add(delay), Fingerprint: fingerprint, Level: 1, Reason: code}
			accountState.LastErrorCode = code
			current.AppendLog(state.LogEntry{Time: now, Event: "reset_deferred", Trigger: trigger, AccountRef: accountRef, Participating: true, Decision: "transient_backoff", NextAttemptAt: now.Add(delay), ErrorCode: code})
		}
		return nil
	})
}

func (e *Engine) finishScan(summary state.ScanSummary) error {
	return e.store.Update(func(current *state.State) error {
		current.LastScan = summary
		current.AppendLog(state.LogEntry{Time: summary.FinishedAt, Event: "scan_completed", Trigger: summary.Trigger, DurationMS: summary.FinishedAt.Sub(summary.StartedAt).Milliseconds(), ErrorCode: summary.Error})
		return nil
	})
}

func (e *Engine) updateCreditSnapshot(accountRef string, credits codex.CreditList, now time.Time) {
	_ = e.store.Update(func(current *state.State) error {
		if accountState := current.Accounts[accountRef]; accountState != nil {
			accountState.AvailableCredits = len(credits.Available)
			accountState.EarliestExpiresAt = time.Time{}
			if len(credits.Available) > 0 {
				accountState.EarliestExpiresAt = credits.Available[0].ExpiresAt
			}
			accountState.LastScannedAt = now
			accountState.LastErrorCode = ""
		}
		return nil
	})
}

func (e *Engine) updateUsageSnapshot(accountRef string, usage codex.Usage, now time.Time) {
	_ = e.store.Update(func(current *state.State) error {
		if accountState := current.Accounts[accountRef]; accountState != nil {
			accountState.UsedPercent = usage.UsedPercent
			accountState.Blocked = usage.Blocked
			accountState.LastScannedAt = now
			accountState.LastErrorCode = ""
		}
		return nil
	})
}

func (e *Engine) log(entry state.LogEntry) error {
	return e.store.Update(func(current *state.State) error {
		current.AppendLog(entry)
		return nil
	})
}

func (e *Engine) accountLock(ref string) *sync.Mutex {
	e.locksMu.Lock()
	defer e.locksMu.Unlock()
	lock := e.locks[ref]
	if lock == nil {
		lock = &sync.Mutex{}
		e.locks[ref] = lock
	}
	return lock
}

func newAttempt(credit codex.Credit, fingerprint string, usage codex.Usage, availableCount int, now time.Time) (*state.Attempt, error) {
	idempotencyKey, errUUID := uuidV4()
	if errUUID != nil {
		return nil, errUUID
	}
	return &state.Attempt{
		AttemptIDRef:      hashCreditID(idempotencyKey),
		CreditID:          credit.ID,
		CreditRef:         credit.Ref,
		IdempotencyKey:    idempotencyKey,
		CreatedAt:         now,
		ExpiresAt:         credit.ExpiresAt,
		Phase:             "persisted",
		Fingerprint:       fingerprint,
		PreAvailableCount: availableCount,
		PreUsedPercent:    usage.UsedPercent,
		PreBlocked:        usage.Blocked,
	}, nil
}

func uuidV4() (string, error) {
	var raw [16]byte
	if _, errRead := rand.Read(raw[:]); errRead != nil {
		return "", errors.New("generate idempotency key")
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func firstUsableCredit(credits []codex.Credit, tombstones map[string]time.Time) (codex.Credit, bool) {
	for _, credit := range credits {
		if _, blocked := tombstones[hashCreditID(credit.ID)]; !blocked {
			return credit, true
		}
	}
	return codex.Credit{}, false
}

func fingerprint(accountRef, creditRef string, usage codex.Usage, availableCount int) string {
	primary, secondary := int64(0), int64(0)
	if usage.Primary != nil {
		primary = usage.Primary.ResetAt.Unix()
	}
	if usage.Secondary != nil {
		secondary = usage.Secondary.ResetAt.Unix()
	}
	return strings.Join([]string{accountRef, creditRef, strconv.FormatInt(primary, 10) + ":" + strconv.FormatInt(secondary, 10), strconv.Itoa(availableCount)}, "+")
}

func transientBackoff(err error, level int, base time.Duration) time.Duration {
	if base < time.Minute {
		base = time.Minute
	}
	delay := backoffDuration(level, base, 6*time.Hour, jitterFor(base))
	var httpErr *codex.HTTPError
	if errors.As(err, &httpErr) && httpErr.RetryAfter > delay {
		delay = httpErr.RetryAfter
	}
	return delay
}

func jitterFor(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	var raw [2]byte
	if _, errRead := rand.Read(raw[:]); errRead != nil {
		return 0
	}
	fraction := float64(uint16(raw[0])<<8|uint16(raw[1])) / float64(math.MaxUint16)
	return time.Duration(float64(base) * 0.2 * fraction)
}

func backoffDuration(level int, base, maximum time.Duration, jitter time.Duration) time.Duration {
	if level < 1 {
		level = 1
	}
	multiplier := math.Pow(2, float64(level-1))
	delay := time.Duration(float64(base) * multiplier)
	if delay > maximum {
		delay = maximum
	}
	return delay + jitter
}

func eventForSuccess(outcome string) string {
	if strings.HasPrefix(outcome, "already_redeemed") {
		return "reset_already_redeemed"
	}
	return "reset_succeeded"
}

func sanitizeTrigger(trigger string) string {
	switch strings.TrimSpace(trigger) {
	case "startup", "scheduled", "manual", "usage":
		return strings.TrimSpace(trigger)
	default:
		return "manual"
	}
}

func hashCreditID(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("ref\x00"+value)))[:24]
}
