package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultManagementURL           = "http://127.0.0.1:8317"
	DefaultManagementKeyEnv        = "CPA_MANAGEMENT_KEY"
	DefaultScanIntervalSeconds     = 300
	DefaultPostResetCooldown       = 1800
	DefaultFailureBackoff          = 300
	DefaultResetThreshold          = 95
	MinimumScanIntervalSeconds     = 60
	MinimumCooldownSeconds         = 60
	MaximumIntervalSeconds         = 7 * 24 * 60 * 60
	MinimumResetThreshold          = 80
	MaximumResetThreshold          = 100
	defaultPluginCacheSubdirectory = "cliproxyapi/plugins/cpa-codex-auto-reset"
)

type Config struct {
	Enabled                  bool
	Priority                 int
	ManagementURL            string
	ManagementKey            string
	ManagementKeyEnv         string
	ScanIntervalSeconds      int
	PostResetCooldownSeconds int
	FailureBackoffSeconds    int
	StateDir                 string
	DefaultParticipation     bool
	ResetThreshold           int
	RemoteManagement         bool
}

type SafeView struct {
	Enabled                  bool   `json:"enabled"`
	ManagementURL            string `json:"management_url"`
	ManagementKeyConfigured  bool   `json:"management_key_configured"`
	ScanIntervalSeconds      int    `json:"scan_interval_seconds"`
	PostResetCooldownSeconds int    `json:"post_reset_cooldown_seconds"`
	FailureBackoffSeconds    int    `json:"failure_backoff_seconds"`
	StateDir                 string `json:"state_dir"`
	DefaultParticipation     bool   `json:"default_participation"`
	ResetThreshold           int    `json:"reset_threshold"`
	RemoteManagementWarning  bool   `json:"remote_management_warning"`
	Complete                 bool   `json:"complete"`
}

func Defaults() Config {
	return Config{
		ManagementURL:            DefaultManagementURL,
		ManagementKeyEnv:         DefaultManagementKeyEnv,
		ScanIntervalSeconds:      DefaultScanIntervalSeconds,
		PostResetCooldownSeconds: DefaultPostResetCooldown,
		FailureBackoffSeconds:    DefaultFailureBackoff,
		StateDir:                 defaultStateDir(),
		DefaultParticipation:     false,
		ResetThreshold:           DefaultResetThreshold,
	}
}

func Parse(raw []byte, getenv func(string) string) (Config, error) {
	cfg := Defaults()
	if getenv == nil {
		getenv = os.Getenv
	}
	values, errDecode := decodeMapping(raw)
	if errDecode != nil {
		return Config{}, errDecode
	}

	if err := decodeBool(values, "enabled", &cfg.Enabled); err != nil {
		return Config{}, err
	}
	if err := decodeInt(values, "priority", &cfg.Priority); err != nil {
		return Config{}, err
	}
	if err := decodeString(values, "management-url", &cfg.ManagementURL); err != nil {
		return Config{}, err
	}
	if err := decodeString(values, "management-key", &cfg.ManagementKey); err != nil {
		return Config{}, err
	}
	if err := decodeString(values, "management-key-env", &cfg.ManagementKeyEnv); err != nil {
		return Config{}, err
	}
	if err := decodeInt(values, "scan-interval-seconds", &cfg.ScanIntervalSeconds); err != nil {
		return Config{}, err
	}
	if err := decodeInt(values, "post-reset-cooldown-seconds", &cfg.PostResetCooldownSeconds); err != nil {
		return Config{}, err
	}
	if err := decodeInt(values, "failure-backoff-seconds", &cfg.FailureBackoffSeconds); err != nil {
		return Config{}, err
	}
	if err := decodeString(values, "state-dir", &cfg.StateDir); err != nil {
		return Config{}, err
	}
	if err := decodeBool(values, "default-participation", &cfg.DefaultParticipation); err != nil {
		return Config{}, err
	}
	if err := decodeInt(values, "reset-thresh", &cfg.ResetThreshold); err != nil {
		return Config{}, err
	}

	cfg.ManagementURL = strings.TrimRight(strings.TrimSpace(cfg.ManagementURL), "/")
	cfg.ManagementKey = strings.TrimSpace(cfg.ManagementKey)
	cfg.ManagementKeyEnv = strings.TrimSpace(cfg.ManagementKeyEnv)
	cfg.StateDir = filepath.Clean(strings.TrimSpace(cfg.StateDir))
	if cfg.ManagementKey == "" && cfg.ManagementKeyEnv != "" {
		cfg.ManagementKey = strings.TrimSpace(getenv(cfg.ManagementKeyEnv))
	}
	if errValidate := cfg.Validate(); errValidate != nil {
		return Config{}, errValidate
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.ResetThreshold < MinimumResetThreshold {
		c.ResetThreshold = DefaultResetThreshold
	}
	parsed, errParse := url.Parse(c.ManagementURL)
	if errParse != nil || parsed.Host == "" {
		return errors.New("management-url must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("management-url must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return errors.New("management-url must not contain userinfo or fragment")
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return errors.New("management-url host is required")
	}
	c.RemoteManagement = !isLoopbackHost(hostname)
	if c.ScanIntervalSeconds < MinimumScanIntervalSeconds || c.ScanIntervalSeconds > MaximumIntervalSeconds {
		return fmt.Errorf("scan-interval-seconds must be between %d and %d", MinimumScanIntervalSeconds, MaximumIntervalSeconds)
	}
	if c.PostResetCooldownSeconds < MinimumCooldownSeconds || c.PostResetCooldownSeconds > MaximumIntervalSeconds {
		return fmt.Errorf("post-reset-cooldown-seconds must be between %d and %d", MinimumCooldownSeconds, MaximumIntervalSeconds)
	}
	if c.FailureBackoffSeconds < MinimumCooldownSeconds || c.FailureBackoffSeconds > MaximumIntervalSeconds {
		return fmt.Errorf("failure-backoff-seconds must be between %d and %d", MinimumCooldownSeconds, MaximumIntervalSeconds)
	}
	if c.ResetThreshold > MaximumResetThreshold {
		return fmt.Errorf("reset-thresh must not exceed %d", MaximumResetThreshold)
	}
	if c.StateDir == "" || c.StateDir == "." {
		return errors.New("state-dir must not be empty")
	}
	return nil
}

func (c Config) Safe() SafeView {
	return SafeView{
		Enabled:                  c.Enabled,
		ManagementURL:            c.ManagementURL,
		ManagementKeyConfigured:  c.ManagementKey != "",
		ScanIntervalSeconds:      c.ScanIntervalSeconds,
		PostResetCooldownSeconds: c.PostResetCooldownSeconds,
		FailureBackoffSeconds:    c.FailureBackoffSeconds,
		StateDir:                 c.StateDir,
		DefaultParticipation:     c.DefaultParticipation,
		ResetThreshold:           c.ResetThreshold,
		RemoteManagementWarning:  c.RemoteManagement,
		Complete:                 c.ManagementURL != "" && c.ManagementKey != "",
	}
}

func (c Config) EqualExceptSecret(other Config) bool {
	a, b := c, other
	a.ManagementKey = ""
	b.ManagementKey = ""
	return a == b
}

func decodeMapping(raw []byte) (map[string]*yaml.Node, error) {
	values := make(map[string]*yaml.Node)
	if len(strings.TrimSpace(string(raw))) == 0 {
		return values, nil
	}
	var document yaml.Node
	if errUnmarshal := yaml.Unmarshal(raw, &document); errUnmarshal != nil {
		return nil, errors.New("invalid plugin configuration YAML")
	}
	if len(document.Content) == 0 {
		return values, nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("plugin configuration must be a mapping")
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		key := normalizeKey(root.Content[index].Value)
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("duplicate configuration field %q", key)
		}
		values[key] = root.Content[index+1]
	}
	return values, nil
}

func normalizeKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "_", "-")
}

func decodeString(values map[string]*yaml.Node, key string, target *string) error {
	node, ok := values[key]
	if !ok {
		return nil
	}
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("%s must be a string", key)
	}
	var value string
	if errDecode := node.Decode(&value); errDecode != nil {
		return fmt.Errorf("%s must be a string", key)
	}
	*target = value
	return nil
}

func decodeBool(values map[string]*yaml.Node, key string, target *bool) error {
	node, ok := values[key]
	if !ok {
		return nil
	}
	var value bool
	if errDecode := node.Decode(&value); errDecode != nil {
		return fmt.Errorf("%s must be a boolean", key)
	}
	*target = value
	return nil
}

func decodeInt(values map[string]*yaml.Node, key string, target *int) error {
	node, ok := values[key]
	if !ok {
		return nil
	}
	var value int
	if errDecode := node.Decode(&value); errDecode != nil {
		return fmt.Errorf("%s must be an integer", key)
	}
	*target = value
	return nil
}

func defaultStateDir() string {
	cacheDir, errCache := os.UserCacheDir()
	if errCache != nil || strings.TrimSpace(cacheDir) == "" {
		return filepath.Join(os.TempDir(), defaultPluginCacheSubdirectory)
	}
	return filepath.Join(cacheDir, filepath.FromSlash(defaultPluginCacheSubdirectory))
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	if strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	return false
}
