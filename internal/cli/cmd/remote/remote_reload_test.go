//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/remote"
)

var errPrivateReloadFailure = errors.New("private reload failure")

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()
	if cmd == nil {
		t.Fatal("NewCommand() returned nil")
	}

	if cmd.Use != "remote" {
		t.Fatalf("cmd.Use = %q, want %q", cmd.Use, "remote")
	}

	subcommands := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	for _, expected := range []string{"peer", "lookup", "attach"} {
		if !subcommands[expected] {
			t.Errorf("missing subcommand %q in NewCommand()", expected)
		}
	}

	if peer, _, err := cmd.Find([]string{"peer"}); err != nil || !peer.Hidden {
		t.Errorf("peer protocol command must stay registered and hidden: %v", err)
	}
}

func TestDaemonMonitorCommandRunsForeground(t *testing.T) {
	cmd := NewDaemonMonitorCommand()
	if cmd.Name() != "remote-monitor" || cmd.Annotations[clibase.AnnotationLiveIO] != "true" {
		t.Fatalf("monitor command = %q live-io %q, want foreground remote-monitor", cmd.Name(), cmd.Annotations[clibase.AnnotationLiveIO])
	}
}

func TestRemoteMonitorReloadSourceDisablesRereadForStdinAndNoConfig(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "stdin", args: []string{"--config", "-"}},
		{name: "no config", args: []string{"--no-config"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := remoteReloadTestCommand(t, test.args...)

			path, load, enabled, err := remoteMonitorReloadSource(cmd)
			if err != nil {
				t.Fatal(err)
			}

			if path != "" || load != nil || enabled {
				t.Fatalf("reload source = (%q, %v, %t), want disabled", path, load != nil, enabled)
			}
		})
	}
}

func TestRemoteMonitorReloadSourceParsesProvidedExplicitFileBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeRemoteReloadTestConfig(t, path, "first")

	cmd := remoteReloadTestCommand(t, "--config", path)

	resolved, load, enabled, err := remoteMonitorReloadSource(cmd)
	if err != nil {
		t.Fatal(err)
	}

	if !enabled || resolved != path {
		t.Fatalf("reload source = (%q, %t), want explicit path", resolved, enabled)
	}

	firstContent := readRemoteReloadTestConfig(t, path)

	loaded, err := load(firstContent)
	if err != nil || loaded.SSHPath != "first" {
		t.Fatalf("first load = %#v, %v", loaded, err)
	}

	writeRemoteReloadTestConfig(t, path, "second")

	loaded, err = load(firstContent)
	if err != nil || loaded.SSHPath != "first" {
		t.Fatalf("captured-byte load = %#v, %v; want first generation", loaded, err)
	}

	secondContent := readRemoteReloadTestConfig(t, path)

	loaded, err = load(secondContent)
	if err != nil || loaded.SSHPath != "second" {
		t.Fatalf("second load = %#v, %v; want reread value", loaded, err)
	}
}

func writeRemoteReloadTestConfig(t *testing.T, path, sshPath string) {
	t.Helper()

	if err := os.WriteFile(path, []byte("[remote]\nssh_path = "+strconv.Quote(sshPath)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRemoteReloadTestConfig(t *testing.T, path string) []byte {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return content
}

func TestBridgeRemoteMonitorReloadsCoalescesFailuresAndKeepsOutputPrivate(t *testing.T) {
	var logs bytes.Buffer

	ctx, cancel := context.WithCancel(zerolog.New(&logs).WithContext(t.Context()))
	defer cancel()

	observed := make(chan remote.ConfigReload, 3)
	configs := make(chan config.RemoteConfig, 1)
	done := make(chan struct{})

	go bridgeRemoteMonitorReloads(ctx, observed, configs, done)

	observed <- remote.ConfigReload{Err: errPrivateReloadFailure}

	observed <- remote.ConfigReload{Err: errPrivateReloadFailure}

	want := config.DefaultConfig().Remote

	want.SSHPath = "PRIVATE_SSH_PATH"
	observed <- remote.ConfigReload{Remote: want}

	close(observed)

	got := <-configs

	<-done

	if got.SSHPath != want.SSHPath {
		t.Fatalf("forwarded config SSH path = %q, want %q", got.SSHPath, want.SSHPath)
	}

	records := decodeRemoteReloadLogRecords(t, logs.String())
	if len(records) != 2 {
		t.Fatalf("reload transition records = %#v, want two", records)
	}

	record := records[0]
	if record["level"] != "warn" || record["component"] != "remote_monitor" ||
		record["operation"] != "reload_config" || record["status"] != "degraded" ||
		record["reason"] != "invalid_configuration" {
		t.Fatalf("reload warning = %#v", record)
	}

	assertReloadRecovery(t, records[1])

	if strings.Contains(logs.String(), errPrivateReloadFailure.Error()) || strings.Contains(logs.String(), want.SSHPath) {
		t.Fatalf("reload warning leaked private data: %s", logs.String())
	}
}

func assertReloadRecovery(t *testing.T, record map[string]any) {
	t.Helper()

	want := map[string]any{
		"level": "info", "component": "remote_monitor", "operation": "reload_config",
		"status": "recovered", "reason": "configuration_valid", "message": "remote monitor configuration reload recovered",
	}
	if diff := cmp.Diff(want, record); diff != "" {
		t.Fatalf("incorrect reload recovery: %s", diff)
	}
}

func decodeRemoteReloadLogRecords(t *testing.T, output string) []map[string]any {
	t.Helper()

	records := make([]map[string]any, 0, 1)

	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}

		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}

		records = append(records, record)
	}

	return records
}

func remoteReloadTestCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("config", "", "")
	cmd.Flags().Bool("no-config", false, "")

	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}

	return cmd
}

func TestRemoteLookupDoesNotInitializeMissingPeerState(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	if _, err := openRemoteLookupRuntime(); err == nil {
		t.Fatal("openRemoteLookupRuntime() succeeded without peer state")
	}

	if _, err := os.Stat(filepath.Join(stateHome, "sesh")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read-only lookup initialized state: %v", err)
	}
}

func TestRemoteLookupTreatsMissingReceiptDirectoryAsNotFoundState(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	statePath, err := remote.PeerStateDir(stateHome)
	if err != nil {
		t.Fatal(err)
	}

	dir, err := privatefs.Open(statePath, false)
	if err != nil {
		t.Fatal(err)
	}

	peerID, idErr := remote.LoadOrCreatePeerID(dir)

	closeErr := dir.Close()
	if err := errors.Join(idErr, closeErr); err != nil {
		t.Fatal(err)
	}

	runtime, err := openRemoteLookupRuntime()
	if err != nil {
		t.Fatal(err)
	}

	if runtime.peerID != peerID || runtime.receipts != nil {
		t.Fatalf("lookup runtime = %#v, want existing peer and absent receipts", runtime)
	}
}

func TestOriginPickerDiscoveryDoesNotRequireOutboundConnections(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = false

	services, err := DefaultPickerRemoteFactory(t.Context(), cfg, "/tmp/unused-origin-test.sock")
	if err != nil || services.OriginBridge == nil || services.Attach != nil {
		t.Fatalf("origin-only services were not wired: %#v, %v", services, err)
	}

	cfg.Remote.EnableReverseBridge = false

	services, err = DefaultPickerRemoteFactory(t.Context(), cfg, "/tmp/unused-origin-test.sock")
	if err != nil || services.OriginBridge != nil {
		t.Fatalf("disabled origin bridge was wired: %#v, %v", services, err)
	}
}
