package tui

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

var (
	errActionsUnavailable = errors.New("no action dispatcher configured")
	errUnknownAction      = errors.New("unknown action")
	errEmptyParam         = errors.New("value cannot be empty")
)

var actionOperations = map[action.ID]tuiLogOperation{
	action.SessionCreate.ID:   tuiOperationCreateSession,
	action.SessionRename.ID:   tuiOperationRenameSession,
	action.SessionKill.ID:     tuiOperationKillSession,
	action.SessionDelete.ID:   tuiOperationDeleteSession,
	action.SnapshotSave.ID:    tuiOperationSnapshotSession,
	action.SnapshotRestore.ID: tuiOperationRestoreSavedSession,
	action.SessionSwitch.ID:   tuiOperationEnterLiveSession,
}

// pendingAction is an action the picker is collecting parameters or confirmation for.
type pendingAction struct {
	entry   action.Entry
	subject action.Subject
	values  action.Values
	param   action.Param
}

type actionResultMsg struct {
	request pendingAction
	output  any
	err     error
}

func actionOperation(id action.ID) tuiLogOperation {
	if operation, ok := actionOperations[id]; ok {
		return operation
	}

	return tuiOperationRunAction
}

// invokeAction starts an action on the selection: it checks that the action applies, asks for
// the parameters a person supplies, and asks for confirmation when the action's policy does.
// Values are parameters the invoking key binding supplies.
func (m *Model) invokeAction(id action.ID, values action.Values) func() (tea.Model, tea.Cmd) {
	entry, ok := action.Lookup(id)
	if !ok {
		m.setErrorStatusf("%s: %v", id, errUnknownAction)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	descriptor := entry.Describe()

	subject, ok := m.actionSubject(descriptor)
	if !ok {
		return nil
	}

	if err := m.actionAvailable(descriptor, subject); err != nil {
		m.setErrorStatusf("%s unavailable: %v", descriptor.Title, err)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	request := pendingAction{entry: entry, subject: subject, values: maps.Clone(values), param: emptyParam()}
	if request.values == nil {
		request.values = action.Values{}
	}

	if query := strings.TrimSpace(m.textInput.Value()); query != "" {
		for _, param := range descriptor.Params {
			if param.FromQuery && !request.values.Has(param.Name) {
				request.values.Set(param.Name, query)
			}
		}
	}

	return m.continueAction(request)
}

// runAction runs an action on a subject the caller chose, without prompts or confirmation.
func (m *Model) runAction(definition action.Entry, subject action.Subject, values action.Values) func() (tea.Model, tea.Cmd) {
	descriptor := definition.Describe()
	if err := m.actionAvailable(descriptor, subject); err != nil {
		m.setErrorStatusf("%s unavailable: %v", descriptor.Title, err)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	return m.dispatchAction(pendingAction{entry: definition, subject: subject, values: values, param: emptyParam()})
}

func (m *Model) actionAvailable(descriptor action.Descriptor, subject action.Subject) error {
	if m.deps.Actions == nil {
		return errActionsUnavailable
	}

	if err := descriptor.Available(action.Env{Accepts: m.deps.Actions.Accepts}, subject); err != nil {
		return oops.In("tui").With("action", string(descriptor.ID)).Wrap(err)
	}

	return nil
}

// actionSubject selects what an action applies to: the selected row's session, and for pane
// actions the selected agent's pane.
func (m *Model) actionSubject(descriptor action.Descriptor) (action.Subject, bool) {
	if descriptor.Subject == action.SubjectNone {
		return action.Subject{At: action.Local(), Sessions: nil, Pane: "", Task: ""}, true
	}

	if m.cursor >= len(m.filtered) {
		return action.Subject{At: action.Local(), Sessions: nil, Pane: "", Task: ""}, false
	}

	selected := m.filtered[m.cursor]

	if m.displayMode == displayModeWork {
		found, ok := m.workTaskAt(m.selectedListRow())
		if ok && descriptor.Subject == action.SubjectTask {
			return action.Subject{At: action.LocationOf(selected), Sessions: nil, Pane: string(found.live.AgentPane), Task: found.live.Record.ID}, true
		}

		if !ok || found.owner.ID == "" || found.live.AgentPane == "" {
			return action.Subject{At: action.Local(), Sessions: nil, Pane: "", Task: ""}, false
		}

		subject := action.SubjectOf(found.owner)
		subject.Pane = string(found.live.AgentPane)

		return subject, true
	}

	subject := action.SubjectOf(selected)

	if row := m.selectedListRow(); row.isAgent() && row.agentIndex < len(selected.Agents) {
		subject.Pane = selected.Agents[row.agentIndex].PaneID
	}

	return subject, true
}

func (m *Model) continueAction(request pendingAction) func() (tea.Model, tea.Cmd) {
	descriptor := request.entry.Describe()

	for _, param := range descriptor.Params {
		if prompted(param) && !request.values.Has(param.Name) {
			return m.promptParam(request, param)
		}
	}

	if action.PolicyFor(m.config, descriptor).Confirm {
		m.pending = &request
		m.confirmYes = false
		m.dialog = dialogConfirm

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	return m.dispatchAction(request)
}

// prompted reports whether the picker asks for a parameter: required text, and values the
// picker can suggest.
func prompted(param action.Param) bool {
	if param.Kind != action.ParamText && param.Kind != action.ParamPath {
		return false
	}

	return param.Required || param.Suggest != action.SuggestNone
}

func (m *Model) promptParam(request pendingAction, param action.Param) func() (tea.Model, tea.Cmd) {
	request.param = param

	if param.Kind != action.ParamPath {
		m.pending = &request
		m.dialog = dialogInputText
		m.textInput.Blur()
		m.textParamInput.SetValue(param.Default)
		m.textParamInput.CursorEnd()

		return func() (tea.Model, tea.Cmd) { return m, m.textParamInput.Focus() }
	}

	suggested, err := m.suggestedPath(request, param)
	if err != nil {
		logTUIFailure(m.operationContext(), tuiOperationResolveWorkingDirectory, err)
		m.setErrorStatusf("getting current directory: %v", err)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	m.pending = &request
	m.dialog = dialogInputPath
	m.textInput.Blur()
	m.setPathInputValue(suggested)
	m.refreshPathSuggestions()

	return func() (tea.Model, tea.Cmd) { return m, m.pathInput.Focus() }
}

func (m *Model) suggestedPath(request pendingAction, param action.Param) (string, error) {
	if param.Suggest != action.SuggestSessionDirectory {
		return param.Default, nil
	}

	cwd, err := m.deps.WorkingDirectory.GetWorkingDirectory()
	if err != nil {
		return "", oops.In("tui").With("operation", "suggest_path").Wrap(err)
	}

	return session.DefaultCreateSessionDirectory(cwd, request.values.String("name"), m.config.Settings.DefaultSessionPathSuggestion), nil
}

// submitParam records the prompted value and moves on to the next prompt, the confirmation or
// the action itself.
func (m *Model) submitParam(value string) func() (tea.Model, tea.Cmd) {
	if m.pending == nil {
		m.closeActionDialog()

		return func() (tea.Model, tea.Cmd) { return m, m.textInput.Focus() }
	}

	request := *m.pending
	param := request.param

	value = strings.TrimSpace(value)
	if value == "" && param.Required {
		m.setErrorStatusf("%s: %s: %v", request.entry.Describe().Title, param.Usage, errEmptyParam)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	request.values.Set(param.Name, value)
	request.param = emptyParam()

	m.closeActionDialog()
	m.clearStatus()

	next := m.continueAction(request)

	return func() (tea.Model, tea.Cmd) {
		model, cmd := next()
		if m.dialog != dialogNone {
			return model, cmd
		}

		return model, tea.Batch(m.textInput.Focus(), cmd)
	}
}

func (m *Model) confirmPending() func() (tea.Model, tea.Cmd) {
	request := m.pending
	m.closeActionDialog()

	if request == nil {
		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	return m.dispatchAction(*request)
}

func (m *Model) cancelAction() func() (tea.Model, tea.Cmd) {
	m.closeActionDialog()

	return func() (tea.Model, tea.Cmd) { return m, m.textInput.Focus() }
}

func (m *Model) closeActionDialog() {
	m.dialog = dialogNone
	m.pending = nil
	m.textParamInput.SetValue("")
	m.textParamInput.Blur()
	m.pathInput.Blur()
	m.pathSuggestions = nil
	m.pathSuggestionIndex = 0
	m.pathSuggestionTop = 0
}

func (m *Model) dispatchAction(request pendingAction) func() (tea.Model, tea.Cmd) {
	descriptor := request.entry.Describe()

	input, err := request.entry.BindAny(request.subject, request.values)
	if err != nil {
		logTUIFailure(m.operationContext(), actionOperation(descriptor.ID), err)
		m.setErrorStatusf("%s failed: %v", descriptor.Title, err)

		return func() (tea.Model, tea.Cmd) { return m, nil }
	}

	m.clearStatus()

	ctx := m.operationContext()
	invocation := action.Invocation{
		Action: descriptor.ID,
		At:     request.subject.At,
		Caller: action.Caller{Client: "", Pane: "", Session: "", Width: "", Height: ""},
		Input:  input,
	}

	return func() (tea.Model, tea.Cmd) {
		return m, func() tea.Msg {
			if !m.lifecycle.BeginWorker() {
				return nil
			}
			defer m.lifecycle.EndWorker()

			output, err := action.RunAny(ctx, m.deps.Actions, invocation)

			return actionResultMsg{request: request, output: output, err: err}
		}
	}
}

func (m *Model) handleActionResult(msg actionResultMsg) (tea.Model, tea.Cmd) {
	descriptor := msg.request.entry.Describe()
	operation := actionOperation(descriptor.ID)
	ctx := m.operationContext()

	if msg.err != nil {
		if cmd, recovered := m.recoverActionFailure(msg); recovered {
			return m, cmd
		}

		if !outcomeLogged(msg.err) {
			logTUIFailure(ctx, operation, msg.err)
		}

		code := action.CodeOf(msg.err)
		if code == action.CodePartial {
			m.statusMessage = fmt.Sprintf("%s incomplete: %v", descriptor.Title, msg.err)
			m.statusLevel = statusDegraded

			return m, m.refreshSessions()
		}

		m.setErrorStatusf("%s failed: %v", descriptor.Title, msg.err)

		if code == action.CodeUncertain {
			return m, m.refreshSessions()
		}

		return m, nil
	}

	logTUIEvent(ctx, operation, tuiLogOutcomeSuccess)
	m.clearStatus()

	return m, m.followAction(msg)
}

// recoverActionFailure turns failures the picker can resolve into the outcome the person
// wanted: creating a session that already exists enters it.
func (m *Model) recoverActionFailure(msg actionResultMsg) (tea.Cmd, bool) {
	if msg.request.entry.Describe().ID != action.SessionCreate.ID || action.CodeOf(msg.err) != action.CodeAlreadyExists {
		return nil, false
	}

	name := msg.request.values.String("name")
	for _, row := range m.sessions {
		if !row.Saved() && row.Name == name && action.LocationOf(row) == msg.request.subject.At {
			logTUIEvent(m.operationContext(), tuiOperationCreateSession, tuiLogOutcomeSuccess)

			return m.runLiveSessionAction(row), true
		}
	}

	return nil, false
}

// followAction applies what a completed action means for the picker: entering a session that
// was created or restored, and keeping history and selection in step with renames and deletes.
func (m *Model) followAction(msg actionResultMsg) tea.Cmd {
	at := msg.request.subject.At

	switch output := msg.output.(type) {
	case action.CreatedSession:
		if at.Kind == action.LocationLocal {
			return m.runLiveSessionAction(output.Session.Session(at))
		}
	case action.SwitchedSession:
		if at.Kind == action.LocationOrigin {
			m.originDetachAfterQuit = true

			return tea.Quit
		}
	case action.RestoredSnapshot:
		if at.Kind == action.LocationLocal && len(output.Sessions) == 1 {
			return m.runLiveSessionAction(output.Sessions[0].Session(at))
		}
	case action.RenamedSession:
		previous := msg.request.subject.Sessions[0].SemanticIdentity()
		renamed := previous
		renamed.Name = output.Name

		m.recordActionState(func(current *session.State) { current.RenameSessionIdentity(previous, renamed) })
	case action.DeletedSession:
		deleted := msg.request.subject.Sessions[0].SemanticIdentity()

		m.recordActionState(func(current *session.State) { current.DeleteSessionIdentity(deleted) })
		m.moveCursorToPreviousRow()
	}

	return m.refreshSessions()
}

func (m *Model) recordActionState(update func(*session.State)) {
	if err := m.updateState(m.operationContext(), update); err != nil {
		logTUIFailure(m.operationContext(), tuiOperationSaveState, err)
		m.setErrorStatusf("Failed to save state: %v", err)
	}
}

func outcomeLogged(err error) bool {
	owner, ok := errors.AsType[interface {
		error
		OutcomeLogged() bool
	}](err)

	return ok && owner.OutcomeLogged()
}

func emptyParam() action.Param {
	var param action.Param

	return param
}
