package account

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type AuthSource interface {
	ListAuthFiles() ([]pluginapi.HostAuthFileEntry, error)
	GetAuth(authIndex string) (pluginapi.HostAuthGetResponse, error)
}

type Account struct {
	Ref         string
	AuthID      string
	AuthIndex   string
	AccountID   string
	AccessToken string
	Label       string
	Email       string
	FileName    string
	Disabled    bool
}

type Discovery struct {
	source AuthSource
}

func NewDiscovery(source AuthSource) *Discovery {
	return &Discovery{source: source}
}

func (d *Discovery) Discover() ([]Account, error) {
	if d == nil || d.source == nil {
		return nil, errors.New("auth source is unavailable")
	}
	entries, errList := d.source.ListAuthFiles()
	if errList != nil {
		return nil, errList
	}
	byAccountID := make(map[string]Account)
	for _, entry := range entries {
		if !isCodexFile(entry) || strings.TrimSpace(entry.AuthIndex) == "" {
			continue
		}
		response, errGet := d.source.GetAuth(entry.AuthIndex)
		if errGet != nil {
			continue
		}
		var credential struct {
			Type        string `json:"type"`
			AccountID   string `json:"account_id"`
			AccessToken string `json:"access_token"`
			IDToken     string `json:"id_token"`
			Email       string `json:"email"`
		}
		if errUnmarshal := json.Unmarshal(response.JSON, &credential); errUnmarshal != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(credential.Type), "codex") || strings.TrimSpace(credential.AccessToken) == "" {
			continue
		}
		tokenAccountID, tokenEmail := oauthIdentity(credential.IDToken, credential.AccessToken)
		accountID := firstNonEmpty(credential.AccountID, tokenAccountID)
		if accountID == "" {
			continue
		}
		candidate := Account{
			Ref:         shortHash("account", accountID),
			AuthID:      stableAuthID(entry),
			AuthIndex:   strings.TrimSpace(entry.AuthIndex),
			AccountID:   accountID,
			AccessToken: strings.TrimSpace(credential.AccessToken),
			Label:       firstNonEmpty(entry.Label, entry.Name, credential.Email, tokenEmail, "Codex 账号"),
			Email:       firstNonEmpty(credential.Email, entry.Email, tokenEmail),
			FileName:    firstNonEmpty(response.Name, entry.Name),
			Disabled:    entry.Disabled || entry.Unavailable,
		}
		current, exists := byAccountID[accountID]
		if !exists || prefer(candidate, current) {
			byAccountID[accountID] = candidate
		}
	}
	accounts := make([]Account, 0, len(byAccountID))
	for _, item := range byAccountID {
		accounts = append(accounts, item)
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].Ref < accounts[j].Ref
	})
	return accounts, nil
}

func oauthIdentity(tokens ...string) (string, string) {
	for _, token := range tokens {
		parts := strings.Split(strings.TrimSpace(token), ".")
		if len(parts) != 3 {
			continue
		}
		payload, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		if errDecode != nil {
			continue
		}
		var claims struct {
			Email string `json:"email"`
			Auth  struct {
				AccountID string `json:"chatgpt_account_id"`
			} `json:"https://api.openai.com/auth"`
			FlatAccountID string `json:"https://api.openai.com/auth.chatgpt_account_id"`
			AccountID     string `json:"chatgpt_account_id"`
		}
		if errUnmarshal := json.Unmarshal(payload, &claims); errUnmarshal != nil {
			continue
		}
		accountID := firstNonEmpty(claims.Auth.AccountID, claims.FlatAccountID, claims.AccountID)
		if accountID != "" {
			return accountID, strings.TrimSpace(claims.Email)
		}
	}
	return "", ""
}

func isCodexFile(entry pluginapi.HostAuthFileEntry) bool {
	provider := strings.TrimSpace(entry.Provider)
	if provider == "" {
		provider = strings.TrimSpace(entry.Type)
	}
	if !strings.EqualFold(provider, "codex") {
		return false
	}
	if entry.RuntimeOnly {
		return false
	}
	source := strings.TrimSpace(entry.Source)
	if source == "" || strings.EqualFold(source, "file") {
		return true
	}
	// A newly persisted OAuth account can briefly be reported by the host as
	// memory-backed while still carrying its physical file path. GetAuth below
	// remains the authoritative check that the credential is actually readable
	// from disk, so accepting this transitional entry does not admit runtime-only
	// credentials.
	return strings.EqualFold(source, "memory") && strings.TrimSpace(entry.Path) != ""
}

func stableAuthID(entry pluginapi.HostAuthFileEntry) string {
	if value := strings.TrimSpace(entry.ID); value != "" {
		return value
	}
	if value := strings.TrimSpace(entry.AuthIndex); value != "" {
		return value
	}
	return shortHash("auth", strings.Join([]string{entry.Name, entry.Path, entry.Email}, "\x00"))
}

func prefer(candidate, current Account) bool {
	if candidate.Disabled != current.Disabled {
		return !candidate.Disabled
	}
	return candidate.AuthIndex < current.AuthIndex
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func shortHash(namespace, value string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + value))
	return hex.EncodeToString(sum[:12])
}
