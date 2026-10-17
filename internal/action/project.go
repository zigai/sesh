package action

var (
	ProjectOpen = define(Descriptor{
		ID:      "project.open",
		Title:   "Open project",
		Summary: "Switch to a project's session, creating it when missing",
		Help: "Switch the invoking client to the session of the project that owns the directory, creating " +
			"the session with a main window in the project root when it has none. With --adopt, a session " +
			"without a project that already has the project's name becomes the project's session.",
		Subject: SubjectNone,
		Arity:   ArityOne,
		Params: []Param{
			newParam("dir", ParamPath, "Directory inside the project").required().positional(),
			newParam("adopt", ParamBool, "Adopt the session that already has the project's name"),
		},
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		InPicker:    false,
		check:       nil,
	}, func(_ Subject, values Values) (OpenProject, error) {
		return OpenProject{Directory: values.String("dir"), Adopt: values.Bool("adopt")}, nil
	}, reportOpenedProject)

	ProjectClone = define(Descriptor{
		ID:          "project.clone",
		Title:       "Clone project",
		Summary:     "Clone a repository into the first project root and open it",
		Help:        "Clone a repository into the first configured project root and switch to its new session.",
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      []Param{newParam("url", ParamText, "Repository URL").required().positional().fromQuery()},
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		InPicker:    false,
		check:       nil,
	}, func(_ Subject, values Values) (CloneProject, error) {
		return CloneProject{URL: values.String("url")}, nil
	}, reportOpenedProject)

	ProjectCreate = define(Descriptor{
		ID:          "project.create",
		Title:       "Create project",
		Summary:     "Create a repository in the first project root and open it",
		Help:        "Create a directory with an empty git repository in the first configured project root and switch to its new session.",
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      []Param{newParam("name", ParamText, "Project directory name").required().positional().fromQuery()},
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		InPicker:    false,
		check:       nil,
	}, func(_ Subject, values Values) (CreateProject, error) {
		return CreateProject{Name: values.String("name")}, nil
	}, reportOpenedProject)

	ProjectPick = define(Descriptor{
		ID:          "project.pick",
		Title:       "Open project picker",
		Summary:     "Pick a project to open in a tmux popup",
		Help:        "Open the project picker in a tmux popup sized for the invoking client, then open the chosen project.",
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: true,
		Interactive: true,
		Destructive: false,
		Remote:      RemoteNever,
		InPicker:    false,
		check:       nil,
	}, func(Subject, Values) (PickProject, error) { return PickProject{}, nil }, func(PickedProject) string { return "" })
)

type OpenProject struct {
	Directory string `json:"directory"`
	Adopt     bool   `json:"adopt"`
}

type CloneProject struct {
	URL string `json:"url"`
}

type CreateProject struct {
	Name string `json:"name"`
}

type OpenedProject struct {
	Root    string `json:"root"`
	Session string `json:"session"`
	Created bool   `json:"created"`
	Adopted bool   `json:"adopted"`
}

type PickProject struct{}

type PickedProject struct{}

func reportOpenedProject(out OpenedProject) string {
	switch {
	case out.Adopted:
		return "Adopted session " + out.Session + " for " + out.Root
	case out.Created:
		return "Created session " + out.Session + " for " + out.Root
	default:
		return "Switched to session " + out.Session
	}
}
