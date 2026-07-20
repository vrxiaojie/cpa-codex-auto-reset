package state

import "time"

const (
	SchemaVersion = 1
	MaxLogs       = 500
)

type State struct {
	SchemaVersion      int                      `json:"schema_version"`
	Accounts           map[string]*AccountState `json:"accounts"`
	ManagementCooldown *Cooldown                `json:"management_auth_cooldown,omitempty"`
	LastScan           ScanSummary              `json:"last_scan"`
	Logs               []LogEntry               `json:"logs"`
}

type AccountState struct {
	Participating      bool                 `json:"participating"`
	ParticipationSetAt time.Time            `json:"participation_set_at"`
	LastSeenAt         time.Time            `json:"last_seen_at"`
	Display            AccountDisplay       `json:"display"`
	PendingAttempt     *Attempt             `json:"pending_attempt,omitempty"`
	Tombstones         map[string]time.Time `json:"credit_tombstones,omitempty"`
	PostResetCooldown  *Cooldown            `json:"post_reset_cooldown,omitempty"`
	FailureBackoff     *Backoff             `json:"failure_backoff,omitempty"`
	PendingLocalClear  *PendingLocalClear   `json:"pending_local_clear,omitempty"`
	LastFingerprint    string               `json:"last_fingerprint,omitempty"`
	LastResult         string               `json:"last_result,omitempty"`
	AvailableCredits   int                  `json:"available_credits"`
	EarliestExpiresAt  time.Time            `json:"earliest_expires_at,omitempty"`
	UsedPercent        float64              `json:"used_percent"`
	Blocked            bool                 `json:"blocked"`
	LastScannedAt      time.Time            `json:"last_scanned_at,omitempty"`
	LastErrorCode      string               `json:"last_error_code,omitempty"`
}

type AccountDisplay struct {
	Label    string `json:"label,omitempty"`
	Email    string `json:"email,omitempty"`
	FileName string `json:"file_name,omitempty"`
	AuthID   string `json:"auth_id,omitempty"`
}

type Attempt struct {
	AttemptIDRef      string    `json:"attempt_id_ref"`
	CreditID          string    `json:"credit_id"`
	CreditRef         string    `json:"credit_ref"`
	IdempotencyKey    string    `json:"idempotency_key"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	Phase             string    `json:"phase"`
	Fingerprint       string    `json:"fingerprint"`
	VerificationCount int       `json:"verification_count"`
	PreAvailableCount int       `json:"pre_available_count"`
	PreUsedPercent    float64   `json:"pre_used_percent"`
	PreBlocked        bool      `json:"pre_blocked"`
}

type Cooldown struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

type Backoff struct {
	Until       time.Time `json:"until"`
	Fingerprint string    `json:"fingerprint"`
	Level       int       `json:"level"`
	Reason      string    `json:"reason"`
}

type PendingLocalClear struct {
	AuthIndex   string    `json:"auth_index"`
	CreatedAt   time.Time `json:"created_at"`
	NextRetryAt time.Time `json:"next_retry_at"`
	Attempts    int       `json:"attempts"`
}

type ScanSummary struct {
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Trigger    string    `json:"trigger,omitempty"`
	Accounts   int       `json:"accounts"`
	Eligible   int       `json:"eligible"`
	Reset      int       `json:"reset"`
	Errors     int       `json:"errors"`
	Error      string    `json:"error,omitempty"`
}

type LogEntry struct {
	Time          time.Time `json:"time"`
	Event         string    `json:"event"`
	Trigger       string    `json:"trigger,omitempty"`
	AccountRef    string    `json:"account_ref,omitempty"`
	Participating bool      `json:"participating"`
	CreditRef     string    `json:"credit_ref,omitempty"`
	Decision      string    `json:"decision,omitempty"`
	Outcome       string    `json:"outcome,omitempty"`
	AttemptIDRef  string    `json:"attempt_id_ref,omitempty"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitempty"`
	DurationMS    int64     `json:"duration_ms,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
}

func New() State {
	return State{SchemaVersion: SchemaVersion, Accounts: make(map[string]*AccountState), Logs: make([]LogEntry, 0)}
}

func (s *State) Normalize() {
	if s.Accounts == nil {
		s.Accounts = make(map[string]*AccountState)
	}
	if s.Logs == nil {
		s.Logs = make([]LogEntry, 0)
	}
	for _, account := range s.Accounts {
		if account != nil && account.Tombstones == nil {
			account.Tombstones = make(map[string]time.Time)
		}
	}
}

func (s *State) AppendLog(entry LogEntry) {
	s.Logs = append(s.Logs, entry)
	if len(s.Logs) > MaxLogs {
		s.Logs = append([]LogEntry(nil), s.Logs[len(s.Logs)-MaxLogs:]...)
	}
}
