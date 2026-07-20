package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	pluginconfig "github.com/vrxiaojie/cpa-codex-auto-reset/internal/config"
	"github.com/vrxiaojie/cpa-codex-auto-reset/internal/state"
)

const (
	apiPrefix      = "/plugins/cpa-codex-auto-reset"
	resourcePrefix = "/v0/resource/plugins/cpa-codex-auto-reset"
	maxRequestBody = 1 << 20
)

//go:embed assets/index.html assets/app.css assets/app.js
var assets embed.FS

type Runtime interface {
	Config() pluginconfig.Config
	Store() *state.Store
	Scan(context.Context, string) (state.ScanSummary, error)
}

type Handler struct {
	runtime Runtime
}

type registration struct {
	Routes    []route    `json:"routes,omitempty"`
	Resources []resource `json:"resources,omitempty"`
}

type route struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type resource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type StatusResponse struct {
	PluginID string                `json:"plugin_id"`
	Version  string                `json:"version"`
	Config   pluginconfig.SafeView `json:"config"`
	LastScan state.ScanSummary     `json:"last_scan"`
	NextScan time.Time             `json:"next_scan,omitempty"`
	Counts   StatusCounts          `json:"counts"`
}

type StatusCounts struct {
	Total            int `json:"total"`
	Participating    int `json:"participating"`
	NotParticipating int `json:"not_participating"`
	WithCredits      int `json:"with_credits"`
	CoolingOrBackoff int `json:"cooling_or_backoff"`
	Errors           int `json:"errors"`
}

type AccountResponse struct {
	Accounts []AccountView `json:"accounts"`
}

type AccountView struct {
	ID                string    `json:"id"`
	Label             string    `json:"label"`
	Email             string    `json:"email,omitempty"`
	FileName          string    `json:"file_name,omitempty"`
	Participating     bool      `json:"participating"`
	AvailableCredits  int       `json:"available_credits"`
	EarliestExpiresAt time.Time `json:"earliest_expires_at,omitempty"`
	UsedPercent       float64   `json:"used_percent"`
	Blocked           bool      `json:"blocked"`
	LastResult        string    `json:"last_result,omitempty"`
	NextAllowedAt     time.Time `json:"next_allowed_at,omitempty"`
	LastScannedAt     time.Time `json:"last_scanned_at,omitempty"`
	ErrorCode         string    `json:"error_code,omitempty"`
}

func New(runtime Runtime) *Handler { return &Handler{runtime: runtime} }

func Registration() registration {
	return registration{
		Routes: []route{
			{Method: http.MethodGet, Path: apiPrefix + "/status", Description: "Plugin status and safe configuration summary."},
			{Method: http.MethodGet, Path: apiPrefix + "/accounts", Description: "Discovered account participation and quota summary."},
			{Method: http.MethodPut, Path: apiPrefix + "/accounts/participation", Description: "Atomically update account participation."},
			{Method: http.MethodGet, Path: apiPrefix + "/logs", Description: "Bounded reset decision log."},
			{Method: http.MethodPost, Path: apiPrefix + "/scan", Description: "Trigger a safe manual scan."},
		},
		Resources: []resource{
			{Path: "/status", Menu: "Codex 自动重置", Description: "管理 Codex 自动重置参与账号并查看日志。"},
			{Path: "/app.css", Description: "Codex Auto Reset styles."},
			{Path: "/app.js", Description: "Codex Auto Reset application."},
		},
	}
}

func (h *Handler) Handle(raw []byte) (pluginapi.ManagementResponse, error) {
	var request pluginapi.ManagementRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return pluginapi.ManagementResponse{}, errors.New("invalid management request")
	}
	return h.route(request), nil
}

func (h *Handler) route(request pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	path := strings.TrimSpace(request.Path)
	if strings.HasPrefix(path, resourcePrefix) {
		return h.resource(path)
	}
	if strings.HasPrefix(path, "/v0/management") {
		path = strings.TrimPrefix(path, "/v0/management")
	}
	switch {
	case request.Method == http.MethodGet && path == apiPrefix+"/status":
		return h.status()
	case request.Method == http.MethodGet && path == apiPrefix+"/accounts":
		return h.accounts()
	case request.Method == http.MethodGet && path == apiPrefix+"/logs":
		return h.logs()
	case request.Method == http.MethodPut && path == apiPrefix+"/accounts/participation":
		if !h.sameOrigin(request.Headers) {
			return jsonError(http.StatusForbidden, "cross_origin_rejected")
		}
		return h.participation(request.Body)
	case request.Method == http.MethodPost && path == apiPrefix+"/scan":
		if !h.sameOrigin(request.Headers) {
			return jsonError(http.StatusForbidden, "cross_origin_rejected")
		}
		return h.scan()
	default:
		return jsonError(http.StatusNotFound, "route_not_found")
	}
}

func (h *Handler) status() pluginapi.ManagementResponse {
	current, errLoad := h.load()
	if errLoad != nil {
		return jsonError(http.StatusServiceUnavailable, "state_unavailable")
	}
	cfg := h.runtime.Config()
	response := StatusResponse{PluginID: "cpa-codex-auto-reset", Version: "0.1.0", Config: cfg.Safe(), LastScan: current.LastScan}
	if !current.LastScan.FinishedAt.IsZero() {
		response.NextScan = current.LastScan.FinishedAt.Add(time.Duration(cfg.ScanIntervalSeconds) * time.Second)
	}
	for _, item := range current.Accounts {
		if item == nil {
			continue
		}
		response.Counts.Total++
		if item.Participating {
			response.Counts.Participating++
		} else {
			response.Counts.NotParticipating++
		}
		if item.AvailableCredits > 0 {
			response.Counts.WithCredits++
		}
		if !nextAllowedAt(item).IsZero() {
			response.Counts.CoolingOrBackoff++
		}
		if item.LastErrorCode != "" {
			response.Counts.Errors++
		}
	}
	return jsonResponse(http.StatusOK, response)
}

func (h *Handler) accounts() pluginapi.ManagementResponse {
	current, errLoad := h.load()
	if errLoad != nil {
		return jsonError(http.StatusServiceUnavailable, "state_unavailable")
	}
	response := AccountResponse{Accounts: make([]AccountView, 0, len(current.Accounts))}
	for ref, item := range current.Accounts {
		if item == nil {
			continue
		}
		response.Accounts = append(response.Accounts, AccountView{
			ID:                ref,
			Label:             item.Display.Label,
			Email:             item.Display.Email,
			FileName:          item.Display.FileName,
			Participating:     item.Participating,
			AvailableCredits:  item.AvailableCredits,
			EarliestExpiresAt: item.EarliestExpiresAt,
			UsedPercent:       item.UsedPercent,
			Blocked:           item.Blocked,
			LastResult:        item.LastResult,
			NextAllowedAt:     nextAllowedAt(item),
			LastScannedAt:     item.LastScannedAt,
			ErrorCode:         item.LastErrorCode,
		})
	}
	sort.Slice(response.Accounts, func(i, j int) bool {
		return strings.ToLower(response.Accounts[i].Label)+response.Accounts[i].ID < strings.ToLower(response.Accounts[j].Label)+response.Accounts[j].ID
	})
	return jsonResponse(http.StatusOK, response)
}

func (h *Handler) logs() pluginapi.ManagementResponse {
	current, errLoad := h.load()
	if errLoad != nil {
		return jsonError(http.StatusServiceUnavailable, "state_unavailable")
	}
	logs := append([]state.LogEntry(nil), current.Logs...)
	for left, right := 0, len(logs)-1; left < right; left, right = left+1, right-1 {
		logs[left], logs[right] = logs[right], logs[left]
	}
	return jsonResponse(http.StatusOK, map[string]any{"logs": logs})
}

func (h *Handler) participation(raw []byte) pluginapi.ManagementResponse {
	if len(raw) == 0 || len(raw) > maxRequestBody {
		return jsonError(http.StatusBadRequest, "invalid_request_body")
	}
	var request struct {
		AuthIDs       []string `json:"auth_ids"`
		Participating *bool    `json:"participating"`
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(raw), maxRequestBody))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&request); errDecode != nil || request.Participating == nil || len(request.AuthIDs) == 0 || len(request.AuthIDs) > 1000 {
		return jsonError(http.StatusBadRequest, "invalid_request_body")
	}
	result, errSet := state.SetParticipation(h.runtime.Store(), request.AuthIDs, *request.Participating, time.Now().UTC())
	if errSet != nil {
		return jsonError(http.StatusServiceUnavailable, "state_update_failed")
	}
	accountsResponse := h.accounts()
	var accountsPayload AccountResponse
	_ = json.Unmarshal(accountsResponse.Body, &accountsPayload)
	return jsonResponse(http.StatusOK, map[string]any{
		"updated":  result.Updated,
		"unknown":  result.Unknown,
		"accounts": accountsPayload.Accounts,
	})
}

func (h *Handler) scan() pluginapi.ManagementResponse {
	summary, errScan := h.runtime.Scan(context.Background(), "manual")
	if errScan != nil {
		return jsonResponse(http.StatusConflict, map[string]any{"ok": false, "error": "scan_not_started", "summary": summary})
	}
	return jsonResponse(http.StatusAccepted, map[string]any{"ok": true, "summary": summary})
}

func (h *Handler) resource(path string) pluginapi.ManagementResponse {
	name := strings.TrimPrefix(path, resourcePrefix+"/")
	assetName, contentType := "", ""
	switch name {
	case "status":
		assetName, contentType = "assets/index.html", "text/html; charset=utf-8"
	case "app.css":
		assetName, contentType = "assets/app.css", "text/css; charset=utf-8"
	case "app.js":
		assetName, contentType = "assets/app.js", "text/javascript; charset=utf-8"
	default:
		return jsonError(http.StatusNotFound, "resource_not_found")
	}
	body, errRead := assets.ReadFile(assetName)
	if errRead != nil {
		return jsonError(http.StatusInternalServerError, "resource_unavailable")
	}
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{contentType},
			"Content-Security-Policy": []string{"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'none'; connect-src 'self'; base-uri 'none'; frame-ancestors 'self'"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"Referrer-Policy":         []string{"same-origin"},
			"Cache-Control":           []string{"no-store"},
		},
		Body: body,
	}
}

func (h *Handler) sameOrigin(headers http.Header) bool {
	if site := strings.ToLower(strings.TrimSpace(headers.Get("Sec-Fetch-Site"))); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	value := strings.TrimSpace(headers.Get("Origin"))
	if value == "" {
		value = strings.TrimSpace(headers.Get("Referer"))
	}
	if value == "" {
		return true
	}
	source, errSource := url.Parse(value)
	target, errTarget := url.Parse(h.runtime.Config().ManagementURL)
	if errSource != nil || errTarget != nil || source.Hostname() == "" || target.Hostname() == "" {
		return false
	}
	return equivalentHost(source, target)
}

func equivalentHost(left, right *url.URL) bool {
	leftHost, rightHost := strings.ToLower(left.Hostname()), strings.ToLower(right.Hostname())
	leftPort, rightPort := normalizedPort(left), normalizedPort(right)
	if leftPort != rightPort {
		return false
	}
	if leftHost == rightHost {
		return true
	}
	return isLoopback(leftHost) && isLoopback(rightHost)
}

func normalizedPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if value.Scheme == "https" {
		return "443"
	}
	return "80"
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	return net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func (h *Handler) load() (state.State, error) {
	if h == nil || h.runtime == nil || h.runtime.Store() == nil {
		return state.State{}, errors.New("state store is unavailable")
	}
	return h.runtime.Store().Load()
}

func nextAllowedAt(item *state.AccountState) time.Time {
	var next time.Time
	for _, candidate := range []time.Time{
		cooldownUntil(item.PostResetCooldown),
		backoffUntil(item.FailureBackoff),
		pendingClearUntil(item.PendingLocalClear),
	} {
		if candidate.After(next) {
			next = candidate
		}
	}
	return next
}

func cooldownUntil(value *state.Cooldown) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.Until
}

func backoffUntil(value *state.Backoff) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.Until
}

func pendingClearUntil(value *state.PendingLocalClear) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.NextRetryAt
}

func jsonResponse(status int, payload any) pluginapi.ManagementResponse {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return jsonError(http.StatusInternalServerError, "response_encode_failed")
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":           []string{"application/json; charset=utf-8"},
			"Cache-Control":          []string{"no-store"},
			"X-Content-Type-Options": []string{"nosniff"},
		},
		Body: body,
	}
}

func jsonError(status int, code string) pluginapi.ManagementResponse {
	return jsonResponse(status, map[string]any{"ok": false, "error": code})
}
