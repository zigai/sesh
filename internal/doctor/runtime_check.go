//go:build linux || darwin

package doctor

func (s *Service) checkDaemonRuntime() Result {
	result := Result{
		Check:   CheckDaemonRuntime,
		Status:  StatusWarning,
		Reason:  ReasonNotConfigured,
		Message: "no explicit tmux endpoint was selected for a live daemon query",
		Fix:     "pass --socket for the daemon-managed endpoint",
	}

	switch s.daemonState {
	case "ready":
		if s.daemonPhase != "running" || !s.inhibitorsHeld {
			return s.notReadyDaemonRuntime(result)
		}

		result.Status = StatusOK
		result.Reason = ""
		result.Message = "the daemon is live and currently holds its shutdown and sleep delay inhibitors"
		result.Fix = ""
	case "not_ready":
		return s.notReadyDaemonRuntime(result)
	case "absent":
		result.Reason = ReasonNotRunning
		result.Message = "no live daemon runtime endpoint was found"
		result.Fix = "start the endpoint-scoped sesh daemon"
	case "stale":
		result.Reason = ReasonStale
		result.Message = "the daemon runtime endpoint is stale and has no live owner"
		result.Fix = "verify no daemon owns the endpoint, then remove the stale private runtime socket"
	case "unverifiable":
		result.Reason = ReasonUnverifiable
		result.Message = "the daemon runtime owner or lifecycle state could not be verified"
		result.Fix = "inspect the daemon service and runtime directory"
	case "unknown":
		result.Reason = ReasonUnverifiable
		result.Message = "service management or daemon runtime state could not be verified"
		result.Fix = "inspect service management and daemon runtime status"
	case "unsupported":
		result.Reason = ReasonIncompatible
		result.Message = "daemon runtime and lifecycle management are unsupported on this platform"
		result.Fix = ""
	}

	return result
}

func (s *Service) notReadyDaemonRuntime(result Result) Result {
	switch {
	case s.daemonPhase == "initializing":
		result.Reason = ReasonInitializing
		result.Message = "the live daemon is still initializing"
		result.Fix = "wait for scheduler and lifecycle readiness"
	case s.daemonPhase == "running" && !s.inhibitorsHeld:
		result.Reason = ReasonInhibitorReleased
		result.Message = "the daemon is live, but its delay inhibitors are currently released"
		result.Fix = "inspect the active lifecycle cycle and last final-save result"
	default:
		result.Reason = ReasonLifecycleActive
		result.Message = "the live daemon is processing or stopping after a lifecycle event"
		result.Fix = "inspect the final-save result after the lifecycle cycle finishes"
	}

	return result
}
