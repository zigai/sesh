package cmdconfig

import (
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func TestConfigInitCommandDeclaresExplicitSafeRuntimeContract(t *testing.T) {
	cmd := newConfigInitCommand()

	if cmd.Annotations[clibase.AnnotationOperation] != "config_init" {
		t.Fatalf("operation annotation = %q, want config_init", cmd.Annotations[clibase.AnnotationOperation])
	}

	if cmd.Annotations[clibase.AnnotationSkipConfigLoad] != "true" {
		t.Fatalf("skip-config-load annotation = %q, want true", cmd.Annotations[clibase.AnnotationSkipConfigLoad])
	}

	for _, flag := range []string{"path", "force", clibase.JSONFlagName} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("missing --%s flag", flag)
		}
	}

	if !strings.Contains(cmd.Long, "inherited --config flag is not read") {
		t.Fatalf("long help does not explain --config behavior: %q", cmd.Long)
	}
}
