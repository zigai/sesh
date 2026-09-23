package restore

import "github.com/zigai/sesh/internal/snapshot/model"

const (
	PaneOutcomeDefaultShell         = "default_shell"
	PaneOutcomeUnsupported          = "unsupported"
	PaneOutcomeRestartDispatch      = "restart_dispatch"
	PaneOutcomeNativeResumeDispatch = "native_resume_dispatch"
	PaneOutcomeMissingExecutable    = "missing_executable"
	PaneOutcomeAllowedCWDFallback   = "allowed_cwd_fallback"
	PaneOutcomeEvidenceUnavailable  = "launch_evidence_unavailable"

	PaneReasonProcessLaunchDisabled = "process_launch_disabled"
	PaneReasonEvidenceUnavailable   = "launch_evidence_unavailable"
	PaneReasonUnsupportedLaunch     = "application_restart_unsupported"
	PaneReasonExecutableUnavailable = "approved_launch_unavailable"
	PaneReasonWorkingDirFallback    = "saved_working_directory_missing"
)

type PaneResult struct {
	PaneID             string `json:"paneId"`
	RuntimeID          string `json:"runtimeId"`
	Outcome            string `json:"outcome"`
	Reason             string `json:"reason,omitempty"`
	AppVerified        bool   `json:"appVerified"`
	CWDFallback        bool   `json:"cwdFallback,omitempty"`
	VerificationReason string `json:"verificationReason,omitempty"`
}

func PanesFromVerifiedRuntime(plan Plan, runtimePanes map[string]string, launchProcesses bool) []PaneResult {
	results := make([]PaneResult, 0, len(plan.SourceGraph.Panes))

	for _, pane := range plan.SourceGraph.Panes {
		results = append(results, PaneResult{
			PaneID:             pane.ID,
			RuntimeID:          runtimePanes[pane.ID],
			Outcome:            verifiedPaneOutcome(pane.Launch, launchProcesses),
			Reason:             verifiedPaneReason(pane.Launch, launchProcesses),
			AppVerified:        false,
			CWDFallback:        false,
			VerificationReason: "",
		})
	}

	return results
}

func verifiedPaneOutcome(launch model.Launch, launchProcesses bool) string {
	if !launchProcesses {
		return PaneOutcomeDefaultShell
	}

	switch launch.Recovery {
	case model.RecoveryNonrestartable:
		return PaneOutcomeUnsupported
	case model.RecoveryUnverified:
		if len(launch.Executable) > 0 || len(launch.Arguments) > 0 {
			return PaneOutcomeUnsupported
		}

		return PaneOutcomeDefaultShell
	case model.RecoveryRestart, model.RecoveryResume:
		return PaneOutcomeEvidenceUnavailable
	default:
		return PaneOutcomeDefaultShell
	}
}

func verifiedPaneReason(launch model.Launch, launchProcesses bool) string {
	if !launchProcesses {
		if launch.Recovery == model.RecoveryRestart || launch.Recovery == model.RecoveryResume {
			return PaneReasonProcessLaunchDisabled
		}

		return ""
	}

	switch launch.Recovery {
	case model.RecoveryNonrestartable:
		return launch.Reason
	case model.RecoveryUnverified:
		if len(launch.Executable) > 0 || len(launch.Arguments) > 0 {
			return PaneReasonUnsupportedLaunch
		}
	case model.RecoveryRestart, model.RecoveryResume:
		return PaneReasonEvidenceUnavailable
	}

	return ""
}
