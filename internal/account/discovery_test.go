package account

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type fakeSource struct {
	entries []pluginapi.HostAuthFileEntry
	auths   map[string]json.RawMessage
}

func (f fakeSource) ListAuthFiles() ([]pluginapi.HostAuthFileEntry, error) { return f.entries, nil }
func (f fakeSource) GetAuth(index string) (pluginapi.HostAuthGetResponse, error) {
	return pluginapi.HostAuthGetResponse{AuthIndex: index, Name: index + ".json", JSON: f.auths[index]}, nil
}

func TestDiscoverFiltersAndDeduplicatesCodexAccounts(t *testing.T) {
	source := fakeSource{
		entries: []pluginapi.HostAuthFileEntry{
			{ID: "auth-a", AuthIndex: "a", Name: "a.json", Provider: "codex", Source: "file", Disabled: true},
			{ID: "auth-b", AuthIndex: "b", Name: "b.json", Type: "codex", Source: "file"},
			{ID: "runtime", AuthIndex: "runtime", Provider: "codex", RuntimeOnly: true},
			{ID: "gemini", AuthIndex: "g", Provider: "gemini", Source: "file"},
		},
		auths: map[string]json.RawMessage{
			"a": []byte(`{"type":"codex","account_id":"account-1","access_token":"secret-a","email":"a@example.com"}`),
			"b": []byte(`{"type":"codex","account_id":"account-1","access_token":"secret-b","email":"b@example.com"}`),
		},
	}
	accounts, errDiscover := NewDiscovery(source).Discover()
	if errDiscover != nil {
		t.Fatalf("Discover() error = %v", errDiscover)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts = %#v", accounts)
	}
	if accounts[0].AuthIndex != "b" || accounts[0].AccessToken != "secret-b" || accounts[0].Disabled {
		t.Fatalf("account = %#v", accounts[0])
	}
	if accounts[0].Ref == "" || accounts[0].Ref == accounts[0].AccountID {
		t.Fatalf("unsafe account ref = %q", accounts[0].Ref)
	}
}

func TestDiscoverRequiresAccountAndAccessToken(t *testing.T) {
	source := fakeSource{
		entries: []pluginapi.HostAuthFileEntry{{AuthIndex: "a", Provider: "codex", Source: "file"}},
		auths:   map[string]json.RawMessage{"a": []byte(`{"type":"codex","account_id":"account-1"}`)},
	}
	accounts, errDiscover := NewDiscovery(source).Discover()
	if errDiscover != nil {
		t.Fatalf("Discover() error = %v", errDiscover)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts = %#v", accounts)
	}
}

func TestDiscoverIncludesNewFileBackedAccountTemporarilyReportedAsMemory(t *testing.T) {
	source := fakeSource{
		entries: []pluginapi.HostAuthFileEntry{{
			ID:        "auth-new",
			AuthIndex: "new-index",
			Name:      "codex-new.json",
			Provider:  "codex",
			Source:    "memory",
			Path:      "/auth/codex-new.json",
		}},
		auths: map[string]json.RawMessage{
			"new-index": []byte(`{"type":"codex","account_id":"account-new","access_token":"secret-new","email":"new@example.com"}`),
		},
	}

	accounts, errDiscover := NewDiscovery(source).Discover()
	if errDiscover != nil {
		t.Fatalf("Discover() error = %v", errDiscover)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts = %#v", accounts)
	}
	if accounts[0].AuthIndex != "new-index" || accounts[0].FileName != "new-index.json" {
		t.Fatalf("account = %#v", accounts[0])
	}
}

func TestDiscoverRejectsRuntimeOnlyMemoryAccount(t *testing.T) {
	source := fakeSource{
		entries: []pluginapi.HostAuthFileEntry{{
			ID:          "runtime",
			AuthIndex:   "runtime-index",
			Provider:    "codex",
			Source:      "memory",
			RuntimeOnly: true,
		}},
		auths: map[string]json.RawMessage{
			"runtime-index": []byte(`{"type":"codex","account_id":"runtime-account","access_token":"runtime-secret"}`),
		},
	}

	accounts, errDiscover := NewDiscovery(source).Discover()
	if errDiscover != nil {
		t.Fatalf("Discover() error = %v", errDiscover)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts = %#v", accounts)
	}
}

func TestDiscoverOAuthAccountIDFromIDToken(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"email": "oauth@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "oauth-account-id",
		},
	})
	source := fakeSource{
		entries: []pluginapi.HostAuthFileEntry{{
			ID:        "oauth-auth",
			AuthIndex: "oauth-index",
			Name:      "codex-oauth.json",
			Provider:  "codex",
			Source:    "file",
		}},
		auths: map[string]json.RawMessage{
			"oauth-index": json.RawMessage(`{"type":"codex","access_token":"oauth-secret","id_token":"` + idToken + `","email":"oauth@example.com"}`),
		},
	}

	accounts, errDiscover := NewDiscovery(source).Discover()
	if errDiscover != nil {
		t.Fatalf("Discover() error = %v", errDiscover)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts = %#v", accounts)
	}
	if accounts[0].AccountID != "oauth-account-id" || accounts[0].AccessToken != "oauth-secret" {
		t.Fatalf("account = %#v", accounts[0])
	}
}

func TestDiscoverRejectsOAuthAccountWithoutVerifiableAccountID(t *testing.T) {
	source := fakeSource{
		entries: []pluginapi.HostAuthFileEntry{{AuthIndex: "oauth-index", Provider: "codex", Source: "file"}},
		auths: map[string]json.RawMessage{
			"oauth-index": []byte(`{"type":"codex","access_token":"opaque-access-token","id_token":"malformed"}`),
		},
	}

	accounts, errDiscover := NewDiscovery(source).Discover()
	if errDiscover != nil {
		t.Fatalf("Discover() error = %v", errDiscover)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts = %#v", accounts)
	}
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, errMarshal := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if errMarshal != nil {
		t.Fatalf("marshal JWT header: %v", errMarshal)
	}
	payload, errMarshal := json.Marshal(claims)
	if errMarshal != nil {
		t.Fatalf("marshal JWT claims: %v", errMarshal)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
