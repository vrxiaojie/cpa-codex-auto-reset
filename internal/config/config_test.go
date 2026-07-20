package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDefaultsAndSecretEnvironment(t *testing.T) {
	cfg, errParse := Parse([]byte("enabled: true\nmanagement-key-env: TEST_KEY\n"), func(name string) string {
		if name == "TEST_KEY" {
			return "super-secret"
		}
		return ""
	})
	if errParse != nil {
		t.Fatalf("Parse() error = %v", errParse)
	}
	if cfg.ManagementURL != DefaultManagementURL || cfg.ScanIntervalSeconds != DefaultScanIntervalSeconds {
		t.Fatalf("defaults = %#v", cfg)
	}
	if cfg.ManagementKey != "super-secret" {
		t.Fatal("management key was not loaded from environment")
	}
	safe := cfg.Safe()
	if !safe.ManagementKeyConfigured || !safe.Complete {
		t.Fatalf("safe config = %#v", safe)
	}
	rawSafe, errMarshal := json.Marshal(safe)
	if errMarshal != nil {
		t.Fatalf("marshal safe config: %v", errMarshal)
	}
	if strings.Contains(string(rawSafe), "super-secret") {
		t.Fatal("safe config leaked management key")
	}
}

func TestParseHyphenAndUnderscoreAliases(t *testing.T) {
	raw := []byte("scan_interval_seconds: 120\npost-reset-cooldown-seconds: 600\nfailure_backoff_seconds: 900\nreset_thresh: 96\n")
	cfg, errParse := Parse(raw, func(string) string { return "" })
	if errParse != nil {
		t.Fatalf("Parse() error = %v", errParse)
	}
	if cfg.ScanIntervalSeconds != 120 || cfg.PostResetCooldownSeconds != 600 || cfg.FailureBackoffSeconds != 900 || cfg.ResetThreshold != 96 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestManagementKeyValueTakesPriority(t *testing.T) {
	cfg, errParse := Parse([]byte("management-key: direct-secret\nmanagement-key-env: TEST_KEY\n"), func(string) string {
		return "environment-secret"
	})
	if errParse != nil {
		t.Fatalf("Parse() error = %v", errParse)
	}
	if cfg.ManagementKey != "direct-secret" {
		t.Fatalf("management key = %q", cfg.ManagementKey)
	}
}

func TestValidationBoundaries(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "short interval", raw: "scan-interval-seconds: 59\n"},
		{name: "threshold low", raw: "reset_thresh: 79\n"},
		{name: "userinfo", raw: "management-url: http://user@example.com\n"},
		{name: "fragment", raw: "management-url: http://localhost:8317/#secret\n"},
		{name: "scheme", raw: "management-url: file:///tmp/api\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, errParse := Parse([]byte(test.raw), func(string) string { return "" }); errParse == nil {
				t.Fatalf("Parse(%q) succeeded", test.raw)
			}
		})
	}
}

func TestRemoteManagementWarning(t *testing.T) {
	cfg, errParse := Parse([]byte("management-url: https://management.example.com\n"), nil)
	if errParse != nil {
		t.Fatalf("Parse() error = %v", errParse)
	}
	if !cfg.RemoteManagement || !cfg.Safe().RemoteManagementWarning {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestDuplicateAliasRejectedWithoutSecretLeak(t *testing.T) {
	secret := "do-not-log-this"
	_, errParse := Parse([]byte("management-key: "+secret+"\nmanagement_key: second\n"), nil)
	if errParse == nil {
		t.Fatal("Parse() succeeded")
	}
	if strings.Contains(errParse.Error(), secret) {
		t.Fatalf("error leaked secret: %v", errParse)
	}
}
