//go:build linux || darwin

package session

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

const (
	switchVerifyAttempts  = 3
	switchVerifyDelay     = 20 * time.Millisecond
	parallelSwitchQueries = 2
)

type previousSession struct {
	Name string
	ID   string
}

func (w *Workflow) CreateSession(ctx context.Context, name, dir string) (gotmux.Session, error) {
	sess, err := w.createSession(ctx, name, dir)
	if err != nil {
		return gotmux.Session{}, w.fail(ctx, "create", err)
	}

	zerolog.Ctx(ctx).Info().
		Str("component", "session").
		Str("operation", "create").
		Str("status", "ok").
		Msg("session created")

	return sess, nil
}

func (w *Workflow) EnterOrCreateSession(ctx context.Context, name, dir string) (EnterOutcome, error) {
	out := EnterOutcome{
		SessionName: name,
		SessionID:   "",
		Action:      EnterNone,
		Created:     false,
		Started:     false,
		Client:      "",
		Hooks:       HookSummary{Attempted: 0, Completed: 0, Failed: 0, TimedOut: 0},
	}

	if err := validateSessionName(name); err != nil {
		return out, oops.In("session").Code("invalid_argument").Wrap(err)
	}

	if w.server == nil {
		return out, w.fail(ctx, "enter", oops.In("session").Code("endpoint_required").Wrap(errServerRequired))
	}

	if !w.insideTmux() {
		if err := validateTerminalStreams(w.terminalStreams()); err != nil {
			return out, w.fail(ctx, "enter", oops.In("session").Code("terminal_required").With("operation", "validate_terminals").Wrap(err))
		}
	}

	exists, err := w.sessionExists(ctx, name)
	if err != nil {
		return out, w.fail(ctx, "enter", err)
	}

	if !exists {
		if _, err := w.createSession(ctx, name, dir); err != nil {
			return out, w.fail(ctx, "enter", err)
		}

		out.Created = true
	}

	entered, enterErr := w.EnterSession(ctx, name)
	if entered.SessionName == "" {
		entered.SessionName = name
	}

	entered.Created = out.Created

	return entered, enterErr
}

func (w *Workflow) EnterSession(ctx context.Context, name string) (EnterOutcome, error) {
	if w.insideTmux() {
		return w.SwitchSessionOutcome(ctx, name)
	}

	return w.AttachSession(ctx, name)
}

func (w *Workflow) SwitchSession(ctx context.Context, name string) error {
	_, err := w.SwitchSessionOutcome(ctx, name)

	return err
}

func (w *Workflow) SwitchSessionOutcome(ctx context.Context, name string) (EnterOutcome, error) {
	return w.switchSessionOutcome(ctx, name, "", "")
}

func (w *Workflow) SwitchSessionOutcomeForClient(ctx context.Context, name, clientName, originSessionID string) (EnterOutcome, error) {
	if clientName == "" || originSessionID == "" {
		return EnterOutcome{}, oops.In("session").Code("client_unavailable").With("operation", "select_client").Wrap(ErrInvokingClientUnavailable)
	}

	return w.switchSessionOutcome(ctx, name, clientName, originSessionID)
}

func (w *Workflow) SwitchSessionOutcomeForClientTarget(ctx context.Context, expected Session, clientName, originSessionID string) (EnterOutcome, error) {
	if clientName == "" || originSessionID == "" {
		return EnterOutcome{}, oops.In("session").Code("client_unavailable").With("operation", "select_client").Wrap(ErrInvokingClientUnavailable)
	}

	out := newSessionSwitchOutcome(expected.Name)
	if err := validateLiveSwitchTarget(expected); err != nil {
		return out, w.fail(ctx, "switch", lookupSessionError(err))
	}

	if w.server == nil {
		return out, w.fail(ctx, "switch", oops.In("session").Code("endpoint_required").Wrap(errServerRequired))
	}

	target, err := w.server.SessionHandle(gotmux.SessionID(expected.ID))
	if err != nil {
		return out, w.fail(ctx, "switch", lookupSessionError(err))
	}

	var (
		info      gotmux.SessionInfo
		targetErr error
		client    gotmux.ClientInfo
		previous  previousSession
		clientErr error
		queries   sync.WaitGroup
	)

	queries.Add(parallelSwitchQueries)

	go func() {
		defer queries.Done()

		info, targetErr = target.Info(ctx)
	}()

	go func() {
		defer queries.Done()

		client, previous, clientErr = w.clientForSession(ctx, clientName, originSessionID)
	}()

	queries.Wait()

	if targetErr != nil {
		return out, w.fail(ctx, "switch", lookupSessionError(targetErr))
	}

	target = info.Handle()
	identity := target.ServerIdentity()

	socket := identity.ReportedSocket
	if socket == "" {
		socket = w.server.Endpoint().SocketPath
	}

	current := buildLiveSession(info, socket, identity, "")
	if current.RuntimeKey() != expected.RuntimeKey() {
		return out, w.fail(ctx, "switch", lookupSessionError(gotmux.ErrNotFound))
	}

	if clientErr != nil {
		return out, w.fail(ctx, "switch", clientErr)
	}

	return w.switchSessionOutcomeWithClient(ctx, out, target, client, previous)
}

func validateLiveSwitchTarget(expected Session) error {
	if expected.Kind != KindLive || expected.Origin != OriginLocal || expected.ID == "" || expected.Name == "" {
		return gotmux.ErrNotFound
	}

	return nil
}

func newSessionSwitchOutcome(name string) EnterOutcome {
	return EnterOutcome{
		SessionName: name,
		SessionID:   "",
		Action:      EnterSwitch,
		Created:     false,
		Started:     false,
		Client:      "",
		Hooks:       HookSummary{Attempted: 0, Completed: 0, Failed: 0, TimedOut: 0},
	}
}

func (w *Workflow) switchSessionOutcome(ctx context.Context, name, clientName, originSessionID string) (EnterOutcome, error) {
	out := newSessionSwitchOutcome(name)

	if w.server == nil {
		return out, w.fail(ctx, "switch", oops.In("session").Code("endpoint_required").Wrap(errServerRequired))
	}

	sess, err := w.server.FindSession(ctx, name)
	if err != nil {
		return out, w.fail(ctx, "switch", lookupSessionError(err))
	}

	return w.switchSessionOutcomeWithTarget(ctx, out, sess, clientName, originSessionID)
}

func (w *Workflow) switchSessionOutcomeWithTarget(
	ctx context.Context,
	out EnterOutcome,
	sess gotmux.Session,
	clientName, originSessionID string,
) (EnterOutcome, error) {
	var (
		client   gotmux.ClientInfo
		previous previousSession
		err      error
	)

	if clientName != "" && originSessionID != "" {
		client, previous, err = w.clientForSession(ctx, clientName, originSessionID)
	} else {
		client, previous, err = w.invokingClient(ctx)
	}

	if err != nil {
		return out, w.fail(ctx, "switch", err)
	}

	return w.switchSessionOutcomeWithClient(ctx, out, sess, client, previous)
}

func (w *Workflow) switchSessionOutcomeWithClient(
	ctx context.Context,
	out EnterOutcome,
	sess gotmux.Session,
	client gotmux.ClientInfo,
	previous previousSession,
) (EnterOutcome, error) {
	out.SessionID = string(sess.ID())
	out.Client = string(client.Name)

	event := HookEvent{
		Name:          EventSessionSwitchPre,
		Phase:         "pre",
		Action:        "switch",
		PrevSession:   previous.Name,
		Session:       out.SessionName,
		PrevSessionID: previous.ID,
		SessionID:     out.SessionID,
		Source:        w.source,
	}

	preHooks, err := w.runHookBoundary(ctx, event)
	if err != nil {
		return out, w.fail(ctx, "switch", err)
	}

	out.Hooks = preHooks

	if err := client.Handle().Switch(ctx, sess, gotmux.SwitchOptions{ToggleReadOnly: false, PreserveEnvironment: false}); err != nil {
		return out, w.fail(ctx, "switch", oops.In("session").Code("switch_failed").With("operation", "switch_client").Wrap(err))
	}

	out.Started = true

	if err := w.verifyClientSession(ctx, client.Name, sess.ID()); err != nil {
		return out, w.fail(ctx, "switch", err)
	}

	event.Name, event.Phase = EventSessionSwitchPost, "post"

	postHooks, postErr := w.runHookBoundary(ctx, event)
	out.Hooks = mergeHookSummaries(out.Hooks, postHooks)

	if postErr != nil {
		return out, w.fail(ctx, "switch", postErr)
	}

	if !w.deferSwitchHistory {
		if err := w.recordSwitch(ctx, out.SessionName); err != nil {
			return out, w.fail(ctx, "switch", oops.In("session").Code("history_failed").With("operation", "record_switch").Wrap(err))
		}
	}

	zerolog.Ctx(ctx).Info().
		Str("component", "session").
		Str("operation", "switch").
		Str("status", "ok").
		Msg("session switched")

	return out, nil
}

func (w *Workflow) clientForSession(ctx context.Context, clientName, originSessionID string) (gotmux.ClientInfo, previousSession, error) {
	clientHandle, err := w.server.ClientHandle(gotmux.ClientName(clientName))
	if err != nil {
		return gotmux.ClientInfo{}, previousSession{}, oops.In("session").Code("client_unavailable").With("operation", "select_client").Wrap(ErrInvokingClientUnavailable)
	}

	client, err := clientHandle.InfoWith(ctx, gotmux.QueryOptions{Filter: "", ExtraFields: []string{"session_name"}})
	if err != nil {
		if errors.Is(err, gotmux.ErrNotFound) || errors.Is(err, gotmux.ErrClientChanged) {
			return gotmux.ClientInfo{}, previousSession{}, oops.In("session").Code("client_unavailable").With("operation", "select_client").Wrap(ErrInvokingClientUnavailable)
		}

		return gotmux.ClientInfo{}, previousSession{}, oops.In("session").Code("tmux_failed").With("operation", "resolve_client").Wrap(err)
	}

	displayed, ok := client.SessionID.Get()
	if client.Control || !ok || string(displayed) != originSessionID {
		return gotmux.ClientInfo{}, previousSession{}, oops.In("session").Code("client_unavailable").With("operation", "select_client").Wrap(ErrInvokingClientUnavailable)
	}

	originName, ok := client.Raw("session_name")
	if !ok || len(originName) == 0 {
		return gotmux.ClientInfo{}, previousSession{}, oops.In("session").Code("invoking_context_failed").
			With("operation", "resolve_invoking_session").Wrap(gotmux.ErrProtocol)
	}

	return client, previousSession{Name: string(originName), ID: originSessionID}, nil
}

func (w *Workflow) createSession(ctx context.Context, name, dir string) (gotmux.Session, error) {
	if err := validateSessionName(name); err != nil {
		return gotmux.Session{}, oops.In("session").Code("invalid_argument").Wrap(err)
	}

	if w.server == nil {
		return gotmux.Session{}, oops.In("session").Code("endpoint_required").Wrap(errServerRequired)
	}

	exists, err := w.sessionExists(ctx, name)
	if err != nil {
		return gotmux.Session{}, err
	}

	if exists {
		return gotmux.Session{}, oops.In("session").Code("already_exists").Wrapf(ErrSessionAlreadyExists, "session %q already exists", name)
	}

	targetDir := dir
	if targetDir != "" {
		if abs, err := filepath.Abs(targetDir); err == nil {
			targetDir = abs
		}
	}

	sess, err := w.server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    name,
		Dir:     targetDir,
		Window:  "",
		Program: gotmux.Program{},
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 0, Height: 0},
		Start:   gotmux.StartPolicyExistingOnly,
		Group:   "",
	})
	if err != nil {
		return gotmux.Session{}, oops.In("session").Code("tmux_failed").With("operation", "create_session").Wrap(err)
	}

	return sess, nil
}

func (w *Workflow) sessionExists(ctx context.Context, name string) (bool, error) {
	if _, err := w.server.FindSession(ctx, name); err != nil {
		if isAbsentSession(err) {
			return false, nil
		}

		return false, lookupSessionError(err)
	}

	return true, nil
}

func (w *Workflow) insideTmux() bool {
	return w.getenv("TMUX") != ""
}

func (w *Workflow) terminalStreams() gotmux.Streams {
	return w.streams
}

func (w *Workflow) invokingClient(ctx context.Context) (gotmux.ClientInfo, previousSession, error) {
	env := gotmux.TmuxVars{TMUX: w.getenv("TMUX"), TMUXPane: w.getenv("TMUX_PANE")}

	if env.TMUX == "" {
		return gotmux.ClientInfo{}, previousSession{}, oops.In("session").Code("not_inside_tmux").Wrap(ErrNotInsideTmux)
	}

	info, err := w.server.CurrentFrom(ctx, env)
	if err != nil {
		return gotmux.ClientInfo{}, previousSession{}, oops.In("session").Code("invoking_context_failed").With("operation", "resolve_invoking_context").Wrap(err)
	}

	session, ok := info.Session.Get()
	if !ok || session.ID == "" {
		return gotmux.ClientInfo{}, previousSession{}, oops.In("session").Code("invoking_context_failed").With("operation", "resolve_invoking_session").Wrap(ErrInvokingClientUnavailable)
	}

	clients, err := w.server.Clients(ctx)
	if err != nil {
		return gotmux.ClientInfo{}, previousSession{}, oops.In("session").Code("tmux_failed").With("operation", "list_clients").Wrap(err)
	}

	candidates := clientsDisplaying(clients, session.ID)
	previous := previousSession{Name: session.Name, ID: string(session.ID)}

	switch len(candidates) {
	case 0:
		return gotmux.ClientInfo{}, previous, oops.In("session").Code("client_unavailable").With("operation", "select_client").Wrap(ErrInvokingClientUnavailable)
	case 1:
		return candidates[0], previous, nil
	default:
		return gotmux.ClientInfo{}, previous, oops.In("session").Code("client_ambiguous").With("operation", "select_client").Wrap(ErrInvokingClientAmbiguous)
	}
}

func clientsDisplaying(clients []gotmux.ClientInfo, session gotmux.SessionID) []gotmux.ClientInfo {
	candidates := make([]gotmux.ClientInfo, 0, 1)

	for _, client := range clients {
		if client.Control {
			continue
		}

		if displayed, ok := client.SessionID.Get(); ok && displayed == session {
			candidates = append(candidates, client)
		}
	}

	return candidates
}

func (w *Workflow) verifyClientSession(ctx context.Context, name gotmux.ClientName, target gotmux.SessionID) error {
	for attempt := range switchVerifyAttempts {
		if attempt > 0 {
			if err := sleepContext(ctx, switchVerifyDelay); err != nil {
				return oops.In("session").Code("operation_canceled").With("operation", "verify_switch").Wrap(err)
			}
		}

		clients, err := w.server.Clients(ctx)
		if err != nil {
			return oops.In("session").Code("tmux_failed").With("operation", "verify_switch").Wrap(err)
		}

		for _, client := range clients {
			if client.Name != name {
				continue
			}

			displayed, ok := client.SessionID.Get()
			if ok && displayed == target {
				return nil
			}
		}
	}

	return oops.In("session").Code("switch_unverified").With("operation", "verify_switch").Wrap(ErrSwitchUnverified)
}

func (w *Workflow) recordSwitch(ctx context.Context, name string) error {
	ident, ok := w.localIdentity(name)
	if !ok {
		return nil
	}

	_, err := UpdateState(ctx, w.historyPath, func(st *State) {
		st.RecordSwitch(ident)
	})

	return err
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return oops.In("session").Code("operation_canceled").With("operation", "wait").Wrap(ctx.Err())
	case <-timer.C:
		return nil
	}
}

func validateSessionName(name string) error {
	if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, ":.\\") || looksLikeSessionID(name) {
		return ErrInvalidSessionName
	}

	if strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return ErrInvalidSessionName
	}

	return nil
}

func looksLikeSessionID(name string) bool {
	digits, ok := strings.CutPrefix(name, "$")

	return ok && digits != "" && strings.Trim(digits, "0123456789") == ""
}
