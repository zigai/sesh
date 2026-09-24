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
		outcome, reason := paneOutcome(pane.Launch, launchProcesses)
		results = append(results, PaneResult{
			PaneID:             pane.ID,
			RuntimeID:          runtimePanes[pane.ID],
			Outcome:            outcome,
			Reason:             reason,
			AppVerified:        false,
			CWDFallback:        false,
			VerificationReason: "",
		})
	}

	return results
}

func paneOutcome(launch model.Launch, launchProcesses bool) (string, string) {
	if !launchProcesses {
		if launch.Recovery == model.RecoveryRestart || launch.Recovery == model.RecoveryResume {
			return PaneOutcomeDefaultShell, PaneReasonProcessLaunchDisabled
		}

		return PaneOutcomeDefaultShell, ""
	}

	switch launch.Recovery {
	case model.RecoveryNonrestartable:
		return PaneOutcomeUnsupported, launch.Reason
	case model.RecoveryUnverified:
		if len(launch.Executable) > 0 || len(launch.Arguments) > 0 {
			return PaneOutcomeUnsupported, PaneReasonUnsupportedLaunch
		}

		return PaneOutcomeDefaultShell, ""
	case model.RecoveryRestart, model.RecoveryResume:
		return PaneOutcomeEvidenceUnavailable, PaneReasonEvidenceUnavailable
	}

	return PaneOutcomeDefaultShell, ""
}
