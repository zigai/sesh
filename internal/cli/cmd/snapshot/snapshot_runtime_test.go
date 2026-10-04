package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/engine"
)

const testSnapshotLease = "/tmp/lease"

func TestParseSaveInputProcessEnvDefaults(t *testing.T) {
	cmd := newSnapshotSaveCommand()
	cmd.SetContext(t.Context())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	expected := config.FilterProcessEnvironmentKeys(config.DefaultProcessEnvironment)
	if !slices.Equal(cfg.ProcessEnvironmentKeys, expected) {
		t.Fatalf("ProcessEnvironmentKeys = %#v, want %#v", cfg.ProcessEnvironmentKeys, expected)
	}

	if cfg.Profile != engine.RecoveryPractical {
		t.Fatalf("Profile = %v, want RecoveryPractical", cfg.Profile)
	}
}

func TestParseSaveInputProcessEnvExplicitEmpty(t *testing.T) {
	cmd := newSnapshotSaveCommand()
	cmd.SetContext(t.Context())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
		"--process-env", "",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	if len(cfg.ProcessEnvironmentKeys) != 0 {
		t.Fatalf("ProcessEnvironmentKeys = %#v, want empty", cfg.ProcessEnvironmentKeys)
	}
}

func TestParseSaveInputProcessEnvCustomKeys(t *testing.T) {
	cmd := newSnapshotSaveCommand()
	cmd.SetContext(t.Context())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
		"--process-env", "CUSTOM_A,CUSTOM_B",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	expected := []string{"CUSTOM_A", "CUSTOM_B"}
	if !slices.Equal(cfg.ProcessEnvironmentKeys, expected) {
		t.Fatalf("ProcessEnvironmentKeys = %#v, want %#v", cfg.ProcessEnvironmentKeys, expected)
	}
}

func TestParseSaveInputProcessEnvExcludesTMUX(t *testing.T) {
	cmd := newSnapshotSaveCommand()
	cmd.SetContext(t.Context())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
		"--process-env", "TMUX,PATH,TMUX_PANE,VIRTUAL_ENV",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	expected := []string{"PATH", "VIRTUAL_ENV"}
	if !slices.Equal(cfg.ProcessEnvironmentKeys, expected) {
		t.Fatalf("ProcessEnvironmentKeys = %#v, want %#v", cfg.ProcessEnvironmentKeys, expected)
	}
}

func TestParseSaveInputProcessEnvRejectsInvalidAndExcessive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Run("rejects key containing equals", func(t *testing.T) {
		cmd := newSnapshotSaveCommand()
		cmd.SetContext(t.Context())

		if err := cmd.ParseFlags([]string{
			testRepositoryFlag, "/tmp/repo",
			"--socket", "/tmp/socket",
			"--lease-root", testSnapshotLease,
			"--session", "s0",
			"--process-env", "INVALID=KEY",
		}); err != nil {
			t.Fatal(err)
		}

		_, _, _, _, err := parseSaveInput(cmd, nil)
		if err == nil {
			t.Fatal("expected error for invalid key with '='")
		}

		if clibase.ExitCode(err) != clibase.ExitCodeUsage {
			t.Fatalf("ExitCode = %d, want usage error", clibase.ExitCode(err))
		}
	})

	t.Run("rejects excessive key count", func(t *testing.T) {
		keys := make([]string, 257)
		for i := range keys {
			keys[i] = fmt.Sprintf("K%d", i)
		}

		cmd := newSnapshotSaveCommand()
		cmd.SetContext(t.Context())

		args := []string{
			testRepositoryFlag, "/tmp/repo",
			"--socket", "/tmp/socket",
			"--lease-root", testSnapshotLease,
			"--session", "s0",
			"--process-env", strings.Join(keys, ","),
		}
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}

		_, _, _, _, err := parseSaveInput(cmd, nil)
		if err == nil {
			t.Fatal("expected error for excessive key count")
		}

		if clibase.ExitCode(err) != clibase.ExitCodeUsage {
			t.Fatalf("ExitCode = %d, want usage error", clibase.ExitCode(err))
		}
	})
}

func TestParseSaveInputConfigRouteAndAHTStore(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	ahtPath := filepath.Join(tempDir, "custom-aht.json")
	configContent := fmt.Sprintf(`
[snapshot]
process_environment = ["CONFIG_PATH", "VIRTUAL_ENV"]

[agent_status]
store_path = %q
`, ahtPath)

	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newSnapshotSaveCommand()
	cmd.Flags().String("config", configPath, "")
	cmd.Flags().Bool("no-config", false, "")
	cmd.SetContext(t.Context())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	expected := []string{"CONFIG_PATH", "VIRTUAL_ENV"}
	if !slices.Equal(cfg.ProcessEnvironmentKeys, expected) {
		t.Fatalf("ProcessEnvironmentKeys = %#v, want %#v", cfg.ProcessEnvironmentKeys, expected)
	}

	ahtStore, ok := cfg.AgentSource.(*agents.AHTStore)
	if !ok || ahtStore.StorePath() != ahtPath {
		t.Fatalf("AgentSource = %#v, want store path %q", cfg.AgentSource, ahtPath)
	}
}

func TestParseSaveInputPropagatesExplicitConfigErrors(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		assertParseSaveConfigError(t, filepath.Join(t.TempDir(), "missing.toml"), true)
	})

	t.Run("malformed", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "malformed.toml")
		if err := os.WriteFile(configPath, []byte("[snapshot\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		assertParseSaveConfigError(t, configPath, false)
	})
}

func assertParseSaveConfigError(t *testing.T, configPath string, wantMissing bool) {
	t.Helper()

	cmd := newSnapshotSaveCommand()
	cmd.Flags().String("config", "", "")
	cmd.Flags().Bool("no-config", false, "")
	cmd.SetContext(t.Context())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, filepath.Join(t.TempDir(), "repository"),
		testSocketFlag, "/tmp/socket",
		testLeaseRootFlag, testSnapshotLease,
		testSessionFlag, "s0",
		testConfigFlag, configPath,
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, sessions, cfg, err := parseSaveInput(cmd, nil)
	if err == nil {
		t.Fatal("explicit configuration error was ignored")
	}

	if repo != "" || socket != "" || sessions != nil || cfg.Profile != engine.RecoveryExact {
		t.Fatalf("failed parse returned operational input: %q / %q / %v / %+v", repo, socket, sessions, cfg)
	}

	if _, ok := errors.AsType[*clibase.ConfigurationError](err); !ok {
		t.Fatalf("error type = %T, want *configurationError", err)
	}

	if wantMissing && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want os.ErrNotExist in chain", err)
	}
}

func TestParseSaveInputStateFlagOverridesConfigAHTStore(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	configContent := `
[agent_status]
store_path = "/from/config/aht.json"
`

	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newSnapshotSaveCommand()
	cmd.Flags().String("config", configPath, "")
	cmd.Flags().Bool("no-config", false, "")
	cmd.Flags().String("state", "", "")
	cmd.SetContext(t.Context())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
		"--state", "/from/flag/aht.json",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	ahtStore, ok := cfg.AgentSource.(*agents.AHTStore)
	if !ok || ahtStore.StorePath() != "/from/flag/aht.json" {
		t.Fatalf("AgentSource = %#v, want /from/flag/aht.json", cfg.AgentSource)
	}
}

func TestParseSaveInputNoConfigFallback(t *testing.T) {
	cmd := newSnapshotSaveCommand()
	cmd.Flags().String("config", "", "")
	cmd.Flags().Bool("no-config", true, "")
	cmd.SetContext(t.Context())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
		"--no-config",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	expected := config.FilterProcessEnvironmentKeys(config.DefaultProcessEnvironment)
	if !slices.Equal(cfg.ProcessEnvironmentKeys, expected) {
		t.Fatalf("ProcessEnvironmentKeys = %#v, want %#v", cfg.ProcessEnvironmentKeys, expected)
	}
}

func TestParseSaveInputAlternateConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "empty-env.toml")

	if err := os.WriteFile(configPath, []byte("[snapshot]\nprocess_environment = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newSnapshotSaveCommand()
	cmd.Flags().String("config", configPath, "")
	cmd.Flags().Bool("no-config", false, "")
	cmd.SetContext(t.Context())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	if len(cfg.ProcessEnvironmentKeys) != 0 {
		t.Fatalf("ProcessEnvironmentKeys = %#v, want empty", cfg.ProcessEnvironmentKeys)
	}
}

func TestParseSaveInputManualFlagOverridesConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "alt.toml")

	if err := os.WriteFile(configPath, []byte("[snapshot]\nprocess_environment = [\"ALT1\", \"ALT2\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newSnapshotSaveCommand()
	cmd.Flags().String("config", configPath, "")
	cmd.Flags().Bool("no-config", false, "")
	cmd.SetContext(t.Context())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
		"--process-env", "OVERRIDE1,OVERRIDE2",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	expected := []string{"OVERRIDE1", "OVERRIDE2"}
	if !slices.Equal(cfg.ProcessEnvironmentKeys, expected) {
		t.Fatalf("ProcessEnvironmentKeys = %#v, want %#v", cfg.ProcessEnvironmentKeys, expected)
	}
}

func TestParseSaveInputManualEmptyFlagOverridesConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "alt.toml")

	if err := os.WriteFile(configPath, []byte("[snapshot]\nprocess_environment = [\"ALT1\", \"ALT2\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newSnapshotSaveCommand()
	cmd.Flags().String("config", configPath, "")
	cmd.Flags().Bool("no-config", false, "")
	cmd.SetContext(t.Context())

	if err := cmd.ParseFlags([]string{
		testRepositoryFlag, "/tmp/repo",
		"--socket", "/tmp/socket",
		"--lease-root", testSnapshotLease,
		"--session", "s0",
		"--process-env", "",
	}); err != nil {
		t.Fatal(err)
	}

	repo, socket, selected, cfg, err := parseSaveInput(cmd, nil)
	if err != nil {
		t.Fatalf("parseSaveInput failed: %v", err)
	}

	if repo == "" || socket == "" || len(selected) == 0 {
		t.Fatal("expected non-empty parsed values")
	}

	if len(cfg.ProcessEnvironmentKeys) != 0 {
		t.Fatalf("ProcessEnvironmentKeys = %#v, want empty", cfg.ProcessEnvironmentKeys)
	}
}
