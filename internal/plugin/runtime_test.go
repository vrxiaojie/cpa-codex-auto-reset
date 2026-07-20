package plugin

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestRegistrationContract(t *testing.T) {
	raw, errHandle := (&Runtime{}).Handle(pluginabi.MethodPluginRegister, []byte(`{"schema_version":1,"config_yaml":"ZW5hYmxlZDogdHJ1ZQo="}`))
	if errHandle != nil {
		t.Fatalf("Handle() error = %v", errHandle)
	}
	var envelope Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	if !envelope.OK {
		t.Fatalf("registration envelope = %#v", envelope)
	}
	var got registration
	if errUnmarshal := json.Unmarshal(envelope.Result, &got); errUnmarshal != nil {
		t.Fatalf("unmarshal registration: %v", errUnmarshal)
	}
	if got.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema version = %d, want %d", got.SchemaVersion, pluginabi.SchemaVersion)
	}
	if got.Metadata.Name != Name || got.Metadata.Version != Version {
		t.Fatalf("metadata = %#v", got.Metadata)
	}
	if !got.Capabilities.UsagePlugin || !got.Capabilities.ManagementAPI {
		t.Fatalf("capabilities = %#v", got.Capabilities)
	}
	if len(got.Metadata.ConfigFields) != 9 {
		t.Fatalf("config fields = %#v", got.Metadata.ConfigFields)
	}
}

func TestRegistrationRejectsUnknownSchema(t *testing.T) {
	raw, errHandle := (&Runtime{}).Handle(pluginabi.MethodPluginRegister, []byte(`{"schema_version":2}`))
	if errHandle != nil {
		t.Fatalf("Handle() error = %v", errHandle)
	}
	var envelope Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "unsupported_schema" {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestReconfigureRejectsStateDirChangeAndKeepsOldConfig(t *testing.T) {
	runtime := &Runtime{}
	register := []byte(`{"schema_version":1,"config_yaml":"c3RhdGUtZGlyOiAvdG1wL29uZQo="}`)
	if _, errHandle := runtime.Handle(pluginabi.MethodPluginRegister, register); errHandle != nil {
		t.Fatalf("register error = %v", errHandle)
	}
	reconfigure := []byte(`{"schema_version":1,"config_yaml":"c3RhdGUtZGlyOiAvdG1wL3R3bwo="}`)
	raw, errHandle := runtime.Handle(pluginabi.MethodPluginReconfigure, reconfigure)
	if errHandle != nil {
		t.Fatalf("reconfigure error = %v", errHandle)
	}
	var envelope Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "state_dir_change_requires_restart" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if got := runtime.Config().StateDir; got != "/tmp/one" {
		t.Fatalf("state dir = %q", got)
	}
}

func TestUnknownMethodFailsClosed(t *testing.T) {
	raw, errHandle := (&Runtime{}).Handle("scheduler.pick", nil)
	if errHandle != nil {
		t.Fatalf("Handle() error = %v", errHandle)
	}
	var envelope Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "unknown_method" {
		t.Fatalf("envelope = %#v", envelope)
	}
}
