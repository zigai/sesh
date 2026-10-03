package setup

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/samber/oops"
)

const (
	maxServiceInspectionBytes = 1 << 20
	serviceRefreshTimeout     = time.Minute
)

var (
	ErrServiceInspect = errors.New("inspect service manager before refreshing definitions")
	ErrServiceReload  = errors.New("reload refreshed service definitions")
	ErrServiceRestart = errors.New("refresh running service")
)

type ServiceManager struct {
	Inspect func(context.Context, string, ...string) (string, error)
	Run     func(context.Context, string, ...string) error
}

type ServiceRefreshResult struct {
	Reloaded  bool     `json:"reloaded"`
	Restarted []string `json:"restarted,omitempty"`
	// Recovered lists failed services started again on their refreshed definition.
	Recovered       []string `json:"recovered,omitempty"`
	RestartRequired []string `json:"restartRequired,omitempty"`
}

// UnitState is what the service manager reports for one unit.
type UnitState struct {
	Name    string `json:"name"`
	Active  string `json:"active"`
	Enabled string `json:"enabled"`
}

type serviceRefreshPlan struct {
	reload          bool
	restart         []string
	recover         []string
	restartRequired []string
}

type serviceInspectionOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

// Running reports whether the unit is active, and Enabled whether it starts with the session.
func (u UnitState) Running() bool { return u.Active == "active" || u.Active == "reloading" }

func (u UnitState) EnabledAtLogin() bool { return u.Enabled == "enabled" || u.Enabled == "static" }

func RefreshDefinitions(
	ctx context.Context,
	definitions []ServiceDefinition,
	plans []ArtifactPlan,
	force bool,
	manager ServiceManager,
) (ApplyResult, ServiceRefreshResult, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceRefreshTimeout)
	defer cancel()

	var refreshed ServiceRefreshResult

	prepared, err := prepareServiceRefresh(ctx, definitions, plans, force, manager)
	if err != nil {
		return emptyApplyResult(), refreshed, enrichServiceRefresh("inspect", err)
	}

	refreshed.RestartRequired = prepared.restartRequired

	if err := ctx.Err(); err != nil {
		return emptyApplyResult(), refreshed, enrichServiceRefresh("inspect", err)
	}

	applied, err := ApplyDefinitions(definitions, plans, force)
	if err != nil {
		return applied, refreshed, enrichServiceRefresh("write", err)
	}

	if !prepared.reload {
		return applied, refreshed, nil
	}

	if err := manager.run(ctx, "--user", "daemon-reload"); err != nil {
		return applied, refreshed, enrichServiceRefresh("reload", errors.Join(ErrServiceReload, err))
	}

	refreshed.Reloaded = true

	for _, name := range prepared.restart {
		if err := manager.run(ctx, "--user", "try-restart", name); err != nil {
			return applied, refreshed, enrichServiceRefresh("restart", errors.Join(ErrServiceRestart, err))
		}

		refreshed.Restarted = append(refreshed.Restarted, name)
	}

	// A failed service was started and then crashed, often on the very definition this refresh
	// replaced. Starting it again restores the state its user chose instead of leaving it down.
	for _, name := range prepared.recover {
		if err := manager.run(ctx, "--user", "reset-failed", name); err != nil {
			return applied, refreshed, enrichServiceRefresh("recover", errors.Join(ErrServiceRestart, err))
		}

		if err := manager.run(ctx, "--user", "restart", name); err != nil {
			return applied, refreshed, enrichServiceRefresh("recover", errors.Join(ErrServiceRestart, err))
		}

		refreshed.Recovered = append(refreshed.Recovered, name)
	}

	return applied, refreshed, nil
}

// UnitStates reports the active and enabled state of each named unit.
func UnitStates(ctx context.Context, manager ServiceManager, names []string) ([]UnitState, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceRefreshTimeout)
	defer cancel()

	states := make([]UnitState, 0, len(names))

	for _, name := range names {
		active, err := manager.property(ctx, name, "ActiveState")
		if err != nil {
			return states, err
		}

		enabled, err := manager.property(ctx, name, "UnitFileState")
		if err != nil {
			return states, err
		}

		states = append(states, UnitState{Name: name, Active: active, Enabled: enabled})
	}

	return states, nil
}

func prepareServiceRefresh(
	ctx context.Context,
	definitions []ServiceDefinition,
	plans []ArtifactPlan,
	force bool,
	manager ServiceManager,
) (serviceRefreshPlan, error) {
	var prepared serviceRefreshPlan

	if err := validateRefreshPlans(definitions, plans); err != nil {
		return prepared, err
	}

	for index, definition := range definitions {
		plan := plans[index]
		if len(plan.Requires) > 0 || (plan.State == ArtifactForeign && !force) {
			continue
		}

		prepared.reload = true

		if plan.State != ArtifactCurrent && plan.State != ArtifactStale {
			continue
		}

		if err := inspectRefreshDefinition(ctx, definition, manager, &prepared); err != nil {
			return prepared, err
		}
	}

	return prepared, nil
}

func validateRefreshPlans(definitions []ServiceDefinition, plans []ArtifactPlan) error {
	if len(definitions) != len(plans) {
		return ErrPlanMismatch
	}

	for index, definition := range definitions {
		plan := plans[index]
		if plan.Name != definition.Name || plan.Path != definition.Path {
			return ErrPlanMismatch
		}

		if definition.Kind != KindSystemdService && definition.Kind != KindSystemdTimer {
			return ErrUnsupportedPlatform
		}
	}

	return nil
}

func inspectRefreshDefinition(
	ctx context.Context,
	definition ServiceDefinition,
	manager ServiceManager,
	prepared *serviceRefreshPlan,
) error {
	previous, err := readDefinition(definition.Path)
	if err != nil {
		return err
	}

	if !previous.Exists || !previous.Owned {
		return ErrPlanMismatch
	}

	switch definition.Name {
	case TmuxServiceName:
		if systemdStartupDefinition(previous.Content) != systemdStartupDefinition(definition.Content) {
			prepared.restartRequired = append(prepared.restartRequired, definition.Name)
		}
	case AutosaveTimerName, RemoteServiceName, CoordinatorServiceName:
		state, err := manager.activeState(ctx, definition.Name)
		if err != nil {
			return err
		}

		switch state {
		case "active", "reloading":
			prepared.restart = append(prepared.restart, definition.Name)
		case "failed":
			prepared.recover = append(prepared.recover, definition.Name)
		}
	}

	return nil
}

func systemdStartupDefinition(content string) string {
	var startup strings.Builder

	for line := range strings.SplitSeq(content, "\n") {
		if strings.HasPrefix(line, "Type=") || strings.HasPrefix(line, "ExecStart") {
			startup.WriteString(line)
			startup.WriteByte('\n')
		}
	}

	return startup.String()
}

// activeState returns a unit's ActiveState, refusing a transitional state whose outcome a
// refresh cannot predict.
func (manager ServiceManager) activeState(ctx context.Context, name string) (string, error) {
	state, err := manager.property(ctx, name, "ActiveState")
	if err != nil {
		return "", err
	}

	switch state {
	case "active", "reloading", "inactive", "failed":
		return state, nil
	default:
		return "", ErrServiceInspect
	}
}

func (manager ServiceManager) property(ctx context.Context, name, property string) (string, error) {
	inspect := manager.Inspect
	if inspect == nil {
		inspect = inspectServiceCommand
	}

	output, err := inspect(ctx, "systemctl", "--user", "show", "--property="+property, "--value", name)
	if err != nil {
		return "", errors.Join(ErrServiceInspect, err)
	}

	return strings.TrimSpace(output), nil
}

func (manager ServiceManager) run(ctx context.Context, arguments ...string) error {
	run := manager.Run
	if run == nil {
		run = runServiceCommand
	}

	return run(ctx, "systemctl", arguments...)
}

func inspectServiceCommand(ctx context.Context, executable string, arguments ...string) (string, error) {
	var output serviceInspectionOutput

	command := exec.CommandContext(ctx, executable, arguments...)
	command.Stdout = &output
	command.WaitDelay = time.Second

	if err := command.Run(); err != nil {
		return "", oops.In("setup").Code("service_refresh_failed").With("operation", "inspect_service_command").Wrap(errors.Join(err, ctx.Err()))
	}

	if output.overflow {
		return "", ErrServiceInspect
	}

	return output.buffer.String(), nil
}

func runServiceCommand(ctx context.Context, executable string, arguments ...string) error {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.WaitDelay = time.Second

	if err := command.Run(); err != nil {
		return oops.In("setup").Code("service_refresh_failed").With("operation", "run_service_command").Wrap(errors.Join(err, ctx.Err()))
	}

	return nil
}

func (output *serviceInspectionOutput) Write(data []byte) (int, error) {
	size := len(data)
	remaining := maxServiceInspectionBytes - output.buffer.Len()

	if size > remaining {
		output.overflow = true
		data = data[:remaining]
	}

	_, _ = output.buffer.Write(data)

	return size, nil
}

func enrichServiceRefresh(phase string, err error) error {
	return oops.In("setup").Code("service_refresh_failed").
		With("operation", "refresh_services").With("phase", phase).Wrap(err)
}
