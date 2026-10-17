package action

var PickPopup = define(Descriptor{
	ID:          "pick.popup",
	Title:       "Open picker",
	Summary:     "Open the session picker in a tmux popup",
	Help:        "Open the session picker in a tmux popup sized for the invoking client, then act on the choice made in it.",
	Subject:     SubjectNone,
	Arity:       ArityOne,
	Params:      []Param{newParam("mode", ParamChoice, "Display mode to open the picker in").withChoices("sesh", "agents", "inbox")},
	NeedsClient: true,
	Interactive: true,
	Destructive: false,
	Remote:      RemoteNever,
	InPicker:    false,
	check:       nil,
}, func(_ Subject, values Values) (OpenPicker, error) {
	return OpenPicker{Mode: values.String("mode")}, nil
}, func(ClosedPicker) string { return "" })

type OpenPicker struct {
	Mode string `json:"mode"`
}

type ClosedPicker struct{}
