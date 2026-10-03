//go:build linux || darwin

package doctor

import "runtime"

func checkCaptureGuarantee() Result {
	return Result{
		Check:   CheckCaptureGuarantee,
		Status:  StatusWarning,
		Reason:  ReasonObservational,
		Message: "saves record tmux and its running programs, not a checkpoint: program memory, shell jobs and pipelines are not saved, and scrollback is kept but not replayed",
		Fix:     "",
	}
}

func checkRestoreCapability() Result {
	return Result{
		Check:   CheckRestoreCapability,
		Status:  StatusWarning,
		Reason:  ReasonIncomplete,
		Message: "restore rebuilds sessions and relaunches or resumes supported programs, but cannot confirm a program recovered; scrollback is not replayed",
		Fix:     "",
	}
}

func checkAutosaveCapability() Result {
	message := "saves run on a timer and before shutdown, but native lifecycle integration and shutdown validation are incomplete on this platform"
	if runtime.GOOS == "linux" {
		message = "saves run on a timer and before shutdown and sleep; a save cut short by shutdown is recorded as incomplete and restore uses the newest finished save"
	}

	return Result{
		Check:   CheckAutosave,
		Status:  StatusWarning,
		Reason:  ReasonIncomplete,
		Message: message,
		Fix:     "",
	}
}
