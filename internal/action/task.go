package action

import "strconv"

var (
	TaskMenu = define(Descriptor{
		ID:      "task.menu",
		Title:   "New task menu",
		Summary: "Show the popup that starts a task or opens a tab",
		Help: "Show a popup on the invoking client that starts a task.\n\n" +
			"In a project session it picks where the task works (a new worktree, a new worktree on an " +
			"existing branch, an existing worktree, or the main checkout) and which agent runs there. " +
			"Elsewhere it offers a plain tab in the pane's directory or a loose task with an agent.",
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: true,
		Interactive: true,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(Subject, Values) (OpenTaskMenu, error) { return OpenTaskMenu{}, nil }, func(OpenedTaskMenu) string { return "" })

	TaskNew = define(Descriptor{
		ID:      "task.new",
		Title:   "New task",
		Summary: "Start a task with an agent harness",
		Help: "Start a task from the invoking pane, in a window with the agent above an editor. In a project " +
			"session, or in the project --project names, --where picks the task's checkout: new (a worktree " +
			"on a new branch from --branch, by default the default branch), branch (a new worktree on " +
			"--branch, a local branch or one only --remote has), worktree (the existing worktree --dir) " +
			"or main (the main checkout). Sesh removes on finish only the worktree and branch it created. " +
			"Elsewhere the task runs in the pane's directory. --title names the task and its new branch and " +
			"is passed to the agent as configured by the harness's prompt_args. The harness \"none\" " +
			"starts the task with a shell instead of an agent.",
		Subject: SubjectNone,
		Arity:   ArityOne,
		Params: []Param{
			newParam("harness", ParamText, "Harness to start the task with").required().positional(),
			newParam("where", ParamChoice, "Checkout a project task works in").withChoices("new", "branch", "worktree", "main").withDefault("new"),
			newParam("branch", ParamText, "Branch for --where branch, or the base for --where new"),
			newParam("remote", ParamText, "Remote that has the --where branch when no local branch does"),
			newParam("dir", ParamPath, "Existing worktree for --where worktree"),
			newParam("title", ParamText, "What the task is about"),
			newParam("project", ParamPath, "Project to start the task in instead of the pane's"),
		},
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(_ Subject, values Values) (NewTask, error) {
		return NewTask{
			Harness: values.String("harness"), Where: values.String("where"), Branch: values.String("branch"),
			Remote: values.String("remote"), Directory: values.String("dir"), Title: values.String("title"),
			Project: values.String("project"),
		}, nil
	}, func(out StartedTask) string {
		if out.Number == 0 {
			return "Started task " + out.ID
		}

		return "Started task " + strconv.Itoa(out.Number)
	})

	TaskTab = define(Descriptor{
		ID:          "task.tab",
		Title:       "New tab",
		Summary:     "Open a plain window in the pane's directory",
		Help:        "Open a plain window next to the current one, in the invoking pane's directory.",
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(Subject, Values) (OpenTab, error) { return OpenTab{}, nil }, func(OpenedTab) string { return "" })

	TaskPopup = define(Descriptor{
		ID:      "task.popup",
		Title:   "Task terminal",
		Summary: "Toggle the task's popup terminal",
		Help: "Open the popup terminal of the task in the invoking window, creating it in the task's " +
			"directory on first use. The popup keeps running after it is closed.",
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(Subject, Values) (OpenTaskPopup, error) { return OpenTaskPopup{}, nil }, func(ClosedTaskPopup) string { return "" })

	TaskSleep = define(Descriptor{
		ID:      "task.sleep",
		Title:   "Sleep task",
		Summary: "Stop a task's idle agent so it can be resumed later",
		Help: "Stop the agent of a task, keeping its window, editor and popup. " +
			"Only an idle agent whose harness has a verified stop and whose native resume command AHT holds " +
			"can sleep. Its pane shows a placeholder until the task is woken.",
		Subject:     SubjectTask,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteOnByDefault,
		check:       nil,
	}, func(subject Subject, _ Values) (SleepTask, error) {
		return SleepTask{Task: subject.Task}, nil
	}, func(out SleptTask) string { return "Task " + out.ID + " is asleep" })

	TaskWake = define(Descriptor{
		ID:      "task.wake",
		Title:   "Wake task",
		Summary: "Resume a sleeping task's agent",
		Help: "Relaunch the agent of a sleeping task in its pane through the harness's native resume. " +
			"The relaunch is reported once it is dispatched; whether the conversation resumed is up to the harness.",
		Subject:     SubjectTask,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteOnByDefault,
		check:       nil,
	}, func(subject Subject, _ Values) (WakeTask, error) {
		return WakeTask{Task: subject.Task}, nil
	}, func(out WokenTask) string {
		if out.AlreadyAwake {
			return "Task " + out.ID + " was already awake"
		}

		return "Resumed task " + out.ID
	})

	TaskFinish = define(Descriptor{
		ID:      "task.finish",
		Title:   "Finish task",
		Summary: "Remove a task's window, popup, worktree and branch",
		Help: "Remove a task: its popup terminal, its window, and for a project task its worktree and branch. " +
			"Without discard it refuses when that would lose work: commits not merged into the base branch, " +
			"uncommitted changes, or a pull request closed without merging.",
		Subject:     SubjectTask,
		Arity:       ArityOne,
		Params:      []Param{newParam("discard", ParamBool, "Remove the task even if that loses work")},
		NeedsClient: false,
		Interactive: false,
		Destructive: true,
		Remote:      RemoteOffByDefault,
		check:       nil,
	}, func(subject Subject, values Values) (FinishTask, error) {
		return FinishTask{Task: subject.Task, Discard: values.Bool("discard")}, nil
	}, func(out FinishedTask) string { return "Finished task " + out.ID })

	TaskRun = define(Descriptor{
		ID:      "task.run",
		Title:   "Run task action",
		Summary: "Run a named task action in the task's popup terminal",
		Help: "Run one of the configured task actions headless in the task's worktree, in a new window of " +
			"the task's popup terminal named after the action. The window stays open after the run.",
		Subject:     SubjectTask,
		Arity:       ArityOne,
		Params:      []Param{newParam("action", ParamText, "Task action to run").required().positional()},
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteOnByDefault,
		check:       nil,
	}, func(subject Subject, values Values) (RunTaskAction, error) {
		return RunTaskAction{Task: subject.Task, Action: values.String("action")}, nil
	}, func(out StartedTaskAction) string { return "Started " + out.Action + " for task " + out.ID })
)

type OpenTaskMenu struct{}

type OpenedTaskMenu struct{}

type NewTask struct {
	Harness   string `json:"harness"`
	Where     string `json:"where"`
	Branch    string `json:"branch"`
	Remote    string `json:"remote"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Project   string `json:"project"`
}

type StartedTask struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Number    int    `json:"number"`
	Directory string `json:"directory"`
	Branch    string `json:"branch"`
}

type OpenTab struct{}

type OpenedTab struct{}

type OpenTaskPopup struct{}

type ClosedTaskPopup struct {
	Task string `json:"task"`
}

type SleepTask struct {
	Task string `json:"task"`
}

type SleptTask struct {
	ID string `json:"id"`
}

type WakeTask struct {
	Task string `json:"task"`
}

type WokenTask struct {
	ID           string `json:"id"`
	AlreadyAwake bool   `json:"alreadyAwake"`
}

type FinishTask struct {
	Task    string `json:"task"`
	Discard bool   `json:"discard"`
}

type FinishedTask struct {
	ID string `json:"id"`
}

type RunTaskAction struct {
	Task   string `json:"task"`
	Action string `json:"action"`
}

type StartedTaskAction struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}
