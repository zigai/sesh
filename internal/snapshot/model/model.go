package model

const (
	Schema             = 3
	MaxManifestBytes   = 16 << 20
	MaxEntities        = 10000
	MaxArtifactBytes   = 256 << 20
	MaxGenerationBytes = 1 << 30

	RecoveryUnverified     Recovery = "unverified"
	RecoveryRestart        Recovery = "restart"
	RecoveryResume         Recovery = "resume"
	RecoveryNonrestartable Recovery = "nonrestartable"
)

type Recovery string

type Manifest struct {
	Format    string     `json:"format"`
	Version   int        `json:"version"`
	Started   int64      `json:"started"`
	Finished  int64      `json:"finished"`
	Source    Source     `json:"source"`
	Evidence  []Evidence `json:"evidence"`
	Sessions  []Session  `json:"sessions"`
	Groups    []Group    `json:"groups"`
	Windows   []Window   `json:"windows"`
	Links     []Link     `json:"links"`
	Panes     []Pane     `json:"panes"`
	Artifacts []Artifact `json:"artifacts"`
}

type Source struct {
	TmuxVersion    string     `json:"tmuxVersion"`
	LibraryVersion string     `json:"libraryVersion"`
	Consistency    string     `json:"consistency"`
	EndpointScope  string     `json:"endpointScope"`
	Options        []Option   `json:"options"`
	Environment    []Variable `json:"environment"`
}

type Evidence struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type Session struct {
	ID          string     `json:"id"`
	Name        Bytes      `json:"name"`
	Directory   Bytes      `json:"directory"`
	Group       string     `json:"group"`
	Options     []Option   `json:"options"`
	Environment []Variable `json:"environment"`
}

type Group struct {
	ID      string   `json:"id"`
	Name    Bytes    `json:"name"`
	Members []string `json:"members"`
}

type Window struct {
	ID      string   `json:"id"`
	Name    Bytes    `json:"name"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
	Layout  Layout   `json:"layout"`
	Zoomed  bool     `json:"zoomed"`
	Options []Option `json:"options"`
}

type Link struct {
	Session string `json:"session"`
	Window  string `json:"window"`
	Index   int    `json:"index"`
	Active  bool   `json:"active"`
	Last    bool   `json:"last"`
	Flags   Bytes  `json:"flags"`
}

type Layout struct {
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	Left     int      `json:"left"`
	Top      int      `json:"top"`
	Pane     string   `json:"pane"`
	Axis     string   `json:"axis"`
	Children []Layout `json:"children"`
}

type Pane struct {
	ID        string   `json:"id"`
	Window    string   `json:"window"`
	Index     int      `json:"index"`
	Title     Bytes    `json:"title"`
	Directory Bytes    `json:"directory"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	Left      int      `json:"left"`
	Top       int      `json:"top"`
	Active    bool     `json:"active"`
	Last      bool     `json:"last"`
	Dead      bool     `json:"dead"`
	Options   []Option `json:"options"`
	Terminal  Terminal `json:"terminal"`
	Launch    Launch   `json:"launch"`
}

type Option struct {
	Scope     string `json:"scope"`
	Name      Bytes  `json:"name"`
	Value     Bytes  `json:"value"`
	Present   bool   `json:"present"`
	Inherited bool   `json:"inherited"`
}

type Variable struct {
	Name   Bytes  `json:"name"`
	Value  Bytes  `json:"value"`
	State  string `json:"state"`
	Hidden bool   `json:"hidden"`
}

type Terminal struct {
	Artifact  string `json:"artifact"`
	History   int    `json:"history"`
	Alternate bool   `json:"alternate"`
	CursorX   int    `json:"cursorX"`
	CursorY   int    `json:"cursorY"`
	Mode      Bytes  `json:"mode"`
}

type Launch struct {
	Executable  Bytes      `json:"executable"`
	Arguments   []Bytes    `json:"arguments"`
	Directory   Bytes      `json:"directory"`
	Environment []Variable `json:"environment"`
	Birth       Bytes      `json:"birth"`
	Recovery    Recovery   `json:"recovery"`
	Reason      string     `json:"reason"`
}

type Artifact struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Length int64  `json:"length"`
	SHA256 string `json:"sha256"`
}

func (l Launch) Command() []string {
	command := make([]string, 0, len(l.Arguments))
	command = append(command, string(l.Executable))

	for _, argument := range l.Arguments[1:] {
		command = append(command, string(argument))
	}

	return command
}
