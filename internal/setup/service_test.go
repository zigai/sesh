package setup_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/setup"
)

func linuxInputs(t *testing.T) setup.ServiceInputs {
	t.Helper()

	return setup.ServiceInputs{
		Program:           "/usr/local/bin/sesh",
		Tmux:              "/usr/bin/tmux",
		SocketPath:        "/run/user/1000/sesh/tmux.sock",
		RepositoryPath:    "/home/user/.local/share/sesh/snapshots",
		LeaseRootPath:     "/home/user/.local/share/sesh/leases",
		HistoryPath:       "/home/user/.local/state/sesh/history.json",
		StatusPath:        "/home/user/.local/state/sesh/autosave-status.json",
		Interval:          3 * time.Minute,
		FinalSaveDeadline: 20 * time.Second,
		Platform:          setup.PlatformLinux,
	}
}

func TestDefinitionsRequireResolvedInputs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*setup.ServiceInputs)
	}{
		{name: "missing program", mutate: func(inputs *setup.ServiceInputs) { inputs.Program = "" }},
		{name: "relative program", mutate: func(inputs *setup.ServiceInputs) { inputs.Program = "sesh" }},
		{name: "missing socket", mutate: func(inputs *setup.ServiceInputs) { inputs.SocketPath = "" }},
		{name: "missing repository", mutate: func(inputs *setup.ServiceInputs) { inputs.RepositoryPath = "" }},
		{name: "missing lease root", mutate: func(inputs *setup.ServiceInputs) { inputs.LeaseRootPath = "" }},
		{name: "missing history", mutate: func(inputs *setup.ServiceInputs) { inputs.HistoryPath = "" }},
		{name: "zero interval", mutate: func(inputs *setup.ServiceInputs) { inputs.Interval = 0 }},
		{name: "zero deadline", mutate: func(inputs *setup.ServiceInputs) { inputs.FinalSaveDeadline = 0 }},
		{
			name: "tmux service without tmux",
			mutate: func(inputs *setup.ServiceInputs) {
				inputs.WithTmuxService = true
				inputs.Tmux = ""
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			inputs := linuxInputs(t)
			testCase.mutate(&inputs)

			if _, err := setup.Definitions(inputs); err == nil {
				t.Fatal("Definitions accepted incomplete inputs")
			}
		})
	}
}

func TestSystemdDefinitionsRenderOwnedUnits(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	if len(definitions) != 2 {
		t.Fatalf("definitions = %d, want the autosave service and timer", len(definitions))
	}

	byName := map[string]setup.ServiceDefinition{}
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}

	service, ok := byName[setup.AutosaveServiceName]
	if !ok {
		t.Fatalf("missing %s", setup.AutosaveServiceName)
	}

	if !strings.HasPrefix(service.Content, "# "+setup.ManagedMarker) {
		t.Fatalf("autosave service is missing the ownership marker:\n%s", service.Content)
	}

	if !strings.Contains(service.Content, "ExecStart=/usr/local/bin/sesh autosave save-if-due --socket /run/user/1000/sesh/tmux.sock") {
		t.Fatalf("autosave service ExecStart is not the native command:\n%s", service.Content)
	}

	timer, ok := byName[setup.AutosaveTimerName]
	if !ok {
		t.Fatalf("missing %s", setup.AutosaveTimerName)
	}

	if !strings.Contains(timer.Content, "OnUnitActiveSec=3min") {
		t.Fatalf("timer does not use the configured interval:\n%s", timer.Content)
	}

	if !strings.Contains(timer.Content, "Unit="+setup.AutosaveServiceName) {
		t.Fatalf("timer does not reference the autosave service:\n%s", timer.Content)
	}
}

func TestSystemdUnitsForUserTmuxServerDoNotReferenceManagedServer(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)
	inputs.SocketPath = "/tmp/tmux-1000/default"
	inputs.WithCoordinator = true

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	remote, err := setup.RemoteMonitorDefinition(inputs, true)
	if err != nil {
		t.Fatalf("RemoteMonitorDefinition failed: %v", err)
	}

	for _, definition := range append(definitions, remote) {
		if strings.Contains(definition.Content, setup.TmuxServiceName) {
			t.Fatalf("%s references the uninstalled managed tmux server:\n%s", definition.Name, definition.Content)
		}
	}

	timer := findServiceDefinition(t, definitions, setup.AutosaveTimerName)
	if strings.Contains(timer.Content, "managed tmux server") {
		t.Fatalf("timer describes a managed tmux server that is not installed:\n%s", timer.Content)
	}
}

func TestSystemdUnitsForManagedTmuxServerFollowIt(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)
	inputs.WithTmuxService = true
	inputs.WithCoordinator = true

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	service := findServiceDefinition(t, definitions, setup.AutosaveServiceName)
	if !strings.Contains(service.Content, "After="+setup.TmuxServiceName+"\n") || !strings.Contains(service.Content, "PartOf="+setup.TmuxServiceName+"\n") {
		t.Fatalf("autosave service is not bound to the managed tmux server:\n%s", service.Content)
	}

	timer := findServiceDefinition(t, definitions, setup.AutosaveTimerName)
	if !strings.Contains(timer.Content, "PartOf="+setup.TmuxServiceName+"\n") {
		t.Fatalf("autosave timer is not bound to the managed tmux server:\n%s", timer.Content)
	}

	coordinator := findServiceDefinition(t, definitions, setup.CoordinatorServiceName)
	if !strings.Contains(coordinator.Content, "After="+setup.TmuxServiceName+"\n") {
		t.Fatalf("coordinator is not ordered after the managed tmux server:\n%s", coordinator.Content)
	}
}

func TestSystemdTmuxServiceRunsBoundedFinalSave(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)
	inputs.WithTmuxService = true
	inputs.RequiresTmuxForeground = true
	inputs.TmuxForegroundVerified = true

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	tmux := findServiceDefinition(t, definitions, setup.TmuxServiceName)
	if len(tmux.Requires) != 0 {
		t.Fatalf("managed tmux service requires = %v, want no unmet prerequisites when foreground is verified", tmux.Requires)
	}

	assertTmuxServiceContent(t, tmux.Content)
	assertTmuxServiceApply(t, definitions)
}

func TestSystemdRestoreServiceRunsStartupRestoreAfterTmuxWithoutBlockingIt(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)
	inputs.WithTmuxService = true
	inputs.RequiresTmuxForeground = true
	inputs.TmuxForegroundVerified = true

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	restore := findServiceDefinition(t, definitions, setup.RestoreServiceName)

	for _, want := range []string{
		"After=" + setup.TmuxServiceName + "\n",
		"Type=oneshot\n",
		"ExecStart=/usr/local/bin/sesh autosave restore-on-start --socket /run/user/1000/sesh/tmux.sock",
		"TimeoutStartSec=10min\n",
	} {
		if !strings.Contains(restore.Content, want) {
			t.Fatalf("restore service is missing %q:\n%s", want, restore.Content)
		}
	}

	tmux := findServiceDefinition(t, definitions, setup.TmuxServiceName)
	if strings.Contains(tmux.Content, "autosave-restore-on-start") {
		t.Fatalf("the tmux service must not run the restore itself, it would delay and time out the server start:\n%s", tmux.Content)
	}
}

func TestSystemdUnitsForUserTmuxServerRenderNoRestoreService(t *testing.T) {
	t.Parallel()

	definitions, err := setup.Definitions(linuxInputs(t))
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	for _, definition := range definitions {
		if definition.Name == setup.RestoreServiceName {
			t.Fatalf("a user-run tmux server has no managed start to restore after:\n%s", definition.Content)
		}
	}
}

func findServiceDefinition(t *testing.T, definitions []setup.ServiceDefinition, name string) setup.ServiceDefinition {
	t.Helper()

	for _, definition := range definitions {
		if definition.Name == name {
			return definition
		}
	}

	t.Fatalf("missing %s in %v", name, definitions)

	return setup.ServiceDefinition{}
}

func assertTmuxServiceContent(t *testing.T, content string) {
	t.Helper()

	for _, want := range []string{
		"ExecStart=/usr/local/bin/sesh daemon tmux --socket /run/user/1000/sesh/tmux.sock --tmux /usr/bin/tmux --stop-grace 5s",
		"ExecStartPost=/usr/local/bin/sesh daemon wait-ready --socket /run/user/1000/sesh/tmux.sock --timeout 5s",
		"autosave final-save",
		"--deadline 20s",
		"--status-file /home/user/.local/state/sesh/autosave-status.json",
		"TimeoutStopSec=25s",
		"OOMPolicy=continue\n",
		"SuccessExitStatus=130\n",
		"Wants=" + setup.RestoreServiceName + "\n",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("tmux service is missing %q:\n%s", want, content)
		}
	}

	if strings.Contains(content, "kill-server") {
		t.Fatalf("rendered tmux service must not request tmux destruction:\n%s", content)
	}
}

func assertTmuxServiceApply(t *testing.T, definitions []setup.ServiceDefinition) {
	t.Helper()

	root := t.TempDir()
	relocated := setup.Relocate(definitions, root)

	plans, err := setup.PlanDefinitions(relocated)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	for _, plan := range plans {
		if plan.Name == setup.TmuxServiceName && (!plan.Changed || len(plan.Requires) > 0) {
			t.Fatalf("tmux service plan = %+v, want changed and unblocked", plan)
		}
	}

	result, err := setup.ApplyDefinitions(relocated, plans, false)
	if err != nil {
		t.Fatalf("ApplyDefinitions failed: %v", err)
	}

	if result.Written != 4 {
		t.Fatalf("written = %d, want 4 definitions including the tmux and restore services", result.Written)
	}

	data, err := os.ReadFile(filepath.Join(root, setup.TmuxServiceName))
	if err != nil {
		t.Fatalf("read written tmux service: %v", err)
	}

	if !strings.Contains(string(data), setup.ManagedMarker) {
		t.Fatalf("written tmux service missing managed marker:\n%s", data)
	}
}

func TestUnverifiedForegroundSupportBlocksTmuxService(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)
	inputs.WithTmuxService = true
	inputs.RequiresTmuxForeground = true

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	var tmux setup.ServiceDefinition

	for _, definition := range definitions {
		if definition.Name == setup.TmuxServiceName {
			tmux = definition
		}
	}

	if len(tmux.Requires) == 0 {
		t.Fatal("unverified foreground support must be reported as an unmet prerequisite")
	}

	root := t.TempDir()
	definitions = setup.Relocate(definitions, root)

	plans, err := setup.PlanDefinitions(definitions)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	assertPlanUnchanged(t, plans, setup.TmuxServiceName)

	result, err := setup.ApplyDefinitions(definitions, plans, true)
	if err != nil {
		t.Fatalf("ApplyDefinitions failed: %v", err)
	}

	if result.Written != 2 {
		t.Fatalf("wrote %d files, want the two unblocked definitions", result.Written)
	}

	if !slices.Equal(result.BlockedNames, []string{setup.TmuxServiceName, setup.RestoreServiceName}) {
		t.Fatalf("blocked = %v, want the tmux service and the restore service that follows it", result.BlockedNames)
	}

	if _, err := os.Stat(filepath.Join(root, setup.TmuxServiceName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked definition was written: %v", err)
	}
}

func TestSystemdEscapeQuotesUnsafeArguments(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)
	inputs.SocketPath = "/run/user/1000/space dir/tmux.sock"
	inputs.HistoryPath = "/home/user/100%/history.json"

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	service := definitions[0].Content
	if !strings.Contains(service, `--socket "/run/user/1000/space dir/tmux.sock"`) {
		t.Fatalf("unsafe path was not quoted:\n%s", service)
	}

	if !strings.Contains(service, "100%%") {
		t.Fatalf("percent specifiers were not escaped:\n%s", service)
	}
}

func TestLaunchdDefinitionsRenderOwnedAgents(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)
	inputs.Platform = setup.PlatformDarwin
	inputs.SocketPath = "/tmp/sesh/tmux.sock"

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	if len(definitions) != 1 {
		t.Fatalf("definitions = %d, want the autosave agent", len(definitions))
	}

	agent := definitions[0]

	if agent.Kind != setup.KindLaunchdAgent || agent.Name != setup.AutosaveAgentName {
		t.Fatalf("definition = %+v, want the launchd autosave agent", agent)
	}

	if !strings.Contains(agent.Content, "<!-- "+setup.ManagedMarker+" -->") {
		t.Fatalf("agent is missing the ownership marker:\n%s", agent.Content)
	}

	if !strings.Contains(agent.Content, "<key>StartInterval</key>") || !strings.Contains(agent.Content, "<integer>180</integer>") {
		t.Fatalf("agent does not use the configured interval:\n%s", agent.Content)
	}

	if len(agent.Requires) == 0 {
		t.Fatal("launchd rendering must record that logout and shutdown ordering is not established")
	}
}

func TestRemoteMonitorDefinitionIsGatedOnTheEntryPoint(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)

	blocked, err := setup.RemoteMonitorDefinition(inputs, false)
	if err != nil {
		t.Fatalf("RemoteMonitorDefinition failed: %v", err)
	}

	if len(blocked.Requires) != 1 {
		t.Fatalf("requires = %v, want the missing entry point", blocked.Requires)
	}

	if !strings.Contains(blocked.Content, "ExecStart=/usr/local/bin/sesh daemon remote-monitor --socket /run/user/1000/sesh/tmux.sock") {
		t.Fatalf("remote monitor ExecStart is not the native command:\n%s", blocked.Content)
	}

	available, err := setup.RemoteMonitorDefinition(inputs, true)
	if err != nil {
		t.Fatalf("RemoteMonitorDefinition failed: %v", err)
	}

	if len(available.Requires) != 0 {
		t.Fatalf("requires = %v, want none when the entry point exists", available.Requires)
	}
}

func TestUnsupportedPlatformIsReported(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)
	inputs.Platform = "windows"

	if _, err := setup.Definitions(inputs); !errors.Is(err, setup.ErrUnsupportedPlatform) {
		t.Fatalf("Definitions error = %v, want ErrUnsupportedPlatform", err)
	}
}

func TestServiceDirectoryUsesPlatformDefaults(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	directory, err := setup.ServiceDirectory(setup.PlatformLinux)
	if err != nil {
		t.Fatalf("ServiceDirectory failed: %v", err)
	}

	if directory != filepath.Join(configHome, "systemd", "user") {
		t.Fatalf("directory = %q, want the systemd user directory", directory)
	}
}

func TestCoordinatorDefinitionRendersDaemonCoordinator(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)

	definition, err := setup.CoordinatorDefinition(inputs)
	if err != nil {
		t.Fatalf("CoordinatorDefinition failed: %v", err)
	}

	if definition.Name != setup.CoordinatorServiceName {
		t.Fatalf("name = %q, want %q", definition.Name, setup.CoordinatorServiceName)
	}

	if definition.Kind != setup.KindSystemdService {
		t.Fatalf("kind = %q, want %q", definition.Kind, setup.KindSystemdService)
	}

	if !strings.Contains(definition.Content, "# "+setup.ManagedMarker) {
		t.Fatalf("coordinator service missing ownership marker:\n%s", definition.Content)
	}

	if !strings.Contains(definition.Content, "ExecStart=/usr/local/bin/sesh daemon coordinator --socket /run/user/1000/sesh/tmux.sock") {
		t.Fatalf("coordinator service ExecStart is not daemon coordinator:\n%s", definition.Content)
	}

	if !strings.Contains(definition.Content, "--repository /home/user/.local/share/sesh/snapshots") {
		t.Fatalf("coordinator service missing repository flag:\n%s", definition.Content)
	}

	if len(definition.Requires) != 0 {
		t.Fatalf("coordinator service on Linux should have no unmet requirements, got %v", definition.Requires)
	}

	inputs.WithCoordinator = true

	defs, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions with coordinator failed: %v", err)
	}

	if len(defs) != 3 {
		t.Fatalf("defs count = %d, want 3 (autosave service, timer, coordinator)", len(defs))
	}
}

func TestCoordinatorDefinitionDarwinPrerequisite(t *testing.T) {
	t.Parallel()

	inputs := linuxInputs(t)
	inputs.Platform = setup.PlatformDarwin

	darwinDef, err := setup.CoordinatorDefinition(inputs)
	if err != nil {
		t.Fatalf("CoordinatorDefinition on Darwin failed: %v", err)
	}

	if darwinDef.Name != setup.CoordinatorAgentName {
		t.Fatalf("darwin name = %q, want %q", darwinDef.Name, setup.CoordinatorAgentName)
	}

	if len(darwinDef.Requires) == 0 {
		t.Fatal("coordinator on Darwin must declare unmet requirement")
	}
}

func assertPlanUnchanged(t *testing.T, plans []setup.ArtifactPlan, name string) {
	t.Helper()

	for _, plan := range plans {
		if plan.Name == name && plan.Changed {
			t.Fatalf("blocked definition must not plan a write: %+v", plan)
		}
	}
}
