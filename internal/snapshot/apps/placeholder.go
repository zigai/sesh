package apps

import (
	"regexp"
	"slices"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	seshName                 = "sesh"
	placeholderArgumentCount = 4
)

var placeholderTask = regexp.MustCompile(`^t[1-9][0-9]*$`)

// PlaceholderArguments are the arguments after the sesh executable of the pane that holds a
// sleeping task's place until it is woken.
func PlaceholderArguments(task string) []string {
	return []string{"task", "placeholder", task}
}

// PlaceholderLaunch is the launch that shows a sleeping task's placeholder with the given sesh
// executable, in the directory and environment of the agent it stands in for.
func PlaceholderLaunch(executable, task string, agent model.Launch) model.Launch {
	argv := append([]string{seshName}, PlaceholderArguments(task)...)

	return model.Launch{
		Executable:  model.Bytes(executable),
		Arguments:   stringArguments(argv),
		Directory:   slices.Clone(agent.Directory),
		Environment: cloneVariables(agent.Environment),
		Birth:       nil,
		Recovery:    RecoveryRestart,
		Reason:      "",
	}
}

func LaunchHarness(launch model.Launch) (string, bool) {
	harnessID, ok := observedAgentHarness(launch)

	return string(harnessID), ok
}

func sleepingTaskPlaceholder(observed model.Launch) bool {
	executable := string(observed.Executable)

	return cleanAbsolute(executable) && cleanAbsolute(string(observed.Directory)) && executableName(executable) == seshName
}

func preparePlaceholder(fallback model.Launch) (model.Launch, Reason) {
	if len(fallback.Arguments) != placeholderArgumentCount {
		return fallback, ReasonUnsupportedApplication
	}

	task := string(fallback.Arguments[placeholderArgumentCount-1])
	if !placeholderTask.MatchString(task) {
		return fallback, ReasonUnsupportedApplication
	}

	for i, want := range PlaceholderArguments(task) {
		if string(fallback.Arguments[i+1]) != want {
			return fallback, ReasonUnsupportedApplication
		}
	}

	return restartLaunch(fallback, append([]string{seshName}, PlaceholderArguments(task)...)), ReasonTaskPlaceholder
}
