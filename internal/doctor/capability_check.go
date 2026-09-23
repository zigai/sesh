//go:build linux || darwin

package doctor

import "runtime"

func checkCaptureGuarantee() Result {
	return Result{
		Check:   CheckCaptureGuarantee,
		Status:  StatusWarning,
		Reason:  ReasonObservational,
		Message: "capture revalidates the tmux graph and foreground processes for practical recovery; it is an observational result, not an atomic application checkpoint or full runtime equivalent, and RAM, shell-local jobs, pipelines, and terminal microstate are outside release scope while captured scrollback is retained but not replayed",
		Fix:     "",
	}
}

func checkRestoreCapability() Result {
	return Result{
		Check:   CheckRestoreCapability,
		Status:  StatusWarning,
		Reason:  ReasonIncomplete,
		Message: "restore executes practical structural plans with durable dispatch-confirmed supported restart and native resume evidence; application recovery requires an independent native receipt, captured scrollback is not replayed, and atomic checkpoints, RAM, shell-local jobs, pipelines, and terminal microstate are outside release scope",
		Fix:     "",
	}
}

func checkAutosaveCapability() Result {
	message := "scheduled and final saves are available, but native lifecycle integration and real OS shutdown validation remain incomplete"
	if runtime.GOOS == "linux" {
		message = "scheduled and final saves use Linux logind integration; limited guest validation observed ordinary reboot, suspend, and a 3-second inhibitor timeout ending in an incomplete final-save result after 8 seconds, while actual daemon runtime status is checked independently and no universal lifecycle guarantee is claimed"
	}

	return Result{
		Check:   CheckAutosave,
		Status:  StatusWarning,
		Reason:  ReasonIncomplete,
		Message: message,
		Fix:     "",
	}
}
