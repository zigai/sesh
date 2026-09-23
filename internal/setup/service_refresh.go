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
	Reloaded        bool     `json:"reloaded"`
	Restarted       []string `json:"restarted,omitempty"`
	RestartRequired []string `json:"restartRequired,omitempty"`
}

type serviceRefreshPlan struct {
	reload          bool
	restart         []string
	restartRequired []string
}

type serviceInspectionOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

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

	return applied, refreshed, nil
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
		running, err := manager.running(ctx, definition.Name)
		if err != nil {
			return err
		}

		if running {
			prepared.restart = append(prepared.restart, definition.Name)
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

func (manager ServiceManager) running(ctx context.Context, name string) (bool, error) {
	inspect := manager.Inspect
	if inspect == nil {
		inspect = inspectServiceCommand
	}

	output, err := inspect(ctx, "systemctl", "--user", "show", "--property=ActiveState", "--value", name)
	if err != nil {
		return false, errors.Join(ErrServiceInspect, err)
	}

	switch strings.TrimSpace(output) {
	case "active", "reloading":
		return true, nil
	case "inactive", "failed":
		return false, nil
	default:
		return false, ErrServiceInspect
	}
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
