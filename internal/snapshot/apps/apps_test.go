package apps_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/apps"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	codexHarness = "codex"
	usrBinTop    = "/usr/bin/top"
	topName      = "top"
	usrBinHtop   = "/usr/bin/htop"
	htopName     = "htop"
)

func TestPrepareRestartsShellWithoutReplayingCapturedCommand(t *testing.T) {
	t.Parallel()

	observed := launch("/bin/bash", "bash", "-c", "printf unsafe")
	prepared, reason := apps.Prepare(observed, nil)

	assertPrepared(t, prepared, reason, apps.ReasonShellRestart, "/bin/bash", []string{"bash"}, apps.RecoveryRestart)
}

func TestPrepareRestartsProcessMonitorsBareWithoutFlags(t *testing.T) {
	t.Parallel()

	for _, executable := range []string{usrBinHtop, usrBinTop, "/opt/tools/btop"} {
		t.Run(executable, func(t *testing.T) {
			t.Parallel()

			observed := launch(executable, executable)
			prepared, reason := apps.Prepare(observed, nil)

			name := prepared.Arguments[0]
			assertPrepared(t, prepared, reason, apps.ReasonMonitorRestart, executable, []string{string(name)}, apps.RecoveryRestart)
		})
	}
}

func TestPreparePreservesSafeMonitorLaunchOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		executable string
		arguments  []string
		wantArgv   []string
	}{
		{
			name:       "top with delay cmdline and user",
			executable: usrBinTop,
			arguments:  []string{topName, "-d", "1.5", "-c", "-u", "root"},
			wantArgv:   []string{topName, "-d", "1.5", "-c", "-u", "root"},
		},
		{
			name:       "top with compact delay and sort",
			executable: usrBinTop,
			arguments:  []string{topName, "-d2", "-i", "-o", "%CPU"},
			wantArgv:   []string{topName, "-d2", "-i", "-o", "%CPU"},
		},
		{
			name:       "top with toggles and sort direction",
			executable: usrBinTop,
			arguments:  []string{topName, "-H", "-1", "-S", "-s", "-o", "+%MEM"},
			wantArgv:   []string{topName, "-H", "-1", "-S", "-s", "-o", "+%MEM"},
		},
		{
			name:       "top with any user and memory scale",
			executable: usrBinTop,
			arguments:  []string{topName, "-U", "!root", "-E", "g", "-e", "m", "-w", "120"},
			wantArgv:   []string{topName, "-U", "!root", "-E", "g", "-e", "m", "-w", "120"},
		},
		{
			name:       "top with long flags",
			executable: usrBinTop,
			arguments:  []string{topName, "--cmdline-toggle", "--idle-toggle", "--delay=2", "--filter-only-euser=root", "--sort-override=-PID"},
			wantArgv:   []string{topName, "--cmdline-toggle", "--idle-toggle", "--delay=2", "--filter-only-euser=root", "--sort-override=-PID"},
		},
		{
			name:       "htop with delay filter tree and no-color",
			executable: usrBinHtop,
			arguments:  []string{htopName, "-d", "10", "-F", "worker|master", "-t", "-C"},
			wantArgv:   []string{htopName, "-d", "10", "-F", "worker|master", "-t", "-C"},
		},
		{
			name:       "htop with long flags and filter metacharacters",
			executable: usrBinHtop,
			arguments:  []string{htopName, "--delay=5", "--filter=daemon$1", "--tree=classic", "--no-color", "--readonly"},
			wantArgv:   []string{htopName, "--delay=5", "--filter=daemon$1", "--tree=classic", "--no-color", "--readonly"},
		},
		{
			name:       "btop with preset filter and update rate",
			executable: "/opt/tools/btop",
			arguments:  []string{"btop", "-p", "2", "-f", "worker&proc", "-u", "500", "--low-color"},
			wantArgv:   []string{"btop", "-p", "2", "-f", "worker&proc", "-u", "500", "--low-color"},
		},
		{
			name:       "btop with tty and force-utf",
			executable: "/opt/tools/btop",
			arguments:  []string{"btop", "--preset=0", "--update=1000", "--tty", "--force-utf"},
			wantArgv:   []string{"btop", "--preset", "0", "--update", "1000", "--tty", "--force-utf"},
		},
		{
			name:       "htop with sort key PID",
			executable: usrBinHtop,
			arguments:  []string{htopName, "-s", "PID"},
			wantArgv:   []string{htopName, "-s", "PID"},
		},
		{
			name:       "htop with sort key PERCENT_CPU and short attached",
			executable: usrBinHtop,
			arguments:  []string{htopName, "-sPERCENT_CPU"},
			wantArgv:   []string{htopName, "-s", "PERCENT_CPU"},
		},
		{
			name:       "htop with sort key Command and equals",
			executable: usrBinHtop,
			arguments:  []string{htopName, "-s=Command"},
			wantArgv:   []string{htopName, "-s", "Command"},
		},
		{
			name:       "top with procps equals syntax",
			executable: usrBinTop,
			arguments:  []string{topName, "-d=1.5", "-o=%CPU"},
			wantArgv:   []string{topName, "-d=1.5", "-o=%CPU"},
		},
		{
			name:       "btop with short equals canonicalization",
			executable: "/opt/tools/btop",
			arguments:  []string{"btop", "-p=1", "-u=500"},
			wantArgv:   []string{"btop", "-p", "1", "-u", "500"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			observed := launch(test.executable, test.arguments...)
			prepared, reason := apps.Prepare(observed, nil)

			assertPrepared(t, prepared, reason, apps.ReasonMonitorRestart, test.executable, test.wantArgv, apps.RecoveryRestart)
		})
	}
}

func TestPrepareRefusesUnsafeMonitorFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		executable string
		arguments  []string
	}{
		{name: "top pid filter", executable: usrBinTop, arguments: []string{topName, "-p", "1234"}},
		{name: "top batch mode", executable: usrBinTop, arguments: []string{topName, "-b"}},
		{name: "top iteration limit", executable: usrBinTop, arguments: []string{topName, "-n", "1"}},
		{name: "top unknown field sort", executable: usrBinTop, arguments: []string{topName, "-o", "cpu"}},
		{name: "top lowercase field sort", executable: usrBinTop, arguments: []string{topName, "-o", "%cpu"}},
		{name: "top fake macos full-command", executable: usrBinTop, arguments: []string{topName, "--full-command"}},
		{name: "top fake macos sort", executable: usrBinTop, arguments: []string{topName, "--sort", "cpu"}},
		{name: "top fake macos rows", executable: usrBinTop, arguments: []string{topName, "--rows", "20"}},
		{name: "top fake macos stats", executable: usrBinTop, arguments: []string{topName, "--stats", "pid,cpu"}},
		{name: "top conflicting user filters", executable: usrBinTop, arguments: []string{topName, "-u", "root", "-U", "daemon"}},
		{name: "top list fields", executable: usrBinTop, arguments: []string{topName, "-O"}},
		{name: "top help flag", executable: usrBinTop, arguments: []string{topName, "-h"}},
		{name: "top version flag", executable: usrBinTop, arguments: []string{topName, "-V"}},
		{name: "top negative delay", executable: usrBinTop, arguments: []string{topName, "-d", "-1"}},
		{name: "htop pid filter", executable: usrBinHtop, arguments: []string{htopName, "-p", "1234"}},
		{name: "htop iteration limit", executable: usrBinHtop, arguments: []string{htopName, "-n", "5"}},
		{name: "htop drop capabilities", executable: usrBinHtop, arguments: []string{htopName, "--drop-capabilities"}},
		{name: "htop unknown flag", executable: usrBinHtop, arguments: []string{htopName, "--custom-flag"}},
		{name: "htop noninteractive help sort-key", executable: usrBinHtop, arguments: []string{htopName, "-s", "help"}},
		{name: "htop invalid sort-key column", executable: usrBinHtop, arguments: []string{htopName, "-s", "NOT_A_FIELD"}},
		{name: "htop lowercase sort-key column", executable: usrBinHtop, arguments: []string{htopName, "-s", "pid"}},
		{name: "btop config path", executable: "/opt/tools/btop", arguments: []string{"btop", "-c", "/etc/btop.conf"}},
		{name: "btop debug mode", executable: "/opt/tools/btop", arguments: []string{"btop", "-d"}},
		{name: "btop default config", executable: "/opt/tools/btop", arguments: []string{"btop", "--default-config"}},
		{name: "btop help flag", executable: "/opt/tools/btop", arguments: []string{"btop", "--help"}},
		{name: "btop version flag", executable: "/opt/tools/btop", arguments: []string{"btop", "-V"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			observed := launch(test.executable, test.arguments...)
			prepared, reason := apps.Prepare(observed, nil)

			if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
				t.Fatalf("Prepare() accepted unsafe monitor flags: %#v, %q", prepared, reason)
			}
		})
	}
}

func TestPrepareReopensEditorFilesAsLiteralArguments(t *testing.T) {
	t.Parallel()

	observed := launch("/usr/bin/nvim", "nvim", "notes one.md", ";touch-not-a-command")
	prepared, reason := apps.Prepare(observed, nil)

	assertPrepared(t, prepared, reason, apps.ReasonEditorReopen, "/usr/bin/nvim",
		[]string{"nvim", "--", "notes one.md", ";touch-not-a-command"}, apps.RecoveryRestart)
}

func TestPrepareRestartsEditorWithoutFiles(t *testing.T) {
	t.Parallel()

	prepared, reason := apps.Prepare(launch("/usr/bin/vim", "vim"), nil)

	assertPrepared(t, prepared, reason, apps.ReasonEditorRestart, "/usr/bin/vim", []string{"vim"}, apps.RecoveryRestart)
}

func TestPrepareRejectsEditorCommandsInsteadOfInterpretingThem(t *testing.T) {
	t.Parallel()

	observed := launch("/usr/bin/vim", "vim", "-c", "echo hello", "real-file.txt")
	prepared, reason := apps.Prepare(observed, nil)

	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() = %#v, %q", prepared, reason)
	}
}

func TestPrepareUsesExactNativeAgentResumeCommandAndResolvedExecutable(t *testing.T) {
	t.Parallel()

	observed := launch("/home/user/.local/bin/codex", "codex")
	resume := &apps.AgentResume{
		Harness:     codexHarness,
		SessionID:   "019abc-session",
		SessionPath: "",
		Command:     []string{codexHarness, "resume", "019abc-session"},
		Executable:  "/home/user/.local/bin/codex",
	}

	prepared, reason := apps.Prepare(observed, resume)

	assertPrepared(t, prepared, reason, apps.ReasonAgentResume, resume.Executable, resume.Command, apps.RecoveryResume)
	resume.Command[2] = "changed-after-prepare"

	if string(prepared.Arguments[2]) != "019abc-session" {
		t.Fatal("prepared resume argv aliases caller metadata")
	}
}

func TestPrepareUsesObservedInterpreterWhenAgentLauncherIsUnavailable(t *testing.T) {
	t.Parallel()

	observed := launch("/usr/bin/node", "node", "/opt/node_modules/@openai/codex/bin/codex.js")
	resume := &apps.AgentResume{
		Harness: codexHarness, SessionID: "019abc-session",
		Command: []string{codexHarness, "resume", "019abc-session"},
	}

	prepared, reason := apps.Prepare(observed, resume)

	assertPrepared(t, prepared, reason, apps.ReasonAgentResume, "/usr/bin/node",
		[]string{"node", "/opt/node_modules/@openai/codex/bin/codex.js", "resume", "019abc-session"}, apps.RecoveryResume)
}

func TestPrepareUsesObservedDirectAgentExecutableWhenLauncherIsUnavailable(t *testing.T) {
	t.Parallel()

	observed := launch("/opt/agents/claude", "claude")
	resume := &apps.AgentResume{
		Harness: "claude", SessionID: "claude-session",
		Command: []string{"claude", "--resume", "claude-session"},
	}

	prepared, reason := apps.Prepare(observed, resume)

	assertPrepared(t, prepared, reason, apps.ReasonAgentResume, "/opt/agents/claude", resume.Command, apps.RecoveryResume)
}

func TestPrepareRejectsUnrecognizedScriptInsideHarnessNamedDirectory(t *testing.T) {
	t.Parallel()

	observed := launch("/usr/bin/node", "node", "/opt/codex/unrelated.js")
	resume := &apps.AgentResume{
		Harness: codexHarness, SessionID: "019abc-session",
		Command: []string{codexHarness, "resume", "019abc-session"},
	}

	prepared, reason := apps.Prepare(observed, resume)
	if reason != apps.ReasonAgentMetadataMismatch || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted an unrelated script: %#v, %q", prepared, reason)
	}
}

func TestPrepareAcceptsPathSpecificNativeAgentResume(t *testing.T) {
	t.Parallel()

	observed := launch("/usr/local/bin/pi", "pi")
	resume := &apps.AgentResume{
		Harness:     "pi",
		SessionID:   "",
		SessionPath: "/home/user/.pi/sessions/019abc.json",
		Command:     []string{"pi", "--session", "/home/user/.pi/sessions/019abc.json"},
	}

	prepared, reason := apps.Prepare(observed, resume)

	assertPrepared(t, prepared, reason, apps.ReasonAgentResume, "/usr/local/bin/pi", resume.Command, apps.RecoveryResume)
}

func TestPrepareRejectsGenericPromptAndMismatchedAgentResumeCommands(t *testing.T) {
	t.Parallel()

	observed := launch("/opt/agents/claude", "claude")

	tests := []struct {
		name   string
		resume *apps.AgentResume
	}{
		{
			name: "prompt replay",
			resume: &apps.AgentResume{
				Harness: "claude", SessionID: "claude-session",
				Command: []string{"claude", "-p", "fix the broken tests"},
			},
		},
		{
			name: "latest session",
			resume: &apps.AgentResume{
				Harness: "claude", SessionID: "claude-session",
				Command: []string{"claude", "--last"},
			},
		},
		{
			name: "wrong harness",
			resume: &apps.AgentResume{
				Harness: "codex", SessionID: "claude-session",
				Command: []string{"codex", "resume", "claude-session"},
			},
		},
		{
			name: "unresolved executable",
			resume: &apps.AgentResume{
				Harness: "claude", SessionID: "claude-session",
				Command:    []string{"claude", "--resume", "claude-session"},
				Executable: "relative/claude",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			prepared, reason := apps.Prepare(observed, test.resume)
			if reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
				t.Fatalf("Prepare() authorized unsafe metadata: %#v, %q", prepared, reason)
			}
		})
	}
}

func TestPrepareRejectsStaleAgentMetadataForDifferentObservedApplication(t *testing.T) {
	t.Parallel()

	observed := launch("/bin/bash", "bash")
	resume := &apps.AgentResume{
		Harness: "claude", SessionID: "claude-session",
		Command: []string{"claude", "--resume", "claude-session"},
	}

	prepared, reason := apps.Prepare(observed, resume)
	if reason != apps.ReasonAgentMetadataMismatch || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted stale pane metadata: %#v, %q", prepared, reason)
	}
}

func TestPrepareRestartsSleepingTaskPlaceholderEvenWithStaleAgentMetadata(t *testing.T) {
	t.Parallel()

	observed := launch("/home/user/.local/bin/sesh", "/home/user/.local/bin/sesh", "task", "placeholder", "t12")
	stale := &apps.AgentResume{
		Harness: "pi", SessionID: "pi-session", SessionPath: "", Executable: "",
		Command: []string{"pi", "--session", "pi-session"},
	}

	for _, agent := range []*apps.AgentResume{nil, stale} {
		prepared, reason := apps.Prepare(observed, agent)

		assertPrepared(t, prepared, reason, apps.ReasonTaskPlaceholder, "/home/user/.local/bin/sesh",
			[]string{"sesh", "task", "placeholder", "t12"}, apps.RecoveryRestart)
	}
}

func TestPrepareRefusesOtherSeshCommands(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"sesh", "task", "placeholder", "t12", "--socket", "/tmp/other"},
		{"sesh", "task", "placeholder", "../t12"},
		{"sesh", "task", "new"},
		{"sesh", "pick"},
	} {
		prepared, reason := apps.Prepare(launch("/usr/bin/sesh", arguments...), nil)
		if reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
			t.Fatalf("Prepare(%q) = %#v, %q; want an unverified launch", arguments, prepared, reason)
		}
	}
}

func TestPrepareUnsupportedLaunchIsIsolatedAndUnverified(t *testing.T) {
	t.Parallel()

	observed := launch("/usr/bin/python3", "python3", "script.py")
	prepared, reason := apps.Prepare(observed, nil)

	if reason != apps.ReasonUnsupportedApplication || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() = %#v, %q", prepared, reason)
	}

	prepared.Arguments[1][0] = 'X'
	if string(observed.Arguments[1]) != "script.py" {
		t.Fatal("unsupported result aliases observed launch")
	}
}

func TestPrepareReopensNanoFilesAsLiteralArguments(t *testing.T) {
	t.Parallel()

	observed := launch("/usr/bin/nano", "nano", "notes one.md", ";touch-not-a-command")
	prepared, reason := apps.Prepare(observed, nil)

	assertPrepared(t, prepared, reason, apps.ReasonEditorReopen, "/usr/bin/nano",
		[]string{"nano", "--", "notes one.md", ";touch-not-a-command"}, apps.RecoveryRestart)
}

func TestPrepareRestartsNanoWithoutFiles(t *testing.T) {
	t.Parallel()

	prepared, reason := apps.Prepare(launch("/usr/bin/nano", "nano"), nil)

	assertPrepared(t, prepared, reason, apps.ReasonEditorRestart, "/usr/bin/nano", []string{"nano"}, apps.RecoveryRestart)
}

func TestPrepareRestartsLazygitWithSafeFlagsAndRefusesUnsafe(t *testing.T) {
	t.Parallel()

	bare := launch("/usr/bin/lazygit", "lazygit")
	prepared, reason := apps.Prepare(bare, nil)
	assertPrepared(t, prepared, reason, apps.ReasonGitTUIRestart, "/usr/bin/lazygit", []string{"lazygit"}, apps.RecoveryRestart)

	withPath := launch("/usr/bin/lazygit", "lazygit", "-p", "/work/project", "-f", "src")
	prepared, reason = apps.Prepare(withPath, nil)
	assertPrepared(t, prepared, reason, apps.ReasonGitTUIRestart, "/usr/bin/lazygit",
		[]string{"lazygit", "-p", "/work/project", "-f", "src"}, apps.RecoveryRestart)

	panel := launch("/usr/bin/lazygit", "lazygit", "status")
	prepared, reason = apps.Prepare(panel, nil)
	assertPrepared(t, prepared, reason, apps.ReasonGitTUIRestart, "/usr/bin/lazygit",
		[]string{"lazygit", "status"}, apps.RecoveryRestart)

	unsafe := launch("/usr/bin/lazygit", "lazygit", "--profile")

	prepared, reason = apps.Prepare(unsafe, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted unsafe lazygit flag: %#v, %q", prepared, reason)
	}
}

func TestPrepareRestartsTigWithSafeSubviewsAndRefusesUnsafe(t *testing.T) {
	t.Parallel()

	bare := launch("/usr/bin/tig", "tig")
	prepared, reason := apps.Prepare(bare, nil)
	assertPrepared(t, prepared, reason, apps.ReasonGitTUIRestart, "/usr/bin/tig", []string{"tig"}, apps.RecoveryRestart)

	status := launch("/usr/bin/tig", "tig", "status")
	prepared, reason = apps.Prepare(status, nil)
	assertPrepared(t, prepared, reason, apps.ReasonGitTUIRestart, "/usr/bin/tig", []string{"tig", "status"}, apps.RecoveryRestart)

	blame := launch("/usr/bin/tig", "tig", "blame", "main.go")
	prepared, reason = apps.Prepare(blame, nil)
	assertPrepared(t, prepared, reason, apps.ReasonGitTUIRestart, "/usr/bin/tig", []string{"tig", "blame", "main.go"}, apps.RecoveryRestart)

	allLog := launch("/usr/bin/tig", "tig", "--all", "log")
	prepared, reason = apps.Prepare(allLog, nil)
	assertPrepared(t, prepared, reason, apps.ReasonGitTUIRestart, "/usr/bin/tig", []string{"tig", "--all", "log"}, apps.RecoveryRestart)

	unsafe := launch("/usr/bin/tig", "tig", "-C", "/work/other")

	prepared, reason = apps.Prepare(unsafe, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted unsafe tig flags: %#v, %q", prepared, reason)
	}
}

func TestPrepareRestartsYaziWithSafeDirectoryAndRefusesUnsafe(t *testing.T) {
	t.Parallel()

	bare := launch("/usr/bin/yazi", "yazi")
	prepared, reason := apps.Prepare(bare, nil)
	assertPrepared(t, prepared, reason, apps.ReasonFileManagerRestart, "/usr/bin/yazi", []string{"yazi"}, apps.RecoveryRestart)

	withDir := launch("/usr/bin/yazi", "yazi", "/work/project")
	prepared, reason = apps.Prepare(withDir, nil)
	assertPrepared(t, prepared, reason, apps.ReasonFileManagerRestart, "/usr/bin/yazi",
		[]string{"yazi", "--", "/work/project"}, apps.RecoveryRestart)

	withExplicitSeparator := launch("/usr/bin/yazi", "yazi", "--", "/work/dir1", "/work/dir2")
	prepared, reason = apps.Prepare(withExplicitSeparator, nil)
	assertPrepared(t, prepared, reason, apps.ReasonFileManagerRestart, "/usr/bin/yazi",
		[]string{"yazi", "--", "/work/dir1", "/work/dir2"}, apps.RecoveryRestart)

	unsafeSubcommand := launch("/usr/bin/yazi", "yazi", "select")

	prepared, reason = apps.Prepare(unsafeSubcommand, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted unsafe yazi subcommand: %#v, %q", prepared, reason)
	}
}

func TestPrepareRestartsRangerWithSafeDirectoryAndRefusesUnsafe(t *testing.T) {
	t.Parallel()

	bare := launch("/usr/bin/ranger", "ranger")
	prepared, reason := apps.Prepare(bare, nil)
	assertPrepared(t, prepared, reason, apps.ReasonFileManagerRestart, "/usr/bin/ranger", []string{"ranger"}, apps.RecoveryRestart)

	withDir := launch("/usr/bin/ranger", "ranger", "/work/project")
	prepared, reason = apps.Prepare(withDir, nil)
	assertPrepared(t, prepared, reason, apps.ReasonFileManagerRestart, "/usr/bin/ranger",
		[]string{"ranger", "--", "/work/project"}, apps.RecoveryRestart)

	unsafeCmd := launch("/usr/bin/ranger", "ranger", "--cmd=delete")

	prepared, reason = apps.Prepare(unsafeCmd, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted unsafe ranger command: %#v, %q", prepared, reason)
	}
}

func TestPrepareRestartsLessWithSafeFilesAndFlags(t *testing.T) {
	t.Parallel()

	withFiles := launch("/usr/bin/less", "less", "-R", "-N", "README.md")
	prepared, reason := apps.Prepare(withFiles, nil)
	assertPrepared(t, prepared, reason, apps.ReasonPagerRestart, "/usr/bin/less",
		[]string{"less", "-R", "-N", "--", "README.md"}, apps.RecoveryRestart)

	withCombinedFlags := launch("/usr/bin/less", "less", "-RNSFXi", "--", "notes.txt")
	prepared, reason = apps.Prepare(withCombinedFlags, nil)
	assertPrepared(t, prepared, reason, apps.ReasonPagerRestart, "/usr/bin/less",
		[]string{"less", "-RNSFXi", "--", "notes.txt"}, apps.RecoveryRestart)
}

func TestPrepareRefusesLessWithoutFilesOrUnsafeCommands(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		arguments []string
	}{
		{name: "bare less without files", arguments: []string{"less"}},
		{name: "plus-command flag", arguments: []string{"less", "+F", "log.txt"}},
		{name: "log output flag", arguments: []string{"less", "-o", "output.txt", "input.txt"}},
		{name: "hyphen stdin pipe", arguments: []string{"less", "-"}},
		{name: "hyphen stdin pipe after separator", arguments: []string{"less", "--", "-"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			observed := launch("/usr/bin/less", tc.arguments...)
			prepared, reason := apps.Prepare(observed, nil)

			if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
				t.Fatalf("Prepare() accepted %s: %#v, %q", tc.name, prepared, reason)
			}
		})
	}
}

func TestPrepareRestartsManWithSafePage(t *testing.T) {
	t.Parallel()

	singlePage := launch("/usr/bin/man", "man", "tmux")
	prepared, reason := apps.Prepare(singlePage, nil)
	assertPrepared(t, prepared, reason, apps.ReasonPagerRestart, "/usr/bin/man",
		[]string{"man", "tmux"}, apps.RecoveryRestart)

	sectionPage := launch("/usr/bin/man", "man", "2", "read")
	prepared, reason = apps.Prepare(sectionPage, nil)
	assertPrepared(t, prepared, reason, apps.ReasonPagerRestart, "/usr/bin/man",
		[]string{"man", "2", "read"}, apps.RecoveryRestart)
}

func TestPrepareRefusesManWithoutPageOrWithUnsafeFlags(t *testing.T) {
	t.Parallel()

	bare := launch("/usr/bin/man", "man")

	prepared, reason := apps.Prepare(bare, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted bare man: %#v, %q", prepared, reason)
	}

	customPager := launch("/usr/bin/man", "man", "-P", "cat", "tmux")

	prepared, reason = apps.Prepare(customPager, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted custom pager in man: %#v, %q", prepared, reason)
	}
}

func TestPreparePreservesSafePathsWithMetacharacters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		executable string
		arguments  []string
		wantArgv   []string
		wantReason apps.Reason
	}{
		{
			name:       "less with semicolon and dollar sign",
			executable: "/usr/bin/less",
			arguments:  []string{"less", "file;name.txt", "doc$1.md"},
			wantArgv:   []string{"less", "--", "file;name.txt", "doc$1.md"},
			wantReason: apps.ReasonPagerRestart,
		},
		{
			name:       "less with space and ampersand",
			executable: "/usr/bin/less",
			arguments:  []string{"less", "-R", "report & log.txt"},
			wantArgv:   []string{"less", "-R", "--", "report & log.txt"},
			wantReason: apps.ReasonPagerRestart,
		},
		{
			name:       "lazygit with path containing semicolon",
			executable: "/usr/bin/lazygit",
			arguments:  []string{"lazygit", "-p", "/dir;project"},
			wantArgv:   []string{"lazygit", "-p", "/dir;project"},
			wantReason: apps.ReasonGitTUIRestart,
		},
		{
			name:       "yazi with directory containing dollar and ampersand",
			executable: "/usr/bin/yazi",
			arguments:  []string{"yazi", "dir$one", "file&two"},
			wantArgv:   []string{"yazi", "--", "dir$one", "file&two"},
			wantReason: apps.ReasonFileManagerRestart,
		},
		{
			name:       "nvim with semicolon and dollar in filenames",
			executable: "/usr/bin/nvim",
			arguments:  []string{"nvim", "file;name.txt", "data$log.csv"},
			wantArgv:   []string{"nvim", "--", "file;name.txt", "data$log.csv"},
			wantReason: apps.ReasonEditorReopen,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			observed := launch(test.executable, test.arguments...)
			prepared, reason := apps.Prepare(observed, nil)

			assertPrepared(t, prepared, reason, test.wantReason, test.executable, test.wantArgv, apps.RecoveryRestart)
		})
	}
}

func TestPrepareAdapterInputBoundaries(t *testing.T) {
	t.Parallel()

	adapters := []struct {
		executable string
		name       string
		bareReason apps.Reason
		allowBare  bool
	}{
		{executable: usrBinTop, name: topName, bareReason: apps.ReasonMonitorRestart, allowBare: true},
		{executable: "/opt/tools/btop", name: "btop", bareReason: apps.ReasonMonitorRestart, allowBare: true},
		{executable: usrBinHtop, name: htopName, bareReason: apps.ReasonMonitorRestart, allowBare: true},
		{executable: "/usr/bin/man", name: "man", bareReason: apps.ReasonUnsafeArguments, allowBare: false},
		{executable: "/usr/bin/lazygit", name: "lazygit", bareReason: apps.ReasonGitTUIRestart, allowBare: true},
		{executable: "/usr/bin/tig", name: "tig", bareReason: apps.ReasonGitTUIRestart, allowBare: true},
		{executable: "/usr/bin/yazi", name: "yazi", bareReason: apps.ReasonFileManagerRestart, allowBare: true},
		{executable: "/usr/bin/ranger", name: "ranger", bareReason: apps.ReasonFileManagerRestart, allowBare: true},
		{executable: "/usr/bin/nvim", name: "nvim", bareReason: apps.ReasonEditorRestart, allowBare: true},
		{executable: "/usr/bin/nano", name: "nano", bareReason: apps.ReasonEditorRestart, allowBare: true},
	}

	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			t.Parallel()

			testAdapterNil(t, a.executable)
			testAdapterEmpty(t, a.executable)
			testAdapterArgv0(t, a.executable, a.name, a.allowBare, a.bareReason)
			testAdapterOversized(t, a.executable, a.name)
		})
	}
}

func testAdapterNil(t *testing.T, executable string) {
	t.Helper()

	observed := model.Launch{
		Executable:  model.Bytes(executable),
		Arguments:   nil,
		Directory:   model.Bytes("/work/project"),
		Environment: nil,
		Birth:       model.Bytes("123:456"),
		Recovery:    apps.RecoveryUnverified,
		Reason:      "",
	}

	prepared, reason := apps.Prepare(observed, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("nil arguments: got %q, want ReasonUnsafeArguments", reason)
	}
}

func testAdapterEmpty(t *testing.T, executable string) {
	t.Helper()

	observed := model.Launch{
		Executable:  model.Bytes(executable),
		Arguments:   []model.Bytes{},
		Directory:   model.Bytes("/work/project"),
		Environment: nil,
		Birth:       model.Bytes("123:456"),
		Recovery:    apps.RecoveryUnverified,
		Reason:      "",
	}

	prepared, reason := apps.Prepare(observed, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("empty arguments: got %q, want ReasonUnsafeArguments", reason)
	}
}

func testAdapterArgv0(t *testing.T, executable, name string, allowBare bool, bareReason apps.Reason) {
	t.Helper()

	observed := launch(executable, name)
	prepared, reason := apps.Prepare(observed, nil)

	if allowBare {
		assertPrepared(t, prepared, reason, bareReason, executable, []string{name}, apps.RecoveryRestart)
	} else if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("argv0-only: got %q, want ReasonUnsafeArguments", reason)
	}
}

func testAdapterOversized(t *testing.T, executable, name string) {
	t.Helper()

	manyArgs := make([]string, 0, 40)
	manyArgs = append(manyArgs, name)

	for range 35 {
		manyArgs = append(manyArgs, "arg")
	}

	observedCount := launch(executable, manyArgs...)

	prepared, reason := apps.Prepare(observedCount, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("oversized args: got %q, want ReasonUnsafeArguments", reason)
	}

	longArg := strings.Repeat("a", 5000)
	observedBytes := launch(executable, name, longArg)

	prepared, reason = apps.Prepare(observedBytes, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("oversized byte length: got %q, want ReasonUnsafeArguments", reason)
	}
}

func TestPrepareRefusesOpenClawDueToMissingExactSessionShape(t *testing.T) {
	t.Parallel()

	observed := launch("/usr/bin/node", "node", "/opt/openclaw/index.js")
	resume := &apps.AgentResume{
		Harness:     "openclaw",
		SessionID:   "session-1",
		SessionPath: "",
		Command:     []string{"openclaw", "resume", "session-1"},
		Executable:  "/home/user/.local/bin/openclaw",
	}

	prepared, reason := apps.Prepare(observed, resume)
	if reason != apps.ReasonAgentMetadataMismatch || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() authorized unsupported OpenClaw: %#v, %q", prepared, reason)
	}
}

func TestPrepareRefusesArgumentsExceedingBounds(t *testing.T) {
	t.Parallel()

	manyArgs := make([]string, 0, 40)
	manyArgs = append(manyArgs, topName)

	for range 35 {
		manyArgs = append(manyArgs, "-c")
	}

	observed := launch(usrBinTop, manyArgs...)

	prepared, reason := apps.Prepare(observed, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted excessive argument count: %#v, %q", prepared, reason)
	}

	nulArg := launch(usrBinTop, topName, "-c\x00extra")

	prepared, reason = apps.Prepare(nulArg, nil)
	if reason != apps.ReasonUnsafeArguments || reason.Supported() || prepared.Recovery != apps.RecoveryUnverified {
		t.Fatalf("Prepare() accepted NUL byte in argument: %#v, %q", prepared, reason)
	}
}

func TestPrepareRefusesMissingOrInvalidAgentMetadata(t *testing.T) {
	t.Parallel()

	observed := launch("/opt/agents/claude", "claude")

	prepared, reason := apps.Prepare(observed, &apps.AgentResume{
		Harness: "", Command: []string{"claude", "--resume", "s1"},
	})
	if reason != apps.ReasonAgentMetadataMissing || reason.Supported() {
		t.Fatalf("missing harness: %#v, %q", prepared, reason)
	}

	prepared, reason = apps.Prepare(observed, &apps.AgentResume{
		Harness: "claude", Command: nil, SessionID: "s1",
	})
	if reason != apps.ReasonAgentMetadataMissing || reason.Supported() {
		t.Fatalf("empty command: %#v, %q", prepared, reason)
	}

	prepared, reason = apps.Prepare(observed, &apps.AgentResume{
		Harness: "claude", Command: []string{"claude\x00", "--resume", "s1"}, SessionID: "s1",
	})
	if reason != apps.ReasonAgentExecutableInvalid || reason.Supported() {
		t.Fatalf("command with NUL: %#v, %q", prepared, reason)
	}

	prepared, reason = apps.Prepare(observed, &apps.AgentResume{
		Harness: "claude", Command: []string{"claude", "--resume", "s1"}, SessionID: "s1", Executable: "/opt/agents/claude\x00",
	})
	if reason != apps.ReasonAgentExecutableInvalid || reason.Supported() {
		t.Fatalf("executable with NUL: %#v, %q", prepared, reason)
	}
}

func launch(executable string, arguments ...string) model.Launch {
	argv := make([]model.Bytes, len(arguments))
	for index, argument := range arguments {
		argv[index] = model.Bytes(argument)
	}

	return model.Launch{
		Executable:  model.Bytes(executable),
		Arguments:   argv,
		Directory:   model.Bytes("/work/project"),
		Environment: []model.Variable{{Name: model.Bytes("TERM"), Value: model.Bytes("xterm-256color"), State: "set"}},
		Birth:       model.Bytes("123:456"),
		Recovery:    apps.RecoveryUnverified,
		Reason:      "",
	}
}

func assertPrepared(
	t *testing.T,
	prepared model.Launch,
	gotReason apps.Reason,
	wantReason apps.Reason,
	wantExecutable string,
	wantArguments []string,
	wantRecovery model.Recovery,
) {
	t.Helper()

	gotArguments := make([]string, len(prepared.Arguments))
	for index, argument := range prepared.Arguments {
		gotArguments[index] = string(argument)
	}

	if gotReason != wantReason || !gotReason.Supported() || string(prepared.Executable) != wantExecutable ||
		!slices.Equal(gotArguments, wantArguments) || prepared.Recovery != wantRecovery {
		t.Fatalf("prepared = %#v, reason = %q; want executable %q, argv %#v, recovery %q, reason %q",
			prepared, gotReason, wantExecutable, wantArguments, wantRecovery, wantReason)
	}

	if string(prepared.Directory) != "/work/project" || string(prepared.Birth) != "123:456" {
		t.Fatalf("prepared directory/birth = %q/%q", prepared.Directory, prepared.Birth)
	}
}
