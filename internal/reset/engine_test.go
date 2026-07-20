package reset

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/account"
	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/codex"
	pluginconfig "github.com/vrxiaojie/cpa-codex-auto-reset/internal/config"
	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/management"
	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/state"
)

type fakeDiscovery struct{ accounts []account.Account }

func (f fakeDiscovery) Discover() ([]account.Account, error) { return f.accounts, nil }

type scriptedCodex struct {
	mu          sync.Mutex
	usage       func(int) (codex.Usage, error)
	credits     func(int) (codex.CreditList, error)
	consume     func(int, string, string) (codex.ConsumeResult, error)
	usageCalls  int
	creditCalls int
	consumeKeys []string
}

func (f *scriptedCodex) Usage(context.Context, codex.Credentials) (codex.Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usageCalls++
	return f.usage(f.usageCalls)
}

func (f *scriptedCodex) Credits(context.Context, codex.Credentials, time.Time) (codex.CreditList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creditCalls++
	return f.credits(f.creditCalls)
}

func (f *scriptedCodex) Consume(_ context.Context, _ codex.Credentials, creditID, key string) (codex.ConsumeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consumeKeys = append(f.consumeKeys, key)
	return f.consume(len(f.consumeKeys), creditID, key)
}

type fakeClearer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeClearer) ResetQuota(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func TestNonParticipatingAccountNeverConsumes(t *testing.T) {
	engine, _, client, _ := testEngine(t, false)
	client.credits = func(int) (codex.CreditList, error) { t.Fatal("Credits() called"); return codex.CreditList{}, nil }
	if _, errScan := engine.Scan(context.Background(), "manual"); errScan != nil {
		t.Fatalf("Scan() error = %v", errScan)
	}
	if len(client.consumeKeys) != 0 {
		t.Fatalf("consume keys = %#v", client.consumeKeys)
	}
}

func TestSuccessfulResetPersistsCooldownAndClearsLocalQuota(t *testing.T) {
	engine, store, client, clearer := testEngine(t, true)
	client.credits = func(call int) (codex.CreditList, error) {
		if call <= 2 {
			return eligibleCredits(engine.now()), nil
		}
		return codex.CreditList{AvailableCount: 0}, nil
	}
	client.usage = func(call int) (codex.Usage, error) {
		if call == 1 {
			return blockedUsage(engine.now(), 100), nil
		}
		return blockedUsage(engine.now(), 0), nil
	}
	client.consume = func(int, string, string) (codex.ConsumeResult, error) {
		return codex.ConsumeResult{Code: codex.ConsumeReset, WindowsReset: 2}, nil
	}
	summary, errScan := engine.Scan(context.Background(), "manual")
	if errScan != nil {
		t.Fatalf("Scan() error = %v", errScan)
	}
	if summary.Reset != 1 || len(client.consumeKeys) != 1 || clearer.calls != 1 {
		t.Fatalf("summary=%#v consume=%d clear=%d", summary, len(client.consumeKeys), clearer.calls)
	}
	loaded, _ := store.Load()
	item := loaded.Accounts[testAccount().Ref]
	if item.PendingAttempt != nil || item.PendingLocalClear != nil || item.PostResetCooldown == nil || !item.PostResetCooldown.Until.After(engine.now()) {
		t.Fatalf("account state = %#v", item)
	}
}

func TestAmbiguousConsumeReusesOriginalIdempotencyKey(t *testing.T) {
	engine, store, client, _ := testEngine(t, true)
	currentTime := engine.now()
	engine.now = func() time.Time { return currentTime }
	client.credits = func(call int) (codex.CreditList, error) {
		if call <= 3 {
			return eligibleCredits(currentTime), nil
		}
		return codex.CreditList{AvailableCount: 0}, nil
	}
	client.usage = func(call int) (codex.Usage, error) {
		if call <= 2 {
			return blockedUsage(currentTime, 100), nil
		}
		return blockedUsage(currentTime, 0), nil
	}
	client.consume = func(call int, _, _ string) (codex.ConsumeResult, error) {
		if call == 1 {
			return codex.ConsumeResult{}, &codex.HTTPError{StatusCode: http.StatusServiceUnavailable, Ambiguous: true}
		}
		return codex.ConsumeResult{Code: codex.ConsumeAlreadyRedeemed}, nil
	}
	firstSummary, errScan := engine.Scan(context.Background(), "manual")
	if errScan != nil || firstSummary.Errors != 1 {
		t.Fatalf("first Scan() summary=%#v error=%v", firstSummary, errScan)
	}
	loaded, _ := store.Load()
	pending := loaded.Accounts[testAccount().Ref].PendingAttempt
	if pending == nil || pending.Phase != "ambiguous" {
		t.Fatalf("pending = %#v", pending)
	}
	currentTime = currentTime.Add(10 * time.Minute)
	if _, errScan := engine.Scan(context.Background(), "manual"); errScan != nil {
		t.Fatalf("second Scan() error = %v", errScan)
	}
	if len(client.consumeKeys) != 2 || client.consumeKeys[0] != client.consumeKeys[1] {
		t.Fatalf("idempotency keys = %#v", client.consumeKeys)
	}
}

func TestNothingToResetSuppressesSameFingerprintAfterBackoff(t *testing.T) {
	engine, store, client, _ := testEngine(t, true)
	currentTime := engine.now()
	usageSnapshot := blockedUsage(currentTime, 100)
	engine.now = func() time.Time { return currentTime }
	client.credits = func(int) (codex.CreditList, error) { return eligibleCredits(currentTime), nil }
	client.usage = func(int) (codex.Usage, error) { return usageSnapshot, nil }
	client.consume = func(int, string, string) (codex.ConsumeResult, error) {
		return codex.ConsumeResult{Code: codex.ConsumeNothingToReset}, nil
	}
	if _, errScan := engine.Scan(context.Background(), "manual"); errScan != nil {
		t.Fatalf("Scan() error = %v", errScan)
	}
	loaded, _ := store.Load()
	item := loaded.Accounts[testAccount().Ref]
	if item.PendingAttempt != nil || item.FailureBackoff == nil || item.FailureBackoff.Until.Sub(currentTime) < MinimumFailureBackoff {
		t.Fatalf("account state = %#v", item)
	}
	currentTime = currentTime.Add(2 * time.Hour)
	if _, errScan := engine.Scan(context.Background(), "manual"); errScan != nil {
		t.Fatalf("second Scan() error = %v", errScan)
	}
	if len(client.consumeKeys) != 1 {
		t.Fatalf("consume calls = %d", len(client.consumeKeys))
	}
}

func TestTransientFailureBackoffSuppressesImmediateRescanRequests(t *testing.T) {
	engine, _, client, _ := testEngine(t, true)
	client.credits = func(call int) (codex.CreditList, error) {
		if call > 1 {
			t.Fatalf("Credits() call = %d during active backoff", call)
		}
		return codex.CreditList{}, errors.New("temporary failure")
	}
	first, errScan := engine.Scan(context.Background(), "manual")
	if errScan != nil || first.Errors != 1 {
		t.Fatalf("first Scan() summary=%#v error=%v", first, errScan)
	}
	second, errScan := engine.Scan(context.Background(), "manual")
	if errScan != nil || second.Errors != 0 {
		t.Fatalf("second Scan() summary=%#v error=%v", second, errScan)
	}
	if client.creditCalls != 1 {
		t.Fatalf("credit calls = %d", client.creditCalls)
	}
}

func TestStopBeforeStartupDelayPreventsRegistrationTimeScan(t *testing.T) {
	engine, _, client, _ := testEngine(t, true)
	engine.Start(context.Background())
	started := time.Now()
	engine.Stop()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Stop() took %s", elapsed)
	}
	if client.creditCalls != 0 {
		t.Fatalf("startup scan ran before registration could return: %d calls", client.creditCalls)
	}
}

func TestConcurrentScansDoNotDuplicateConsume(t *testing.T) {
	engine, _, client, _ := testEngine(t, true)
	entered := make(chan struct{})
	release := make(chan struct{})
	client.credits = func(int) (codex.CreditList, error) { return eligibleCredits(engine.now()), nil }
	client.usage = func(int) (codex.Usage, error) { return blockedUsage(engine.now(), 100), nil }
	client.consume = func(int, string, string) (codex.ConsumeResult, error) {
		close(entered)
		<-release
		return codex.ConsumeResult{Code: codex.ConsumeNothingToReset}, nil
	}
	firstDone := make(chan error, 1)
	go func() {
		_, errScan := engine.Scan(context.Background(), "manual")
		firstDone <- errScan
	}()
	<-entered
	if _, errScan := engine.Scan(context.Background(), "manual"); errScan == nil || errScan.Error() != "scan already in progress" {
		t.Fatalf("concurrent Scan() error = %v", errScan)
	}
	close(release)
	if errScan := <-firstDone; errScan != nil {
		t.Fatalf("first Scan() error = %v", errScan)
	}
	if len(client.consumeKeys) != 1 {
		t.Fatalf("consume calls = %d", len(client.consumeKeys))
	}
}

func TestManagementAuthenticationFailureCreatesGlobalCooldown(t *testing.T) {
	engine, store, client, clearer := testEngine(t, true)
	clearer.err = &management.HTTPError{StatusCode: http.StatusUnauthorized}
	client.credits = func(call int) (codex.CreditList, error) {
		if call <= 2 {
			return eligibleCredits(engine.now()), nil
		}
		return codex.CreditList{AvailableCount: 0}, nil
	}
	client.usage = func(call int) (codex.Usage, error) {
		if call == 1 {
			return blockedUsage(engine.now(), 100), nil
		}
		return blockedUsage(engine.now(), 0), nil
	}
	client.consume = func(int, string, string) (codex.ConsumeResult, error) {
		return codex.ConsumeResult{Code: codex.ConsumeReset}, nil
	}
	if _, errScan := engine.Scan(context.Background(), "manual"); errScan != nil {
		t.Fatalf("Scan() error = %v", errScan)
	}
	loaded, _ := store.Load()
	if loaded.ManagementCooldown == nil || loaded.ManagementCooldown.Until.Sub(engine.now()) < ManagementAuthCooldown {
		t.Fatalf("management cooldown = %#v", loaded.ManagementCooldown)
	}
	if loaded.Accounts[testAccount().Ref].PendingLocalClear == nil {
		t.Fatal("pending local clear was not preserved")
	}
}

func testEngine(t *testing.T, participating bool) (*Engine, *state.Store, *scriptedCodex, *fakeClearer) {
	t.Helper()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	store := state.NewStore(filepath.Join(t.TempDir(), "state"))
	item := testAccount()
	if errReconcile := state.ReconcileAccounts(store, []account.Account{item}, false, now); errReconcile != nil {
		t.Fatalf("ReconcileAccounts() error = %v", errReconcile)
	}
	if participating {
		if _, errSet := state.SetParticipation(store, []string{item.Ref}, true, now); errSet != nil {
			t.Fatalf("SetParticipation() error = %v", errSet)
		}
	}
	config := pluginconfig.Defaults()
	config.Enabled = true
	config.ManagementKey = "management-secret"
	client := &scriptedCodex{
		usage:   func(int) (codex.Usage, error) { return codex.Usage{}, errors.New("unexpected Usage call") },
		credits: func(int) (codex.CreditList, error) { return codex.CreditList{}, errors.New("unexpected Credits call") },
		consume: func(int, string, string) (codex.ConsumeResult, error) {
			return codex.ConsumeResult{}, errors.New("unexpected Consume call")
		},
	}
	clearer := &fakeClearer{}
	engine := New(config, fakeDiscovery{accounts: []account.Account{item}}, client, clearer, store)
	engine.now = func() time.Time { return now }
	return engine, store, client, clearer
}

func testAccount() account.Account {
	return account.Account{Ref: "account-ref", AuthID: "auth-id", AuthIndex: "auth-index", AccountID: "account-id", AccessToken: "access-token", Label: "Account"}
}

func eligibleCredits(now time.Time) codex.CreditList {
	return codex.CreditList{AvailableCount: 1, Available: []codex.Credit{{ID: "credit-id", Ref: "credit-ref", ExpiresAt: now.Add(time.Hour)}}}
}

func blockedUsage(now time.Time, used float64) codex.Usage {
	return codex.Usage{Allowed: used < 100, Blocked: used >= 100, UsedPercent: used, Primary: &codex.Window{UsedPercent: used, ResetAt: now.Add(5 * time.Hour)}, Secondary: &codex.Window{UsedPercent: used, ResetAt: now.Add(7 * 24 * time.Hour)}}
}
