package plugin

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestRegistrationContract(t *testing.T) {
	raw, errHandle := (&Runtime{}).Handle(pluginabi.MethodPluginRegister, []byte(`{"schema_version":1}`))
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
