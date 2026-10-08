//go:build linux || darwin

package picker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
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

	rootCommand.AddCommand(NewPickCommand(ctx))

	return rootCommand
}

func TestResolvePickerPresentationFollowsChoiceAndTmuxContext(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		choice string
		env    map[string]string
		want   pickerPresentation
	}{
		{name: "auto outside tmux", choice: "auto", env: nil, want: pickerFullscreen},
		{name: "empty outside tmux", choice: "", env: nil, want: pickerFullscreen},
		{name: "auto inside tmux", choice: "auto", env: map[string]string{"TMUX": "/tmp/tmux-1/default,1,0"}, want: pickerInline},
		{name: "auto inside tmux popup", choice: "auto", env: map[string]string{"TMUX": "/tmp/tmux-1/default,1,0", "TMUX_POPUP": "1"}, want: pickerPopup},
		{name: "auto ignores popup marker outside tmux", choice: "auto", env: map[string]string{"TMUX_POPUP": "1"}, want: pickerFullscreen},
		{name: "explicit fullscreen inside tmux", choice: "fullscreen", env: map[string]string{"TMUX": "/tmp/tmux-1/default,1,0"}, want: pickerFullscreen},
		{name: "explicit inline outside tmux", choice: "inline", env: nil, want: pickerInline},
		{name: "explicit popup case insensitive", choice: " Popup ", env: nil, want: pickerPopup},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolvePickerPresentation(test.choice, func(key string) string { return test.env[key] })
			if err != nil || got != test.want {
				t.Fatalf("resolvePickerPresentation(%q) = %d, %v; want %d", test.choice, got, err, test.want)
			}
		})
	}
}

func TestPickHidesPopupChildFlagsAndOmitsLauncherFlags(t *testing.T) {
	t.Parallel()

	cmd := NewPickCommand(t.Context())

	ui := cmd.Flags().Lookup("ui")
	if ui == nil || ui.Hidden || ui.DefValue != "auto" {
		t.Fatalf("--ui flag = %#v, want visible auto default", ui)
	}

	for _, name := range []string{"switch-handoff", "launched-at", "target-pane", "current-session-id"} {
		if flag := cmd.Flags().Lookup(name); flag == nil || !flag.Hidden {
			t.Fatalf("--%s flag = %#v, want hidden popup child flag", name, flag)
		}
	}

	for _, name := range []string{"tmux", "embedded", "client", "client-width", "client-height"} {
		if flag := cmd.Flags().Lookup(name); flag != nil {
			t.Fatalf("pick registers launcher flag --%s", name)
		}
	}
}

func pickPopupCommand(t *testing.T) *cobra.Command {
	t.Helper()

	cmd, _, err := newTestRootCommand(t.Context()).Find([]string{"pick", "popup"})
	if err != nil || cmd.Name() != "popup" {
		t.Fatalf("pick popup = %v, %v", cmd, err)
	}

	return cmd
}

func TestPopupChildReceivesInvokingPaneSessionAndRepository(t *testing.T) {
	t.Parallel()

	cmd := pickPopupCommand(t)

	if err := cmd.Flags().Set("repository", "/private/repository"); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Flags().Set("client", "/dev/pts/1"); err != nil {
		t.Fatal(err)
	}

	launchedAt := time.Unix(0, 1790000000123456789)

	arguments := pickerPopupArguments(cmd, "/private/tmux.sock", "$7", "%2", "/private/handoff", launchedAt)
	if !slices.Equal(arguments[:min(len(arguments), 3)], []string{"pick", "--ui", "popup"}) {
		t.Fatalf("popup child arguments = %#v", arguments)
	}

	if slices.Contains(arguments, "--client") {
		t.Fatalf("popup child arguments carry launcher arguments: %#v", arguments)
	}

	for _, required := range []string{
		"--repository", "/private/repository", "--target-pane", "%2", "--current-session-id", "$7",
		"--switch-handoff", "/private/handoff", "--launched-at", "1790000000123456789",
	} {
		if !slices.Contains(arguments, required) {
			t.Fatalf("popup child arguments lack %q: %#v", required, arguments)
		}
	}

	pick, _, err := newTestRootCommand(t.Context()).Find(arguments)
	if err != nil || pick.Name() != "pick" {
		t.Fatalf("popup child arguments resolve to %v, %v; want pick", pick, err)
	}

	if err := pick.ParseFlags(arguments[1:]); err != nil {
		t.Fatalf("pick rejects popup child arguments: %v", err)
	}
}

func TestPopupChildAcceptsOnlyRecentLaunchTimes(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{"absent", "", false},
		{"recent", strconv.FormatInt(now.Add(-300*time.Millisecond).UnixNano(), 10), true},
		{"future", strconv.FormatInt(now.Add(time.Second).UnixNano(), 10), false},
		{"stale", strconv.FormatInt(now.Add(-2*time.Minute).UnixNano(), 10), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cmd := NewPickCommand(t.Context())
			if test.value != "" {
				if err := cmd.Flags().Set("launched-at", test.value); err != nil {
					t.Fatal(err)
				}
			}

			if _, ok := pickerLaunchedAt(cmd, now); ok != test.want {
				t.Fatalf("launch time accepted = %t, want %t", ok, test.want)
			}
		})
	}
}

func TestPickerCommandsExposeLiveInteractiveSurface(t *testing.T) {
	t.Parallel()

	for _, cmd := range []*cobra.Command{NewPickCommand(t.Context()), pickPopupCommand(t)} {
		t.Run(cmd.Name(), func(t *testing.T) {
			t.Parallel()

			if cmd.Annotations[clibase.AnnotationLiveIO] != "true" {
				t.Fatalf("live I/O annotation = %q", cmd.Annotations[clibase.AnnotationLiveIO])
			}

			for _, flag := range []string{"socket", "repository", "lease-root", "history", "agent-state"} {
				if cmd.Flags().Lookup(flag) == nil {
					t.Fatalf("missing --%s", flag)
				}
			}
		})
	}
}

func TestPickerPopupUsesApplicationOwnedFrame(t *testing.T) {
	t.Parallel()

	options := pickerPopupOptions(gotmux.Exec("/bin/true"), "120", "32", "")
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

func TestPickerColorProfileInsideTmuxNeedsNoTmuxQuery(t *testing.T) {
	t.Parallel()

	inside := []string{"TMUX=/tmp/tmux-1000/default,42,0", "TERM=tmux-256color"}
	for _, test := range []struct {
		name    string
		environ []string
		want    colorprofile.Profile
		decided bool
	}{
		{"outside tmux", []string{"TERM=xterm-256color"}, colorprofile.Unknown, false},
		{"inside tmux", inside, colorprofile.TrueColor, true},
		{"inside tmux without color", append(slices.Clone(inside), "NO_COLOR=1"), colorprofile.ASCII, true},
		{"inside tmux on a dumb terminal", []string{"TMUX=/tmp/tmux-1000/default,42,0", "TERM=dumb"}, colorprofile.NoTTY, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, decided := pickerColorProfile(test.environ)
			if got != test.want || decided != test.decided {
				t.Fatalf("color profile = %v, %t; want %v, %t", got, decided, test.want, test.decided)
			}
		})
	}
}

func TestPickerPopupBackgroundStyle(t *testing.T) {
	t.Parallel()

	transparentOpts := pickerPopupOptions(gotmux.Exec("/bin/true"), "120", "32", "default")
	if transparentOpts.Style != "" {
		t.Fatalf("transparent popup style = %q, want empty", transparentOpts.Style)
	}

	customBgOpts := pickerPopupOptions(gotmux.Exec("/bin/true"), "120", "32", "#1e1e2e")
	if customBgOpts.Style != "bg=#1e1e2e" {
		t.Fatalf("custom bg popup style = %q, want bg=#1e1e2e", customBgOpts.Style)
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

	services, err := newPickerServices(ctx, resolvePickerTestWorkspace(t, cfg, app.Locations{
		Socket: server.Endpoint().SocketPath, Repository: filepath.Join(dirs, "repository"), LeaseRoot: filepath.Join(dirs, "leases"),
		History: filepath.Join(dirs, "history.json"), FinalSaveStatus: "", AgentStore: filepath.Join(dirs, "agents.json"),
	}), gotmux.Streams{}, RemoteServices{})
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

	_, err = action.Run(ctx, services.actions, action.SessionKill, action.Local(),
		action.Caller{Client: "", Pane: "", Session: "", Width: "", Height: ""},
		action.KillSession{Session: action.RefOf(selected), Force: false})
	if action.CodeOf(err) != action.CodeTargetChanged {
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
