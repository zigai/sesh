//go:build linux || darwin

package cmdpicker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const (
	testRepositoryFlag = "--repository"
	testSocketFlag     = "--socket"
)

func newTestRootCommand(ctx context.Context) *cobra.Command {
	rootCommand := &cobra.Command{
		Use:           "sesh",
		Short:         "Native tmux session persistence",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	rootCommand.PersistentFlags().String("config", "", "Explicit native TOML configuration file")
	rootCommand.PersistentFlags().Bool("no-config", false, "Use built-in settings without reading configuration files")

	rootCommand.AddCommand(
		NewPopupCommand(ctx),
		NewInlinePickerCommand(ctx),
		NewStandalonePickerCommand(ctx),
	)

	return rootCommand
}

func TestStandalonePickerRejectsNonTerminalBeforeRepositoryCreation(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	root := newTestRootCommand(t.Context())
	root.SetIn(bytes.NewBufferString("not a terminal"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{
		"standalone", "--no-config", testSocketFlag, "/tmp/sesh-picker-missing.sock",
		testRepositoryFlag, repository,
	})

	err := root.Execute()
	if !errors.Is(err, errPickerTerminalRequired) {
		t.Fatalf("standalone error = %v, want terminal required", err)
	}

	if _, statErr := os.Stat(repository); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("repository created before terminal validation: %v", statErr)
	}
}

func TestPopupCommandUsesCurrentTerminalUnlessTmuxRequested(t *testing.T) {
	t.Parallel()

	cmd := NewPopupCommand(t.Context())
	tmuxFlag := cmd.Flags().Lookup("tmux")

	if tmuxFlag == nil || tmuxFlag.DefValue != "false" {
		t.Fatalf("--tmux flag = %#v, want exposed false default", tmuxFlag)
	}

	if embedded := cmd.Flags().Lookup("embedded"); embedded != nil {
		t.Fatalf("obsolete --embedded flag remains registered: %#v", embedded)
	}
}

func TestPopupChildReceivesInvokingPaneSessionAndRepository(t *testing.T) {
	t.Parallel()

	cmd := NewPopupCommand(t.Context())

	if err := cmd.Flags().Set("repository", "/private/repository"); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Flags().Set("tmux", "true"); err != nil {
		t.Fatal(err)
	}

	arguments := pickerPopupArguments(cmd, "/private/tmux.sock", "/dev/pts/1", "$7", "%2", "/private/handoff")
	if len(arguments) < 3 || arguments[0] != "popup" {
		t.Fatalf("popup child arguments = %#v", arguments)
	}

	if slices.Contains(arguments, "--tmux") || slices.Contains(arguments, "--embedded") {
		t.Fatalf("popup child arguments recurse through launcher: %#v", arguments)
	}

	for _, required := range []string{"--repository", "/private/repository", "--target-pane", "%2", "--current-session-id", "$7", "--switch-handoff", "/private/handoff"} {
		if !slices.Contains(arguments, required) {
			t.Fatalf("popup child arguments lack %q: %#v", required, arguments)
		}
	}
}

func TestPickerCommandsExposeLiveInteractiveSurface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		new  func(...PickerRemoteFactory) interface{ Name() string }
	}{
		{name: "popup", new: func(remote ...PickerRemoteFactory) interface{ Name() string } {
			return NewPopupCommand(t.Context(), remote...)
		}},
		{name: "inline", new: func(remote ...PickerRemoteFactory) interface{ Name() string } {
			return NewInlinePickerCommand(t.Context(), remote...)
		}},
		{name: "standalone", new: func(remote ...PickerRemoteFactory) interface{ Name() string } {
			return NewStandalonePickerCommand(t.Context(), remote...)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := test.new()

			if cmd.Name() != test.name {
				t.Fatalf("command name = %q, want %q", cmd.Name(), test.name)
			}

			concrete := NewPopupCommand(t.Context())

			switch test.name {
			case "inline":
				concrete = NewInlinePickerCommand(t.Context())
			case "standalone":
				concrete = NewStandalonePickerCommand(t.Context())
			}

			if concrete.Annotations[clibase.AnnotationLiveIO] != "true" {
				t.Fatalf("live I/O annotation = %q", concrete.Annotations[clibase.AnnotationLiveIO])
			}

			for _, flag := range []string{"socket", "repository", "lease-root", "history", "agent-state"} {
				if concrete.Flags().Lookup(flag) == nil {
					t.Fatalf("missing --%s", flag)
				}
			}
		})
	}
}

func TestPickerPopupUsesApplicationOwnedFrame(t *testing.T) {
	t.Parallel()

	options := pickerPopupOptions(gotmux.Exec("/bin/true"), "120", "32")
	if !options.Borderless || options.Title != "" {
		t.Fatalf("popup frame = borderless %t, title %q; want application-owned border", options.Borderless, options.Title)
	}

	if options.Style != "bg=#000000" || options.X != "C" || options.Y != "C" {
		t.Fatalf("popup presentation = style %q at %q,%q", options.Style, options.X, options.Y)
	}

	if !options.CloseOnExit || options.CloseOnSuccess || options.CloseOnKey || options.DisableDismiss {
		t.Fatalf("popup close policy = %+v", options)
	}

	if options.Width != "120" || options.Height != "32" {
		t.Fatalf("popup dimensions = %q x %q", options.Width, options.Height)
	}
}

func TestPickerPopupBalancesConfiguredPercentageForClientGeometry(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Popup.AgentsWidth = "80%"
	cfg.Popup.AgentsHeight = "70%"
	state := &session.State{PopupDisplayMode: "agents"}

	tests := []struct {
		name       string
		available  gotmux.Size
		wantWidth  string
		wantHeight string
	}{
		{name: "portrait", available: gotmux.Size{Width: 100, Height: 200}, wantWidth: "98", wantHeight: "25"},
		{name: "landscape", available: gotmux.Size{Width: 200, Height: 100}, wantWidth: "198", wantHeight: "50"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			width, height := pickerPopupDimensions(cfg, state, test.available)
			if width != test.wantWidth || height != test.wantHeight {
				t.Fatalf("popup dimensions = %s x %s, want %s x %s", width, height, test.wantWidth, test.wantHeight)
			}
		})
	}
}

func TestPickerPopupPreservesConfiguredDimensionsWithoutClientGeometry(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	width, height := pickerPopupDimensions(cfg, &session.State{}, gotmux.Size{Width: 0, Height: 0})

	if width != cfg.Popup.SeshWidth || height != cfg.Popup.SeshHeight {
		t.Fatalf("popup dimensions = %q x %q, want configured %q x %q", width, height, cfg.Popup.SeshWidth, cfg.Popup.SeshHeight)
	}
}

func TestPickerPopupDimensionsFollowEffectiveConfiguredMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		modes []string
		state *session.State
		want  string
	}{
		{name: "agents only", modes: []string{"agents"}, state: &session.State{}, want: "agents"},
		{name: "remembered mode disabled", modes: []string{"sesh"}, state: &session.State{PopupDisplayMode: "agents"}, want: "sesh"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.DefaultConfig()
			cfg.Settings.DisplayModes = test.modes
			width, height := pickerPopupDimensions(cfg, test.state, gotmux.Size{Width: 0, Height: 0})

			wantWidth, wantHeight := cfg.Popup.SeshWidth, cfg.Popup.SeshHeight
			if test.want == "agents" {
				wantWidth, wantHeight = cfg.Popup.AgentsWidth, cfg.Popup.AgentsHeight
			}

			if width != wantWidth || height != wantHeight {
				t.Fatalf("popup dimensions = %q x %q, want effective %s dimensions %q x %q", width, height, test.want, wantWidth, wantHeight)
			}
		})
	}
}

func TestPickerServiceRejectsReplacementSessionBeforeMutation(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	original, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "picker-target"})
	if err != nil {
		t.Fatalf("create fixture session: %v", err)
	}

	dirs := t.TempDir()
	cfg := config.DefaultConfig()

	services, err := newPickerServices(
		ctx,
		cfg,
		server.Endpoint().SocketPath,
		filepath.Join(dirs, "repository"),
		filepath.Join(dirs, "leases"),
		filepath.Join(dirs, "history.json"),
		filepath.Join(dirs, "agents.json"),
		gotmux.TerminalStreams{},
		PickerRemoteServices{},
	)
	if err != nil {
		t.Fatalf("new picker services: %v", err)
	}

	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Errorf("close picker services: %v", err)
		}
	})

	catalog, err := services.LoadCatalog(ctx)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	var selected session.Session

	for _, row := range catalog.Sessions {
		if row.ID == string(original.ID()) {
			selected = row
			break
		}
	}

	if selected.ID == "" {
		t.Fatal("fixture session missing from picker catalog")
	}

	if err := original.Kill(ctx); err != nil {
		t.Fatalf("kill original: %v", err)
	}

	replacement, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "picker-target"})
	if err != nil {
		t.Fatalf("create replacement: %v", err)
	}

	err = services.KillSession(ctx, selected)
	if !errors.Is(err, errPickerTargetChanged) {
		t.Fatalf("kill stale target error = %v, want target changed", err)
	}

	if _, err := replacement.Info(ctx); err != nil {
		t.Fatalf("replacement session was mutated: %v", err)
	}
}

func TestPickerSessionNamesRefreshAppendClearAndReplace(t *testing.T) {
	const nativeID = "019f6aa1-db20-75c3-bebe-1658114ddc6f"

	path := filepath.Join(t.TempDir(), "2026-07-16_"+nativeID+".jsonl")
	writePickerNameFixture(t, path, `{"type":"session_info","name":"  Initial name  "}`+"\n")
	observed := []agents.AgentSession{{ID: "registry-id", Harness: "pi", HarnessSessionID: nativeID, SessionPath: path}}

	var reader pickerTranscriptNameCache

	assertLabel := func(want string) {
		t.Helper()

		summaries := pickerAgentSummaries(observed)
		reader.annotateInventory(t.Context(), summaries)

		if got := summaries[0].Agents[0].HarnessSession; got != want {
			t.Fatalf("display label = %q, want %q", got, want)
		}

		if observed[0].HarnessSessionID != nativeID {
			t.Fatal("native resume identifier was changed")
		}
	}

	assertLabel("Initial name")
	assertLabel("Initial name")
	appendPickerNameFixture(t, path, `{"type":"session_info","name":"Renamed"}`+"\n")
	assertLabel("Renamed")
	appendPickerNameFixture(t, path, `{"type":"session_info","name":"Still writing`)
	assertLabel("Renamed")
	appendPickerNameFixture(t, path, ` name"}`)
	assertLabel("Still writing name")
	appendPickerNameFixture(t, path, "\n"+`{"type":"session_info","name":"  "}`+"\n")
	assertLabel(nativeID)

	writePickerNameFixture(t, path, `{"type":"session_info","name":"Truncated"}`+"\n")
	assertLabel("Truncated")

	replacement := path + ".replacement"
	writePickerNameFixture(t, replacement, `{"type":"session_info","name":"Replaced with a longer conversation name"}`+"\n")

	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}

	assertLabel("Replaced with a longer conversation name")
	writePickerNameFixture(t, path, "")
	assertLabel(nativeID)
}

func TestPickerHarnessSessionLabelFallbacks(t *testing.T) {
	const nativeID = "019f6aa1-db20-75c3-bebe-1658114ddc6f"

	for _, test := range []struct {
		name  string
		agent agents.AgentSession
		want  string
	}{
		{name: "native identifier", agent: agents.AgentSession{HarnessSessionID: " native "}, want: "native"},
		{name: "pi filename", agent: agents.AgentSession{Harness: "pi", SessionPath: "/state/date_" + nativeID + ".jsonl"}, want: nativeID},
		{name: "ordinary filename", agent: agents.AgentSession{Harness: "codex", SessionPath: "/state/conversation.jsonl"}, want: "conversation.jsonl"},
		{name: "invalid pi suffix", agent: agents.AgentSession{Harness: "pi", SessionPath: "/state/date_not-a-uuid.jsonl"}, want: "date_not-a-uuid.jsonl"},
		{name: "registry identifier", agent: agents.AgentSession{ID: " registry-id ", SessionPath: "/"}, want: "registry:registry-id"},
		{name: "unknown", agent: agents.AgentSession{}, want: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := pickerAgentSession(test.agent).HarnessSession; got != test.want {
				t.Fatalf("display label = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPickerSessionNamesSkipLargeEntriesAndResumeBoundedReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.jsonl")
	transcript := `{"type":"session_info","name":"Initial"}` + "\n" +
		`{"type":"message","message":{"content":"` + strings.Repeat("x", 3*pickerNameLineLimit) + `"}}` + "\n" +
		`{"type":"message","name":"Not metadata","content":"session_info"}` + "\n" +
		`{"type":"session_info","name":"Latest"}` + "\n"
	writePickerNameFixture(t, path, transcript)

	var state pickerSessionNameState
	for range 8 {
		before := state.offset

		state = readPickerSessionName(t.Context(), path, state, pickerNameLineLimit)
		if state.offset-before > pickerNameLineLimit {
			t.Fatal("transcript read exceeded its budget")
		}
	}

	if state.name != "Latest" || state.offset != int64(len(transcript)) {
		t.Fatalf("bounded reads = %q at %d, want latest name at %d", state.name, state.offset, len(transcript))
	}
}

func TestPickerSessionNamesRetainUsableNameOnMalformedOrMissingMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	writePickerNameFixture(t, path, `{"type":"session_info","name":"Known"}`+"\n")
	state := readPickerSessionName(t.Context(), path, pickerSessionNameState{}, pickerNameFileReadLimit)
	appendPickerNameFixture(t, path, `{"type":"session_info","name":`+"\n")

	state = readPickerSessionName(t.Context(), path, state, pickerNameFileReadLimit)
	if state.name != "Known" {
		t.Fatalf("malformed entry replaced name: %q", state.name)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	state = readPickerSessionName(t.Context(), path, state, pickerNameFileReadLimit)
	if state.name != "Known" {
		t.Fatalf("missing transcript replaced cached name: %q", state.name)
	}

	state = readPickerSessionName(t.Context(), filepath.Dir(path), pickerSessionNameState{}, pickerNameFileReadLimit)
	if state.hasInfo {
		t.Fatal("directory provided metadata")
	}
}

func TestPickerSessionNamesHonorCancellationBeforeAndDuringLargeReads(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	input := &pickerNameCancelReader{cancel: func() { t.Fatal("canceled decoder read transcript") }}

	state := decodePickerSessionName(ctx, input, pickerSessionNameState{}, true)
	if state.offset != 0 {
		t.Fatal("canceled decoder advanced")
	}

	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()

	input = &pickerNameCancelReader{cancel: cancel}

	state = decodePickerSessionName(ctx, input, pickerSessionNameState{}, true)
	if input.reads != 1 || state.offset != 0 {
		t.Fatalf("decoder kept reading after cancellation: reads=%d, offset=%d", input.reads, state.offset)
	}
}

func TestPickerSessionNamesEvictAbsentTranscriptsAndBoundCache(t *testing.T) {
	observed := make([]agents.AgentSession, pickerNameCacheLimit+1)
	for index := range observed {
		observed[index] = agents.AgentSession{
			Harness: "pi", SessionPath: filepath.Join(t.TempDir(), fmt.Sprintf("missing-%d.jsonl", index)),
		}
	}

	var reader pickerTranscriptNameCache
	reader.annotateInventory(t.Context(), pickerAgentSummaries(observed))

	if len(reader.cache) > pickerNameCacheLimit {
		t.Fatal("name cache exceeded its capacity")
	}

	reader.annotateInventory(t.Context(), pickerAgentSummaries(observed[1:2]))

	if len(reader.cache) != 1 {
		t.Fatalf("cache retained absent transcripts: %d entries", len(reader.cache))
	}

	reader.annotateInventory(t.Context(), nil)

	if len(reader.cache) != 0 {
		t.Fatal("empty inventory retained transcripts")
	}
}

func TestPickerSessionNamesShareReaderAcrossConcurrentRefreshes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	writePickerNameFixture(t, path, `{"type":"session_info","name":"Concurrent"}`+"\n")
	observed := []agents.AgentSession{{Harness: "pi", SessionPath: path}, {Harness: "pi", SessionPath: path}}

	var (
		reader pickerTranscriptNameCache
		group  sync.WaitGroup
	)

	for range 8 {
		group.Go(func() {
			summaries := pickerAgentSummaries(observed)
			reader.annotateInventory(t.Context(), summaries)

			for _, agent := range summaries[0].Agents {
				if agent.HarnessSession != "Concurrent" {
					t.Errorf("concurrent display label = %q", agent.HarnessSession)
				}
			}
		})
	}

	group.Wait()
}

func TestPickerSessionNamesResetSameSizeRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	writePickerNameFixture(t, path, `{"type":"session_info","name":"Before"}`+"\n")
	state := readPickerSessionName(t.Context(), path, pickerSessionNameState{}, pickerNameFileReadLimit)
	writePickerNameFixture(t, path, `{"type":"session_info","name":"After!"}`+"\n")

	changed := state.fileInfo.ModTime().Add(time.Second)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}

	state = readPickerSessionName(t.Context(), path, state, pickerNameFileReadLimit)
	if state.name != "After!" {
		t.Fatalf("same-size rewrite retained %q", state.name)
	}
}

type pickerNameCancelReader struct {
	cancel context.CancelFunc
	reads  int
}

func (r *pickerNameCancelReader) Read(buffer []byte) (int, error) {
	r.reads++
	r.cancel()

	for index := range buffer {
		buffer[index] = 'x'
	}

	return len(buffer), nil
}

func writePickerNameFixture(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendPickerNameFixture(t *testing.T, path, content string) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}

	_, writeErr := io.WriteString(file, content)

	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append transcript: write=%v close=%v", writeErr, closeErr)
	}
}
