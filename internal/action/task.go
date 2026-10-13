package action

import "strconv"

var (
	TaskMenu = define(Descriptor{
		ID:      "task.menu",
		Title:   "New task menu",
		Summary: "Show the popup that starts a task or opens a tab",
		Help: "Show a popup on the invoking client that starts a task.\n\n" +
			"In a project session or a repository it picks whether the task works in a new worktree or " +
			"the pane's checkout, or any other checkout or branch from the expanded list, and which agent " +
			"runs there. Elsewhere it offers a plain tab or a loose task in the pane's directory.",
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
		Help: "Start a task from the invoking pane, in a window with the agent and the configured panes, " +
			"laid out by --layout or the first configured layout that fits the window. In a project " +
			"session, a repository, or the project --project names, --where picks the task's checkout: new (a worktree " +
			"on a new branch from --branch, by default the default branch, or origin's when the local one is behind), branch (a new worktree on " +
			"--branch, a local branch or one only --remote has), worktree (the existing worktree --dir) " +
			"or main (the main checkout). Sesh removes on finish only the worktree and branch it created. " +
			"Elsewhere the task runs in the pane's directory. --title names the task and its new branch and " +
			"is passed to the agent as configured by the harness's prompt_args.",
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
			newParam("layout", ParamText, "Named task layout for the window"),
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
			Project: values.String("project"), Layout: values.String("layout"),
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

	TaskRelayout = define(Descriptor{
		ID:      "task.relayout",
		Title:   "Relayout task window",
		Summary: "Lay the task window out again for its current size",
		Help: "Lay out the task window of the invoking pane again with --layout, or the first configured " +
			"layout that fits the window's current size. Panes the layout places that the window lacks are " +
			"opened again.",
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      []Param{newParam("layout", ParamText, "Named task layout to apply")},
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(_ Subject, values Values) (RelayoutTask, error) {
		return RelayoutTask{Layout: values.String("layout")}, nil
	}, func(RelaidOutTask) string { return "" })

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

	TaskOpen = define(Descriptor{
		ID:      "task.open",
		Title:   "Open task window",
		Summary: "Open again the window of a task that has none",
		Help: "Open the window of a task whose window was closed or lost, with its project's panes and layout: " +
			"a project task's in its project session, a loose task's next to the invoking window. A sleeping " +
			"task's agent resumes through its native resume command; any other task starts a new agent.",
		Subject:     SubjectTask,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(subject Subject, _ Values) (OpenTask, error) {
		return OpenTask{Task: subject.Task}, nil
	}, func(out OpenedTask) string { return "Opened task " + out.ID })

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
	Layout    string `json:"layout"`
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

type RelayoutTask struct {
	Layout string `json:"layout"`
}

type RelaidOutTask struct {
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

type OpenTask struct {
	Task string `json:"task"`
}

type OpenedTask struct {
	ID string `json:"id"`
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
