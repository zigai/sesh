package action

// SessionMoveWindow moves a whole window, even when selected through an agent pane.
var SessionMoveWindow = define(Descriptor{
	ID:      "session.move-window",
	Title:   "Move agent window",
	Summary: "Move an agent's entire window to another live local session",
	Help: "Move the selected pane's entire window, preserving every pane and process. " +
		"Append after the destination's last window without selecting it. " +
		"Requires a destination session ID; linked windows and remote targets are refused. " +
		"Moving the last window removes the source session.",
	Subject: SubjectAgentPane,
	Arity:   ArityOne,
	Params: []Param{
		newParam("destination", ParamText, "Destination live session ID").required(),
	},
	NeedsClient: false,
	Interactive: false,
	Destructive: false,
	Remote:      RemoteNever,
	InPicker:    false,
	check:       nil,
}, func(subject Subject, values Values) (MoveWindow, error) {
	var destination SessionRef

	destination.Kind = "live"
	destination.ID = values.String("destination")

	return MoveWindow{
		Source:      RefOf(subject.Sessions[0]),
		Destination: destination,
		Window:      "",
		Pane:        subject.Pane,
	}, nil
}, func(out MovedWindow) string {
	return "Moved window " + out.Window + " to session " + out.Destination.Name
})

// MoveWindow uses stable tmux IDs, never names or window indexes. Pane or Window
// is required; when both are supplied they must identify the same window.
// Destination may be an ID-only live reference; full references also guard
// against changes since selection. Both references belong to this local server.
type MoveWindow struct {
	Source      SessionRef `json:"source"`
	Destination SessionRef `json:"destination"`
	Window      string     `json:"window"`
	Pane        string     `json:"pane"`
}

// MovedWindow reports the completed move. Moved remains true on a post-move
// history failure. Such failures must not be retried.
type MovedWindow struct {
	Source         SessionRef `json:"source"`
	Destination    SessionRef `json:"destination"`
	Window         string     `json:"window"`
	Index          int        `json:"index"`
	PaneIDs        []string   `json:"paneIds"`
	Moved          bool       `json:"moved"`
	HistoryUpdated bool       `json:"historyUpdated"`
}
