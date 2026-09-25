package setup

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/samber/oops"
	"github.com/zigai/aht/v2/pkg/harness"
	"github.com/zigai/aht/v2/pkg/manage"
	"github.com/zigai/aht/v2/pkg/registry"
)

const (
	TrackerEnable  = "enable"
	TrackerDisable = "disable"
	TrackerStatus  = "status"
	TrackerNone    = ""
)

var ErrUnknownTrackerMode = errors.New("unknown agent tracker operation")

var ErrUnknownAgent = errors.New("unknown agent")

type AgentRequest struct {
	Agents       []string
	DryRun       bool
	Force        bool
	UseShim      bool
	TargetBinary string
	Tracker      string
}

type AgentHarnessResult struct {
	Harness  string
	Path     string
	Changed  bool
	Message  string
	NextStep string
}

type AgentTrackerResult struct {
	Platform  string
	Manager   string
	Installed bool
	Current   bool
	Running   bool
	Changed   bool
	Message   string
}

type AgentResult struct {
	DryRun    bool
	Harnesses []AgentHarnessResult
	Tracker   AgentTrackerResult
	TrackerOp string
}

func SetupAgents(ctx context.Context, request AgentRequest) (AgentResult, error) {
	harnesses, err := resolveHarnesses(request.Agents)
	if err != nil {
		return AgentResult{}, oops.In("setup").Code("setup_agent_failed").With("operation", "setup_agents").Wrap(err)
	}

	if err := validateTrackerMode(request.Tracker); err != nil {
		return AgentResult{}, oops.In("setup").Code("setup_agent_failed").With("operation", "setup_agents").Wrap(err)
	}

	manager := manage.New(manage.Config{
		Binary:             "",
		StorePath:          "",
		TrackerInterval:    0,
		TrackerGracePeriod: 0,
	})

	result := AgentResult{
		DryRun:    request.DryRun,
		Harnesses: make([]AgentHarnessResult, 0, len(harnesses)),
		Tracker:   emptyTrackerResult(),
		TrackerOp: request.Tracker,
	}

	for _, target := range harnesses {
		installed, err := manager.InstallIntegration(ctx, target, manage.IntegrationOptions{
			TargetBinary: request.TargetBinary,
			DryRun:       request.DryRun,
			Force:        request.Force,
			UseShim:      request.UseShim,
		})
		if err != nil {
			return result, oops.In("setup").Code("setup_agent_failed").With("operation", "install_integration").Wrap(err)
		}

		result.Harnesses = append(result.Harnesses, AgentHarnessResult{
			Harness:  string(installed.Harness),
			Path:     installed.Path,
			Changed:  installed.Changed,
			Message:  installed.Message,
			NextStep: installed.NextStep,
		})
	}

	tracker, err := runTracker(ctx, manager, request)
	if err != nil {
		return result, oops.In("setup").Code("setup_agent_failed").With("operation", "setup_agents").Wrap(err)
	}

	result.Tracker = tracker

	return result, nil
}

func AgentStatus(ctx context.Context, request AgentRequest) (AgentResult, error) {
	harnesses, err := resolveHarnesses(request.Agents)
	if err != nil {
		return AgentResult{}, oops.In("setup").Code("setup_agent_failed").With("operation", "agent_status").Wrap(err)
	}

	manager := manage.New(manage.Config{
		Binary:             "",
		StorePath:          "",
		TrackerInterval:    0,
		TrackerGracePeriod: 0,
	})

	result := AgentResult{
		DryRun:    false,
		Harnesses: make([]AgentHarnessResult, 0, len(harnesses)),
		Tracker:   emptyTrackerResult(),
		TrackerOp: TrackerNone,
	}

	for _, target := range harnesses {
		status, err := manager.IntegrationStatus(ctx, target)
		if err != nil {
			return result, oops.In("setup").Code("setup_agent_failed").With("operation", "inspect_integration").Wrap(err)
		}

		result.Harnesses = append(result.Harnesses, AgentHarnessResult{
			Harness:  string(status.Harness),
			Path:     strings.Join(status.Paths, ", "),
			Changed:  status.Status == manage.ArtifactCurrent,
			Message:  string(status.Status) + ": " + status.Message,
			NextStep: status.NextStep,
		})
	}

	tracker, err := runTracker(ctx, manager, AgentRequest{
		Agents:       nil,
		DryRun:       false,
		Force:        false,
		UseShim:      false,
		TargetBinary: "",
		Tracker:      TrackerStatus,
	})
	if err != nil {
		return result, oops.In("setup").Code("setup_agent_failed").With("operation", "agent_status").Wrap(err)
	}

	result.Tracker = tracker
	result.TrackerOp = TrackerStatus

	return result, nil
}

func runTracker(ctx context.Context, manager *manage.Manager, request AgentRequest) (AgentTrackerResult, error) {
	if request.Tracker == TrackerNone {
		return emptyTrackerResult(), nil
	}

	options := manage.TrackerOptions{DryRun: request.DryRun}

	var (
		result manage.TrackerResult
		err    error
	)

	switch request.Tracker {
	case TrackerEnable:
		result, err = manager.EnableTracker(ctx, options)
	case TrackerDisable:
		result, err = manager.DisableTracker(ctx, options)
	case TrackerStatus:
		result, err = manager.TrackerStatus(ctx)
	default:
		return emptyTrackerResult(), oops.In("setup").Code("setup_agent_failed").With("operation", "run_tracker").Wrap(ErrUnknownTrackerMode)
	}

	if err != nil {
		return emptyTrackerResult(), oops.In("setup").Code("setup_agent_failed").With("operation", "run_tracker").Wrap(err)
	}

	return AgentTrackerResult{
		Platform:  result.Platform,
		Manager:   result.Manager,
		Installed: result.Installed,
		Current:   result.Current,
		Running:   result.Running,
		Changed:   result.Changed,
		Message:   result.Message,
	}, nil
}

func validateTrackerMode(mode string) error {
	switch mode {
	case TrackerNone, TrackerEnable, TrackerDisable, TrackerStatus:
		return nil
	default:
		return oops.In("setup").Code("setup_agent_failed").With("operation", "validate_tracker_mode").Wrap(ErrUnknownTrackerMode)
	}
}

func resolveHarnesses(names []string) ([]registry.Harness, error) {
	supported := manage.SupportedHarnesses()

	if len(names) == 0 {
		return slices.Clone(supported), nil
	}

	selected := make([]registry.Harness, 0, len(names))
	seen := make(map[registry.Harness]bool, len(names))

	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}

		parsed, err := harness.Parse(trimmed)
		if err != nil {
			return nil, oops.In("setup").Code("setup_agent_failed").With("operation", "parse_agent_list").Wrap(errors.Join(ErrUnknownAgent, err))
		}

		if seen[parsed] {
			continue
		}

		seen[parsed] = true
		selected = append(selected, parsed)
	}

	return selected, nil
}

func emptyTrackerResult() AgentTrackerResult {
	return AgentTrackerResult{
		Platform:  "",
		Manager:   "",
		Installed: false,
		Current:   false,
		Running:   false,
		Changed:   false,
		Message:   "",
	}
}
