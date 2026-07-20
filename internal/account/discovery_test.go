package account

import (
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
