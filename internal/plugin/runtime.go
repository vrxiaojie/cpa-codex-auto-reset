package plugin

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	pluginconfig "github.com/vrxiaojie/cpa-codex-auto-reset/internal/config"
)

const (
	ID         = "cpa-codex-auto-reset"
	Name       = "Codex Auto Reset"
	Version    = "0.1.0"
	Author     = "vrxiaojie"
	Repository = "https://github.com/vrxiaojie/cpa-codex-auto-reset"
)

type HostCaller func(method string, payload any) (json.RawMessage, error)

type Runtime struct {
	mu         sync.RWMutex
	hostCaller HostCaller
	closed     bool
	configured bool
	config     pluginconfig.Config
}

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

type EnvelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	UsagePlugin   bool `json:"usage_plugin"`
	ManagementAPI bool `json:"management_api"`
}

var defaultRuntime = &Runtime{}

func Default() *Runtime { return defaultRuntime }

func (r *Runtime) SetHostCaller(caller HostCaller) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostCaller = caller
}

func (r *Runtime) Handle(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req lifecycleRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return nil, errors.New("invalid lifecycle request")
			}
		}
		if req.SchemaVersion != 0 && req.SchemaVersion != pluginabi.SchemaVersion {
			return ErrorEnvelope("unsupported_schema", "unsupported plugin schema version"), nil
		}
		cfg, errParse := pluginconfig.Parse(req.ConfigYAML, nil)
		if errParse != nil {
			return ErrorEnvelope("invalid_config", SanitizeError(errParse)), nil
		}
		r.mu.Lock()
		if method == pluginabi.MethodPluginReconfigure && r.configured && r.config.StateDir != cfg.StateDir {
			r.mu.Unlock()
			return ErrorEnvelope("state_dir_change_requires_restart", "state-dir cannot be changed during hot reconfiguration"), nil
		}
		r.config = cfg
		r.configured = true
		r.closed = false
		r.mu.Unlock()
		return OKEnvelope(Registration()), nil
	case pluginabi.MethodUsageHandle:
		return OKEnvelope(struct{}{}), nil
	case pluginabi.MethodManagementRegister:
		return OKEnvelope(pluginapi.ManagementRegistrationResponse{}), nil
	default:
		return ErrorEnvelope("unknown_method", "unknown method"), nil
	}
}

func (r *Runtime) Shutdown() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func (r *Runtime) Config() pluginconfig.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config
}

func Registration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             Name,
			Version:          Version,
			Author:           Author,
			GitHubRepository: Repository,
			ConfigFields:     configFields(),
		},
		Capabilities: registrationCapabilities{
			UsagePlugin:   true,
			ManagementAPI: true,
		},
	}
}

func configFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{Name: "management-url", Type: pluginapi.ConfigFieldTypeString, Description: "CLIProxyAPI Management API root URL. Defaults to http://127.0.0.1:8317."},
		{Name: "management-key", Type: pluginapi.ConfigFieldTypeString, Description: "CLIProxyAPI Management Key. Stored in memory only; prefer management-key-env."},
		{Name: "management-key-env", Type: pluginapi.ConfigFieldTypeString, Description: "Environment variable containing the Management Key. Defaults to CPA_MANAGEMENT_KEY."},
		{Name: "scan-interval-seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Background scan interval in seconds. Minimum 60."},
		{Name: "post-reset-cooldown-seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Cooldown after a successful or already-redeemed reset."},
		{Name: "failure-backoff-seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Initial persistent backoff after a confirmed failure."},
		{Name: "state-dir", Type: pluginapi.ConfigFieldTypeString, Description: "Private persistent state directory."},
		{Name: "default-participation", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Whether newly discovered accounts participate. Safe default is false."},
		{Name: "reset_thresh", Type: pluginapi.ConfigFieldTypeInteger, Description: "Usage threshold percentage for reset eligibility. Range 80-100; default 95."},
	}
}

func OKEnvelope(value any) []byte {
	result, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return ErrorEnvelope("marshal_error", "failed to encode plugin response")
	}
	raw, _ := json.Marshal(Envelope{OK: true, Result: result})
	return raw
}

func ErrorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(Envelope{OK: false, Error: &EnvelopeError{Code: code, Message: message}})
	return raw
}

func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "plugin error"
	}
	if len(text) > 240 {
		text = text[:240]
	}
	return text
}
