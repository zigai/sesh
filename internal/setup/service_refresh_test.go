package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/samber/oops"
)

var errTestServiceManager = errors.New("private service manager failure")

func TestRefreshDefinitionsPreservesRunningAndEnabledState(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"active", "reloading", "inactive", "failed"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			definitions := refreshDefinitionsFixture(t)
			for range 2 {
				plans := planRefreshFixture(t, definitions)
				manager, calls := recordingServiceManager(t, state)

				applied, result, err := RefreshDefinitions(t.Context(), definitions, plans, false, manager)
				if err != nil {
					t.Fatal(err)
				}

				assertServiceRefreshResult(t, result, state == "active" || state == "reloading")
				assertRefreshPreservesServiceState(t, *calls)

				if applied.Written != 0 && applied.Written != len(definitions) {
					t.Fatalf("written = %d", applied.Written)
				}

				assertRefreshedDefinitions(t, definitions)
			}
		})
	}
}

func assertServiceRefreshResult(t *testing.T, result ServiceRefreshResult, running bool) {
	t.Helper()

	wantRestarts := []string(nil)
	if running {
		wantRestarts = []string{AutosaveTimerName, RemoteServiceName}
	}

	if !result.Reloaded || !slices.Equal(result.Restarted, wantRestarts) {
		t.Fatalf("refresh = %+v, want restarted %v", result, wantRestarts)
	}
}

func assertRefreshPreservesServiceState(t *testing.T, calls []string) {
	t.Helper()

	for _, call := range calls {
		if strings.Contains(call, " enable ") || strings.Contains(call, " disable ") || strings.Contains(call, " start ") {
			t.Fatalf("changed enabled state or started a service: %s", call)
		}

		if strings.Contains(call, "try-restart") && !strings.HasSuffix(call, AutosaveTimerName) && !strings.HasSuffix(call, RemoteServiceName) {
			t.Fatalf("restarted tmux or an in-flight autosave: %s", call)
		}
	}
}

func refreshDefinitionsFixture(t *testing.T) []ServiceDefinition {
	t.Helper()

	directory := t.TempDir()
	names := []string{TmuxServiceName, AutosaveServiceName, AutosaveTimerName, RemoteServiceName}
	definitions := make([]ServiceDefinition, 0, len(names))

	for _, name := range names {
		kind := KindSystemdService
		if name == AutosaveTimerName {
			kind = KindSystemdTimer
		}

		definition := ServiceDefinition{
			Name: name, Kind: kind, Path: filepath.Join(directory, name),
			Content: "# " + ManagedMarker + "\n[Service]\nType=exec\nExecStart=/private/new-sesh\n",
		}
		if err := os.WriteFile(definition.Path, []byte(strings.ReplaceAll(definition.Content, "new-sesh", "old-sesh")), 0o600); err != nil {
			t.Fatal(err)
		}

		definitions = append(definitions, definition)
	}

	return definitions
}

func planRefreshFixture(t *testing.T, definitions []ServiceDefinition) []ArtifactPlan {
	t.Helper()

	plans, err := PlanDefinitions(definitions)
	if err != nil {
		t.Fatal(err)
	}

	return plans
}

func recordingServiceManager(t *testing.T, state string) (ServiceManager, *[]string) {
	t.Helper()

	var calls []string

	manager := ServiceManager{
		Inspect: func(ctx context.Context, executable string, arguments ...string) (string, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > serviceRefreshTimeout {
				t.Fatal("inspection has no bounded deadline")
			}

			calls = append(calls, executable+" "+strings.Join(arguments, " "))

			return state + "\n", nil
		},
		Run: func(_ context.Context, executable string, arguments ...string) error {
			calls = append(calls, executable+" "+strings.Join(arguments, " "))
			return nil
		},
	}

	return manager, &calls
}

func assertRefreshedDefinitions(t *testing.T, definitions []ServiceDefinition) {
	t.Helper()

	for _, definition := range definitions {
		content, err := os.ReadFile(definition.Path)
		if err != nil {
			t.Fatal(err)
		}

		if string(content) != definition.Content {
			t.Fatalf("definition was not refreshed: %s", content)
		}
	}
}

func TestRefreshDefinitionsInspectsEveryRestartBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	definitions := refreshDefinitionsFixture(t)
	manager, calls := recordingServiceManager(t, "active")
	manager.Inspect = func(_ context.Context, _ string, arguments ...string) (string, error) {
		assertRefreshDefinitionsUnchanged(t, definitions)

		if arguments[len(arguments)-1] == RemoteServiceName {
			return "", errTestServiceManager
		}

		return "active", nil
	}

	applied, result, err := RefreshDefinitions(t.Context(), definitions, planRefreshFixture(t, definitions), false, manager)
	if !errors.Is(err, ErrServiceInspect) || !errors.Is(err, errTestServiceManager) || applied.Written != 0 || result.Reloaded || len(*calls) > 0 {
		t.Fatalf("failed inspection = %+v, %+v, %v; calls = %v", applied, result, err, *calls)
	}

	structured, ok := oops.AsOops(err)
	if !ok || structured.Code() != "service_refresh_failed" || structured.Context()["phase"] != "inspect" {
		t.Fatalf("missing fixed refresh diagnostics: %v", err)
	}
}

func assertRefreshDefinitionsUnchanged(t *testing.T, definitions []ServiceDefinition) {
	t.Helper()

	for _, definition := range definitions {
		content, err := os.ReadFile(definition.Path)
		if err != nil || !strings.Contains(string(content), "old-sesh") {
			t.Fatalf("inspection observed already changed definition: %s, %v", content, err)
		}
	}
}

func TestRefreshDefinitionsRetriesReloadAndRestartFailures(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"daemon-reload", AutosaveTimerName, RemoteServiceName} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()

			definitions := refreshDefinitionsFixture(t)
			manager, _ := recordingServiceManager(t, "active")
			manager.Run = func(_ context.Context, _ string, arguments ...string) error {
				if slices.Contains(arguments, phase) {
					return errTestServiceManager
				}

				return nil
			}

			applied, result, err := RefreshDefinitions(t.Context(), definitions, planRefreshFixture(t, definitions), false, manager)
			if !errors.Is(err, errTestServiceManager) || applied.Written != len(definitions) {
				t.Fatalf("failed refresh = %+v, %+v, %v", applied, result, err)
			}

			if result.Reloaded != (phase != "daemon-reload") {
				t.Fatalf("reload result was not preserved: %+v", result)
			}

			if phase == RemoteServiceName && !slices.Equal(result.Restarted, []string{AutosaveTimerName}) {
				t.Fatalf("completed timer restart was not preserved: %+v", result)
			}

			manager, _ = recordingServiceManager(t, "active")

			applied, result, err = RefreshDefinitions(t.Context(), definitions, planRefreshFixture(t, definitions), false, manager)
			if err != nil || applied.Written != 0 || !result.Reloaded || !slices.Equal(result.Restarted, []string{AutosaveTimerName, RemoteServiceName}) {
				t.Fatalf("retry with current definitions = %+v, %+v, %v", applied, result, err)
			}
		})
	}
}

func TestRefreshDefinitionsRefusesAmbiguousManagerStates(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"activating", "deactivating", "unexpected", "", "active\ninactive"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			definitions := refreshDefinitionsFixture(t)
			manager, calls := recordingServiceManager(t, state)

			applied, _, err := RefreshDefinitions(t.Context(), definitions, planRefreshFixture(t, definitions), false, manager)
			if !errors.Is(err, ErrServiceInspect) || applied.Written != 0 || len(*calls) != 1 {
				t.Fatalf("ambiguous refresh = %+v, %v; calls = %v", applied, err, *calls)
			}
		})
	}
}

func TestRefreshDefinitionsDoesNotActivateMissingForeignOrBlockedServices(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"missing", "foreign", "symlink", "blocked", "forced-foreign"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			definitions := refreshDefinitionsFixture(t)[2:]
			prepareUnownedRefreshFixture(t, definitions, kind)
			manager, calls := recordingServiceManager(t, "active")
			manager.Inspect = func(context.Context, string, ...string) (string, error) {
				t.Fatal("inspected a missing, foreign or blocked service")
				return "", errTestServiceManager
			}

			_, result, err := RefreshDefinitions(t.Context(), definitions, planRefreshFixture(t, definitions), kind == "forced-foreign", manager)
			if err != nil || len(result.Restarted) != 0 {
				t.Fatalf("refresh = %+v, %v", result, err)
			}

			for _, call := range *calls {
				if call != "systemctl --user daemon-reload" {
					t.Fatalf("activated an unowned or missing service: %s", call)
				}
			}

			if kind == "foreign" || kind == "symlink" || kind == "blocked" {
				if len(*calls) > 0 {
					t.Fatalf("contacted manager without eligible definitions: %v", *calls)
				}
			}
		})
	}
}

func prepareUnownedRefreshFixture(t *testing.T, definitions []ServiceDefinition, kind string) {
	t.Helper()

	for index, definition := range definitions {
		if kind == "blocked" {
			definitions[index].Requires = []string{"unmet prerequisite"}
			continue
		}

		if err := os.Remove(definition.Path); err != nil {
			t.Fatal(err)
		}

		switch kind {
		case "foreign", "forced-foreign":
			if err := os.WriteFile(definition.Path, []byte("private foreign config"), 0o600); err != nil {
				t.Fatal(err)
			}
		case "symlink":
			if err := os.Symlink("/dev/null", definition.Path); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRefreshDefinitionsReportsTmuxStartupChangesWithoutRestart(t *testing.T) {
	t.Parallel()

	definitions := refreshDefinitionsFixture(t)[:1]
	manager, calls := recordingServiceManager(t, "active")

	_, result, err := RefreshDefinitions(t.Context(), definitions, planRefreshFixture(t, definitions), false, manager)
	if err != nil || !slices.Equal(result.RestartRequired, []string{TmuxServiceName}) || len(result.Restarted) != 0 {
		t.Fatalf("refresh = %+v, %v", result, err)
	}

	if !slices.Equal(*calls, []string{"systemctl --user daemon-reload"}) {
		t.Fatalf("tmux refresh changed service state: %v", *calls)
	}
}

func TestRefreshDefinitionsHonorsCancellationBeforeWriting(t *testing.T) {
	t.Parallel()

	definitions := refreshDefinitionsFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	manager, _ := recordingServiceManager(t, "active")
	manager.Inspect = func(context.Context, string, ...string) (string, error) {
		cancel()
		return "active", nil
	}
	manager.Run = func(context.Context, string, ...string) error {
		t.Fatal("ran a mutation after cancellation")
		return nil
	}

	applied, _, err := RefreshDefinitions(ctx, definitions, planRefreshFixture(t, definitions), false, manager)
	if !errors.Is(err, context.Canceled) || applied.Written != 0 {
		t.Fatalf("canceled refresh = %+v, %v", applied, err)
	}
}

func TestRefreshDefinitionsBoundsManagerWait(t *testing.T) {
	t.Parallel()

	definitions := refreshDefinitionsFixture(t)
	plans := planRefreshFixture(t, definitions)

	synctest.Test(t, func(t *testing.T) {
		manager := ServiceManager{
			Inspect: func(ctx context.Context, _ string, _ ...string) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			},
			Run: func(context.Context, string, ...string) error {
				t.Fatal("ran mutation after deadline")
				return nil
			},
		}

		started := time.Now()

		applied, _, err := RefreshDefinitions(t.Context(), definitions, plans, false, manager)
		if !errors.Is(err, context.DeadlineExceeded) || applied.Written != 0 || time.Since(started) != serviceRefreshTimeout {
			t.Fatalf("timed out refresh = %+v, %v, elapsed %s", applied, err, time.Since(started))
		}
	})
}

func TestServiceInspectionOutputIsBounded(t *testing.T) {
	t.Parallel()

	var output serviceInspectionOutput

	for range 3 {
		data := []byte(strings.Repeat("x", maxServiceInspectionBytes/2))

		written, err := output.Write(data)
		if err != nil || written != len(data) {
			t.Fatalf("inspection output write = %d, %v", written, err)
		}
	}

	if !output.overflow || output.buffer.Len() != maxServiceInspectionBytes {
		t.Fatalf("inspection output length = %d, overflow = %v", output.buffer.Len(), output.overflow)
	}
}

func TestRefreshDefinitionsPreservesWrittenCountOnPublicationFailure(t *testing.T) {
	t.Parallel()

	definitions := refreshDefinitionsFixture(t)[1:]
	definitions = []ServiceDefinition{definitions[0], definitions[2]}

	for _, definition := range definitions {
		if err := os.Remove(definition.Path); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Mkdir(definitions[1].Path, 0o700); err != nil {
		t.Fatal(err)
	}

	manager, calls := recordingServiceManager(t, "active")

	applied, result, err := RefreshDefinitions(t.Context(), definitions, planRefreshFixture(t, definitions), true, manager)
	if err == nil || applied.Written != 1 || result.Reloaded || len(*calls) > 0 {
		t.Fatalf("publication failure = %+v, %+v, %v; calls = %v", applied, result, err, *calls)
	}

	assertRefreshedDefinitions(t, definitions[:1])

	if _, ok := errors.AsType[*os.LinkError](err); !ok {
		t.Fatalf("lost typed rename failure: %v", err)
	}
}
