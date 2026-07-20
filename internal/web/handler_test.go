package web

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	pluginconfig "github.com/vrxiaojie/cpa-codex-auto-reset/internal/config"
	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/state"
)

type fakeRuntime struct {
	config pluginconfig.Config
	store  *state.Store
	scans  int
}

func (f *fakeRuntime) Config() pluginconfig.Config { return f.config }
func (f *fakeRuntime) Store() *state.Store         { return f.store }
func (f *fakeRuntime) Scan(context.Context, string) (state.ScanSummary, error) {
	f.scans++
	return state.ScanSummary{Trigger: "manual"}, nil
}

func TestRegistrationDeclaresManagementAndResourceRoutes(t *testing.T) {
	registration := Registration()
	if len(registration.Routes) != 5 || len(registration.Resources) != 3 {
		t.Fatalf("registration = %#v", registration)
	}
	if registration.Routes[2].Method != http.MethodPut || registration.Resources[0].Path != "/status" {
		t.Fatalf("registration = %#v", registration)
	}
}

func TestStatusNeverReturnsManagementKey(t *testing.T) {
	runtime := seededRuntime(t)
	runtime.config.ManagementKey = "management-secret-do-not-return"
	response := New(runtime).route(managementRequest(http.MethodGet, "/v0/management"+apiPrefix+"/status", nil, nil))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if strings.Contains(string(response.Body), runtime.config.ManagementKey) {
		t.Fatalf("status leaked key: %s", response.Body)
	}
	var payload StatusResponse
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode status: %v", errDecode)
	}
	if !payload.Config.ManagementKeyConfigured {
		t.Fatalf("safe config = %#v", payload.Config)
	}
}

func TestParticipationUpdateIsAtomicAndRejectsUnknownIDs(t *testing.T) {
	runtime := seededRuntime(t)
	body := []byte(`{"auth_ids":["account-ref","missing"],"participating":true}`)
	headers := http.Header{"Origin": []string{"http://localhost:8317"}, "Sec-Fetch-Site": []string{"same-origin"}}
	response := New(runtime).route(managementRequest(http.MethodPut, "/v0/management"+apiPrefix+"/accounts/participation", headers, body))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload struct {
		Updated int      `json:"updated"`
		Unknown []string `json:"unknown"`
	}
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if payload.Updated != 1 || len(payload.Unknown) != 1 || payload.Unknown[0] != "missing" {
		t.Fatalf("payload = %#v", payload)
	}
	loaded, _ := runtime.store.Load()
	if !loaded.Accounts["account-ref"].Participating {
		t.Fatal("participation was not persisted")
	}
}

func TestCrossOriginWriteIsRejected(t *testing.T) {
	runtime := seededRuntime(t)
	headers := http.Header{"Origin": []string{"https://evil.example"}, "Sec-Fetch-Site": []string{"cross-site"}}
	response := New(runtime).route(managementRequest(http.MethodPost, "/v0/management"+apiPrefix+"/scan", headers, []byte(`{}`)))
	if response.StatusCode != http.StatusForbidden || runtime.scans != 0 {
		t.Fatalf("response=%#v scans=%d", response, runtime.scans)
	}
}

func TestAssetsUseSafeDynamicRenderingAndNoExternalRuntime(t *testing.T) {
	javascript, errRead := assets.ReadFile("assets/app.js")
	if errRead != nil {
		t.Fatalf("read app.js: %v", errRead)
	}
	if strings.Contains(string(javascript), "innerHTML") || strings.Contains(string(javascript), "https://") || strings.Contains(string(javascript), "http://") {
		t.Fatal("frontend contains unsafe rendering or external runtime reference")
	}
	page := New(seededRuntime(t)).resource(resourcePrefix + "/status")
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Headers.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("page response = %#v", page)
	}
}

func seededRuntime(t *testing.T) *fakeRuntime {
	t.Helper()
	store := state.NewStore(filepath.Join(t.TempDir(), "state"))
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if errUpdate := store.Update(func(current *state.State) error {
		current.Accounts["account-ref"] = &state.AccountState{
			Participating: false,
			LastSeenAt:    now,
			Display:       state.AccountDisplay{Label: `<img src=x onerror=alert(1)>`, Email: "user@example.com", FileName: "codex.json"},
			Tombstones:    make(map[string]time.Time),
		}
		return nil
	}); errUpdate != nil {
		t.Fatalf("seed state: %v", errUpdate)
	}
	config := pluginconfig.Defaults()
	config.Enabled = true
	config.ManagementKey = "configured"
	return &fakeRuntime{config: config, store: store}
}

func managementRequest(method, path string, headers http.Header, body []byte) pluginapi.ManagementRequest {
	return pluginapi.ManagementRequest{Method: method, Path: path, Headers: headers, Body: body}
}
