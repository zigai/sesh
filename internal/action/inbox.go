package action

var (
	InboxSnooze = defineInboxAction("inbox.snooze", "Snooze", "Hide an inbox row until a time or the agent's next message",
		"Move the selected inbox row to the snoozed section until --until passes or the agent finishes a turn or asks "+
			"for input, whichever comes first. --until is one of the configured inbox.snooze choices, a duration such as "+
			"90m, a time of day such as 18:00, \"tomorrow 09:00\" or \"next message\"; without it the picker offers the choices.",
		newParam("until", ParamText, "When the snooze ends"))

	InboxUnsnooze = defineInboxAction("inbox.unsnooze", "Unsnooze", "Return a snoozed inbox row to its section",
		"End the snooze of the selected inbox row.")

	InboxSeen = defineInboxAction("inbox.seen", "Mark seen", "Mark the agent's latest result as seen",
		"Record that the selected agent's latest result was seen, as visiting its pane does, so the row leaves done.")

	InboxUnseen = defineInboxAction("inbox.unseen", "Mark unseen", "Mark the agent's latest result as not seen",
		"Forget that the selected agent's latest result was seen, so the row returns to done.")

	InboxPR = defineInboxAction("inbox.pr", "Open PR", "Open the pull request of the row's branch",
		"Open the pull request of the selected row's branch in the browser with gh.")

	InboxEvidence = defineInboxAction("inbox.evidence", "Evidence", "List the evidence of the agent's final report",
		"List the screenshots, recordings, files and links in the Evidence section of the selected agent's latest "+
			"report. Choosing one opens it: a file or link with xdg-open, text in a pager.")
)

// InboxInput is the input of an inbox action: the picker applies it to the selected row.
type InboxInput struct {
	Until string `json:"until"`
}

type InboxOutput struct{}

func defineInboxAction(id ID, title, summary, help string, params ...Param) *Definition[InboxInput, InboxOutput] {
	return define(Descriptor{
		ID:          id,
		Title:       title,
		Summary:     summary,
		Help:        help,
		Subject:     SubjectAgentPane,
		Arity:       ArityOne,
		Params:      params,
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		InPicker:    true,
		check:       nil,
	}, func(_ Subject, values Values) (InboxInput, error) {
		return InboxInput{Until: values.String("until")}, nil
	}, func(InboxOutput) string { return "" })
}
