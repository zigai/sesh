package action

import (
	"errors"
	"strconv"
	"strings"
)

// ErrPRNumber rejects a pull request that is not a positive number.
var ErrPRNumber = errors.New("pull request must be a number such as 12 or #12")

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
		InPicker:    false,
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
		InPicker:    false,
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
		InPicker:    false,
		check:       nil,
	}, func(Subject, Values) (ClearWindowStatus, error) { return ClearWindowStatus{}, nil }, func(ClearedWindowStatus) string {
		return "Cleared agent window styles"
	})
)

// AgentMerge merges the branch an agent works on when it is not a sesh task.
var AgentMerge = define(Descriptor{
	ID:      "agent.merge",
	Title:   "Merge branch",
	Summary: "Rebase the agent's branch onto the default branch and push it there",
	Help: "Put the branch checked out in the agent's directory on origin's default branch without a merge " +
		"commit: refuse with uncommitted changes, fetch, rebase onto origin's default branch (a conflict aborts " +
		"the rebase), run the project's merge check, and push to origin's default branch as a fast-forward. " +
		"Nothing is removed afterwards.",
	Subject:     SubjectAgentPane,
	Arity:       ArityOne,
	Params:      nil,
	NeedsClient: false,
	Interactive: false,
	Destructive: true,
	Remote:      RemoteNever,
	InPicker:    false,
	check:       nil,
}, func(subject Subject, _ Values) (MergeAgentBranch, error) {
	return MergeAgentBranch{Session: RefOf(subject.Sessions[0]), Pane: subject.Pane}, nil
}, func(out MergedAgentBranch) string { return "Merged " + out.Branch + " into " + out.Base })

// AgentResume resumes a closed agent conversation that the inbox lists.
var AgentResume = define(Descriptor{
	ID:      "agent.resume",
	Title:   "Resume agent",
	Summary: "Resume a closed agent conversation in a new window",
	Help: "Open a window that runs the native resume command of a recorded agent conversation in its directory: " +
		"in its project's session, else in the invoking pane's session, and reopen it if it finished. The agent " +
		"status service records the conversations of interactive agents in tmux panes.",
	Subject:     SubjectNone,
	Arity:       ArityOne,
	Params:      []Param{newParam("item", ParamText, "Recorded conversation to resume").required().positional()},
	NeedsClient: false,
	Interactive: false,
	Destructive: false,
	Remote:      RemoteOnByDefault,
	InPicker:    false,
	check:       nil,
}, func(_ Subject, values Values) (ResumeAgent, error) {
	return ResumeAgent{Item: values.String("item")}, nil
}, func(out ResumedAgent) string { return "Resumed " + out.Item })

var (
	AgentFinish = defineWorkAction("agent.finish", "Finished", "Finish conversation", "Mark a recorded conversation finished",
		"Finish a recorded agent conversation: the inbox stops listing it once its agent exits. Resuming it "+
			"reopens it. A conversation also finishes when every pull request linked to it is merged or closed.")

	AgentPin = defineWorkAction("agent.pin", "Pinned", "Pin conversation", "Keep a recorded conversation first and open",
		"Pin a recorded agent conversation: the inbox lists it first in its section, and its pull requests "+
			"ending does not finish it.")

	AgentUnpin = defineWorkAction("agent.unpin", "Unpinned", "Unpin conversation", "Unpin a recorded conversation",
		"Unpin a recorded agent conversation.")

	AgentLink = defineWorkAction("agent.link", "Linked a pull request to", "Link PR", "Link a pull request to a recorded conversation",
		"Link a pull request of the conversation's repository to it, as gh reports it. The conversation finishes "+
			"when every linked pull request is merged or closed.",
		newParam("pr", ParamText, "Pull request number").required().positional())

	AgentUnlink = defineWorkAction("agent.unlink", "Unlinked a pull request from", "Unlink PR", "Unlink a pull request from a recorded conversation",
		"Unlink a pull request from a recorded conversation. Sync does not link it again.",
		newParam("pr", ParamText, "Pull request number").required().positional())
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

type MergeAgentBranch struct {
	Session SessionRef `json:"session"`
	Pane    string     `json:"pane"`
}

type MergedAgentBranch struct {
	Branch string `json:"branch"`
	Base   string `json:"base"`
}

type ResumeAgent struct {
	Item string `json:"item"`
}

// ChangeWork names a recorded conversation and, for linking, a pull request number.
type ChangeWork struct {
	Item string `json:"item"`
	PR   int    `json:"pr"`
}

type ChangedWork struct {
	Item string `json:"item"`
}

// ResumedAgent is the window a resumed conversation runs in.
type ResumedAgent struct {
	Item    string     `json:"item"`
	Session SessionRef `json:"session"`
	Pane    string     `json:"pane"`
}

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
		InPicker:    false,
		check:       nil,
	}, func(_ Subject, values Values) (NavigateAgents, error) {
		return NavigateAgents{Local: values.Bool("local")}, nil
	}, reportFocusedPane)
}

func defineWorkAction(id ID, done, title, summary, help string, params ...Param) *Definition[ChangeWork, ChangedWork] {
	return define(Descriptor{
		ID:          id,
		Title:       title,
		Summary:     summary,
		Help:        help,
		Subject:     SubjectNone,
		Arity:       ArityOne,
		Params:      append([]Param{newParam("item", ParamText, "Recorded conversation").required().positional()}, params...),
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteOnByDefault,
		InPicker:    false,
		check:       nil,
	}, func(_ Subject, values Values) (ChangeWork, error) {
		pr := 0

		if text := values.String("pr"); text != "" {
			number, err := strconv.Atoi(strings.TrimPrefix(text, "#"))
			if err != nil || number <= 0 {
				return ChangeWork{Item: "", PR: 0}, ErrPRNumber
			}

			pr = number
		}

		return ChangeWork{Item: values.String("item"), PR: pr}, nil
	}, func(out ChangedWork) string { return done + " " + out.Item })
}

// reportFocusedPane is silent: navigation runs from tmux key bindings, where any output would
// cover the pane it just focused.
func reportFocusedPane(FocusedPane) string {
	return ""
}
