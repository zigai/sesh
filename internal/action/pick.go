package action

var PickPopup = define(Descriptor{
	ID:          "pick.popup",
	Title:       "Open picker",
	Summary:     "Open the session picker in a tmux popup",
	Help:        "Open the session picker in a tmux popup sized for the invoking client, then act on the choice made in it.",
	Subject:     SubjectNone,
	Arity:       ArityOne,
	Params:      nil,
	NeedsClient: true,
	Interactive: true,
	Destructive: false,
	Remote:      RemoteNever,
	check:       nil,
}, func(Subject, Values) (OpenPicker, error) { return OpenPicker{}, nil }, func(ClosedPicker) string { return "" })

type OpenPicker struct{}

type ClosedPicker struct{}
