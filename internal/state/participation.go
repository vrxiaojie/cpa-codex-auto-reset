package state

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/account"
)

type ParticipationResult struct {
	Updated int
	Unknown []string
}

func ReconcileAccounts(store *Store, accounts []account.Account, defaultParticipation bool, now time.Time) error {
	if store == nil {
		return errors.New("state store is unavailable")
	}
	return store.Update(func(current *State) error {
		for _, item := range current.Accounts {
			if item != nil {
				item.Present = presencePointer(false)
			}
		}
		for _, discovered := range accounts {
			item := current.Accounts[discovered.Ref]
			if item == nil {
				item = &AccountState{
					Participating:      defaultParticipation,
					ParticipationSetAt: now.UTC(),
					Tombstones:         make(map[string]time.Time),
				}
				current.Accounts[discovered.Ref] = item
			}
			item.Present = presencePointer(true)
			item.LastSeenAt = now.UTC()
			item.Display = AccountDisplay{
				Label:    discovered.Label,
				Email:    discovered.Email,
				FileName: discovered.FileName,
				AuthID:   discovered.AuthID,
			}
		}
		return nil
	})
}

func SetParticipation(store *Store, refs []string, participating bool, now time.Time) (ParticipationResult, error) {
	result := ParticipationResult{}
	if store == nil {
		return result, errors.New("state store is unavailable")
	}
	unique := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref = strings.TrimSpace(ref); ref != "" {
			unique[ref] = struct{}{}
		}
	}
	errUpdate := store.Update(func(current *State) error {
		for ref := range unique {
			item := current.Accounts[ref]
			if item == nil || !item.IsPresent() {
				result.Unknown = append(result.Unknown, ref)
				continue
			}
			item.Participating = participating
			item.ParticipationSetAt = now.UTC()
			result.Updated++
		}
		return nil
	})
	sort.Strings(result.Unknown)
	return result, errUpdate
}

func presencePointer(value bool) *bool { return &value }
