package action

var (
	AgentNext = defineAgentNavigation("agent.next", "Next agent", "Switch to the next coding agent needing attention",
		"Switch to the next coding agent pane needing attention (waiting, failed, or idle) and activate its window, session, and pane.")

	AgentLatest = defineAgentNavigation("agent.latest", "Latest agent", "Focus the coding agent that most recently became idle",
		"Focus the live coding agent pane with the most recent recorded transition to idle. Idle can include startup and does not confirm completed work.")

	AgentWaiting = defineAgentNavigation("agent.waiting", "Waiting agent", "Focus the coding agent waiting longest for input or approval",
		"Focus the live coding agent with the oldest recorded transition to waiting.")

	AgentUnread = defineAgentNavigation("agent.unread", "Unread agent", "Focus the most recently idle coding agent with an unvisited result",
		"Focus the newest idle transition not acknowledged by sesh-recorded visits. The current pane is observed as visited. "+
			"Repeated invocations advance through unread results. Arbitrary manual pane visits are not tracked.")

	AgentFailed = defineAgentNavigation("agent.failed", "Failed agent", "Focus the most recently failed coding agent",
		"Focus the live coding agent with the most recent recorded transition to failed.")

	AgentToggle = defineAgentNavigation("agent.toggle", "Toggle agent", "Alternate between your working pane and the last agent visited",
		"Alternate between the working pane and the last agent visited through sesh navigation, independently of agent back "+
			"history. A manually selected working pane replaces the remembered working pane. Requires an earlier agent navigation for this client.")

	AgentBack = define(Descriptor{
		ID:          "agent.back",
		Title:       "Agent back",
		Summary:     "Return through agent navigation history",
		Help:        "Return through the agent navigation history stack to the previous pane.",
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(Subject, Values) (ReturnToAgent, error) { return ReturnToAgent{}, nil }, reportFocusedPane)

	AgentFocus = define(Descriptor{
		ID:          "agent.focus",
		Title:       "Focus agent",
		Summary:     "Focus one coding agent pane",
		Help:        "Focus a pane, activating its window and session for the invoking client.",
		Subject:     SubjectAgentPane,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(subject Subject, _ Values) (FocusAgent, error) {
		return FocusAgent{Session: RefOf(subject.Sessions[0]), Pane: subject.Pane}, nil
	}, reportFocusedPane)

	AgentWindowStatusClear = define(Descriptor{
		ID:          "agent.window-status-clear",
		Title:       "Clear agent window styles",
		Summary:     "Clear window status styles applied by sesh",
		Help:        "Restore every window status style sesh applied, leaving styles owned by other tools untouched.",
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(Subject, Values) (ClearWindowStatus, error) { return ClearWindowStatus{}, nil }, func(ClearedWindowStatus) string {
		return "Cleared agent window styles"
	})
)

type NavigateAgents struct {
	Local bool `json:"local"`
}

type ReturnToAgent struct{}

type FocusAgent struct {
	Session SessionRef `json:"session"`
	Pane    string     `json:"pane"`
}

// FocusedPane is the pane navigation activated.
type FocusedPane struct {
	Pane      string `json:"pane"`
	SessionID string `json:"sessionId"`
	Session   string `json:"session"`
}

type ClearWindowStatus struct{}

type ClearedWindowStatus struct{}

func defineAgentNavigation(id ID, title, summary, help string) *Definition[NavigateAgents, FocusedPane] {
	return define(Descriptor{
		ID:          id,
		Title:       title,
		Summary:     summary,
		Help:        help,
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      []Param{newParam("local", ParamBool, "Only consider panes in the current tmux session")},
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		check:       nil,
	}, func(_ Subject, values Values) (NavigateAgents, error) {
		return NavigateAgents{Local: values.Bool("local")}, nil
	}, reportFocusedPane)
}

// reportFocusedPane is silent: navigation runs from tmux key bindings, where any output would
// cover the pane it just focused.
func reportFocusedPane(FocusedPane) string {
	return ""
}
