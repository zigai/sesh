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

	InboxFinish = defineInboxAction("inbox.finish", "Finish", "Finish the row's task or conversation",
		"Finish the selected row: a task is finished as task.finish does, and any other recorded conversation "+
			"as agent.finish does.")

	InboxTask = defineInboxAction("inbox.task", "New task", "Open the new task menu for the row's project",
		"Close the picker and open the new task menu, as task.menu does, for the project of the selected row "+
			"instead of the invoking pane's.")

	InboxPin = defineInboxAction("inbox.pin", "Pin", "Pin the row's conversation",
		"Pin the selected row's recorded conversation as agent.pin does.")

	InboxUnpin = defineInboxAction("inbox.unpin", "Unpin", "Unpin the row's conversation",
		"Unpin the selected row's recorded conversation.")

	InboxLink = defineInboxAction("inbox.link", "Link PR", "Link a pull request to the row's conversation",
		"Link a pull request to the selected row's recorded conversation as agent.link does.",
		newParam("pr", ParamText, "Pull request number").required())

	InboxUnlink = defineInboxAction("inbox.unlink", "Unlink PR", "Unlink a pull request from the row's conversation",
		"Unlink a pull request from the selected row's recorded conversation; without --pr the picker offers "+
			"the linked ones.",
		newParam("pr", ParamText, "Pull request number"))
)

type InboxInput struct {
	Until string `json:"until"`
	PR    string `json:"pr"`
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
		return InboxInput{Until: values.String("until"), PR: values.String("pr")}, nil
	}, func(InboxOutput) string { return "" })
}
