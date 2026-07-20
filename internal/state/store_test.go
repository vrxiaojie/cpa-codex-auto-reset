package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/account"
)

func TestStoreAtomicRoundTripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	store := NewStore(dir)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	errUpdate := store.Update(func(current *State) error {
		current.Accounts["account-ref"] = &AccountState{Participating: true, LastSeenAt: now, Tombstones: map[string]time.Time{}}
		return nil
	})
	if errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}
	loaded, errLoad := store.Load()
	if errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	if !loaded.Accounts["account-ref"].Participating {
		t.Fatalf("state = %#v", loaded)
	}
	info, errStat := os.Stat(filepath.Join(dir, "state.json"))
	if errStat != nil {
		t.Fatalf("stat state: %v", errStat)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o", info.Mode().Perm())
	}
}

func TestCorruptStateBlocksUpdate(t *testing.T) {
	dir := t.TempDir()
	if errWrite := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"schema_version":1,"unknown":true}`), 0o600); errWrite != nil {
		t.Fatalf("write corrupt state: %v", errWrite)
	}
	store := NewStore(dir)
	errUpdate := store.Update(func(current *State) error {
		current.Accounts["unsafe"] = &AccountState{}
		return nil
	})
	if !errors.Is(errUpdate, ErrCorrupt) {
		t.Fatalf("Update() error = %v", errUpdate)
	}
	raw, errRead := os.ReadFile(filepath.Join(dir, "state.json"))
	if errRead != nil {
		t.Fatalf("read state: %v", errRead)
	}
	if string(raw) != `{"schema_version":1,"unknown":true}` {
		t.Fatalf("corrupt state was overwritten: %s", raw)
	}
}

func TestParticipationIsPersistedAtomically(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	accounts := []account.Account{{Ref: "one", AuthID: "auth-one", Label: "One"}, {Ref: "two", AuthID: "auth-two", Label: "Two"}}
	if errReconcile := ReconcileAccounts(store, accounts, false, now); errReconcile != nil {
		t.Fatalf("ReconcileAccounts() error = %v", errReconcile)
	}
	result, errSet := SetParticipation(store, []string{"one", "missing"}, true, now.Add(time.Second))
	if errSet != nil {
		t.Fatalf("SetParticipation() error = %v", errSet)
	}
	if result.Updated != 1 || len(result.Unknown) != 1 || result.Unknown[0] != "missing" {
		t.Fatalf("result = %#v", result)
	}
	loaded, errLoad := store.Load()
	if errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	if !loaded.Accounts["one"].Participating || loaded.Accounts["two"].Participating {
		t.Fatalf("accounts = %#v", loaded.Accounts)
	}
}

func TestLogRetentionIsBounded(t *testing.T) {
	current := New()
	for index := 0; index < MaxLogs+10; index++ {
		current.AppendLog(LogEntry{Event: "scan_completed", DurationMS: int64(index)})
	}
	if len(current.Logs) != MaxLogs || current.Logs[0].DurationMS != 10 {
		t.Fatalf("logs length=%d first=%d", len(current.Logs), current.Logs[0].DurationMS)
	}
}
