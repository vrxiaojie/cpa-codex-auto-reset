package scripts

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	pluginruntime "github.com/vrxiaojie/cpa-codex-auto-reset/internal/plugin"
)

func TestRegistryAndReleaseContractUseVersion017(t *testing.T) {
	rawRegistry, errRead := os.ReadFile("../registry.json")
	if errRead != nil {
		t.Fatalf("read registry: %v", errRead)
	}
	var registry struct {
		SchemaVersion int `json:"schema_version"`
		Plugins       []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if errDecode := json.Unmarshal(rawRegistry, &registry); errDecode != nil {
		t.Fatalf("decode registry: %v", errDecode)
	}
	if registry.SchemaVersion != 1 || len(registry.Plugins) != 1 || registry.Plugins[0].ID != "cpa-codex-auto-reset" || registry.Plugins[0].Version != "0.1.7" {
		t.Fatalf("registry = %#v", registry)
	}
	if registry.Plugins[0].Version != pluginruntime.Version {
		t.Fatalf("registry version %q differs from code version %q", registry.Plugins[0].Version, pluginruntime.Version)
	}
	rawScript, errRead := os.ReadFile("verify-release-assets.sh")
	if errRead != nil {
		t.Fatalf("read verifier: %v", errRead)
	}
	for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64", "windows_amd64"} {
		if !strings.Contains(string(rawScript), target) {
			t.Fatalf("verifier missing target %s", target)
		}
	}
	rawWorkflow, errRead := os.ReadFile("../.github/workflows/release.yml")
	if errRead != nil {
		t.Fatalf("read workflow: %v", errRead)
	}
	var workflow yaml.Node
	if errDecode := yaml.Unmarshal(rawWorkflow, &workflow); errDecode != nil {
		t.Fatalf("decode workflow YAML: %v", errDecode)
	}
	if !strings.Contains(string(rawWorkflow), "-buildmode=c-shared") || !strings.Contains(string(rawWorkflow), "checksums.txt") {
		t.Fatal("workflow is missing c-shared build or checksum publication")
	}
	workflowText := string(rawWorkflow)
	setupMSYS2 := strings.Index(workflowText, "msys2/setup-msys2@v2")
	buildPackage := strings.Index(workflowText, "name: Build and package")
	if setupMSYS2 < 0 || !strings.Contains(workflowText, "mingw-w64-x86_64-gcc") {
		t.Fatal("workflow must install the Windows MinGW64 C compiler")
	}
	if buildPackage < 0 || setupMSYS2 > buildPackage {
		t.Fatal("workflow must install the Windows C compiler before building")
	}
	if !strings.Contains(workflowText, "id: setup-msys2") || !strings.Contains(workflowText, "steps.setup-msys2.outputs.msys2-location") {
		t.Fatal("workflow must resolve the Windows compiler from the setup-msys2 output")
	}
	if strings.Contains(workflowText, "C:/msys64/mingw64/bin/gcc.exe") {
		t.Fatal("workflow must not assume a fixed MSYS2 installation directory")
	}
}
