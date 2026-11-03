//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJSONServiceSetupPreviewContracts(t *testing.T) {
	home := isolateJSONContractHome(t)
	for name, flags := range map[string][]string{"default": nil, "with-coordinator": {"--with-coordinator"}} {
		t.Run(name, func(t *testing.T) {
			result := runJSONContract(t, "setup.services", append([]string{"setup", "services", "--dir", filepath.Join(home, name)}, flags...)...)
			assertJSONContractFields(t, result, map[string]any{"applied": false, "written": float64(0)})
			_ = contractJSONValue[string](t, result, "platform")

			services := contractJSONValue[[]any](t, result, "services")
			if len(services) == 0 {
				t.Fatal("service preview is empty")
			}

			service, ok := services[0].(map[string]any)
			if !ok {
				t.Fatalf("service must be an object: %v", services[0])
			}

			_ = contractJSONValue[string](t, service, "name")
			_ = contractJSONValue[string](t, service, "content")
			assertJSONContractFields(t, service, map[string]any{"state": "missing", "changed": true})

			if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
				t.Fatalf("preview created definition directory: %v", err)
			}
		})
	}
}
