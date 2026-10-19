package action

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/session"
)

const (
	SubjectNone SubjectKind = iota
	SubjectLiveSession
	SubjectSavedSession
	SubjectSession
	SubjectAgentPane
	SubjectTask
)

const (
	ArityOne Arity = iota
	ArityMany
	ArityOptional
)

const (
	RemoteNever RemoteAccess = iota
	RemoteOffByDefault
	RemoteOnByDefault
)

const (
	LocationLocal LocationKind = iota
	LocationPeer
	LocationOrigin
)

const idSegments = 2

var (
	ErrUnknownAction        = errors.New("unknown action")
	ErrSubjectMismatch      = errors.New("action does not apply to this target")
	ErrLocationRejected     = errors.New("action cannot run at this location")
	ErrClientRequired       = errors.New("action needs an invoking tmux client")
	ErrOutputTypeMismatch   = errors.New("action returned an unexpected output type")
	ErrSessionAttached      = errors.New("session is attached")
	ErrWholeSessionRequired = errors.New("action applies to a whole session, not an agent pane")
	ErrTaskRequired         = errors.New("action applies to a task")

	registry = make(map[ID]Entry)
	order    []ID
)

type (
	ID           string
	SubjectKind  uint8
	Arity        uint8
	RemoteAccess uint8
	LocationKind uint8
)

type Location struct {
	Kind       LocationKind
	Connection string
}

type Caller struct {
	Client  string
	Pane    string
	Session string
	Width   string
	Height  string
}

type Subject struct {
	At       Location
	Sessions []session.Session
	Pane     string
	Task     string
}

type Env struct {
	Accepts func(Location, ID) bool
}

type Descriptor struct {
	ID          ID
	Title       string
	Summary     string
	Help        string
	Subject     SubjectKind
	Arity       Arity
	Params      []Param
	NeedsClient bool
	Interactive bool
	Destructive bool
	Remote      RemoteAccess
	// InPicker actions change only what the picker shows, so the picker runs them itself and only
	// picker keys can bind them.
	InPicker bool
	check    func(Subject) error
}

type Entry interface {
	Describe() Descriptor
	BindAny(subject Subject, values Values) (any, error)
	ReportAny(output any) string
	DecodeInput(data []byte) (any, error)
	EncodeInput(input any) ([]byte, error)
	DecodeOutput(data []byte) (any, error)
	EncodeOutput(output any) ([]byte, error)
}

type Invocation struct {
	Action ID
	At     Location
	Caller Caller
	Input  any
}

type Dispatcher interface {
	Accepts(at Location, id ID) bool
	Dispatch(ctx context.Context, invocation Invocation) (any, error)
}

type Dispatchers []Dispatcher

type Router struct {
	Local  Dispatcher
	Peer   Dispatcher
	Origin Dispatcher
}

func (id ID) Group() string {
	group, _, _ := strings.Cut(string(id), ".")

	return group
}

func (id ID) Verb() string {
	_, verb, _ := strings.Cut(string(id), ".")

	return verb
}

func (id ID) Valid() bool {
	parts := strings.Split(string(id), ".")
	if len(parts) != idSegments {
		return false
	}

	return validSegment(parts[0]) && validSegment(parts[1])
}

func (kind LocationKind) String() string {
	switch kind {
	case LocationLocal:
		return "local"
	case LocationPeer:
		return "peer"
	case LocationOrigin:
		return "origin"
	default:
		return "unknown"
	}
}

func (descriptor Descriptor) Param(name string) (Param, bool) {
	for _, param := range descriptor.Params {
		if param.Name == name {
			return param, true
		}
	}

	var missing Param

	return missing, false
}

// Available reads only the subject and the dispatcher's capabilities, so menus may call it on
// every render; Run validates again.
func (descriptor Descriptor) Available(env Env, subject Subject) error {
	if err := descriptor.matchSubject(subject); err != nil {
		return err
	}

	if env.Accepts != nil && !env.Accepts(subject.At, descriptor.ID) {
		return Wrap(CodeUnsupported, ErrLocationRejected)
	}

	if descriptor.check != nil {
		return descriptor.check(subject)
	}

	return nil
}

func (descriptor Descriptor) matchSubject(subject Subject) error {
	switch descriptor.Subject {
	case SubjectNone:
		if len(subject.Sessions) != 0 {
			return Wrap(CodeInvalidArgument, ErrSubjectMismatch)
		}

		return nil
	case SubjectAgentPane:
		if subject.Pane == "" || len(subject.Sessions) != 1 {
			return Wrap(CodeInvalidArgument, ErrSubjectMismatch)
		}

		return nil
	case SubjectTask:
		if subject.Task == "" {
			return Wrap(CodeInvalidArgument, ErrTaskRequired)
		}

		return nil
	case SubjectLiveSession, SubjectSavedSession, SubjectSession:
		return descriptor.matchSessions(subject.Sessions)
	default:
		return Wrap(CodeInvalidArgument, ErrSubjectMismatch)
	}
}

func (descriptor Descriptor) matchSessions(sessions []session.Session) error {
	switch {
	case descriptor.Arity == ArityOne && len(sessions) != 1,
		descriptor.Arity == ArityMany && len(sessions) == 0:
		return Wrap(CodeInvalidArgument, ErrSubjectMismatch)
	}

	for _, candidate := range sessions {
		saved := candidate.Saved()
		if (descriptor.Subject == SubjectLiveSession && saved) || (descriptor.Subject == SubjectSavedSession && !saved) {
			return Wrap(CodeInvalidArgument, ErrSubjectMismatch)
		}
	}

	return nil
}

func Local() Location {
	return Location{Kind: LocationLocal, Connection: ""}
}

func Peer(connection string) Location {
	return Location{Kind: LocationPeer, Connection: connection}
}

func Origin() Location {
	return Location{Kind: LocationOrigin, Connection: ""}
}

func LocationOf(target session.Session) Location {
	switch target.Origin {
	case session.OriginSSH:
		return Peer(target.RuntimeIdentity.ConnectionID)
	case session.OriginBridge:
		return Origin()
	case session.OriginLocal:
		return Local()
	default:
		return Local()
	}
}

func SubjectOf(sessions ...session.Session) Subject {
	at := Local()
	if len(sessions) > 0 {
		at = LocationOf(sessions[0])
	}

	return Subject{At: at, Sessions: sessions, Pane: "", Task: ""}
}

// SelectSession matches an exact ID before a name.
func SelectSession(rows []session.Session, selector string) (session.Session, error) {
	if index := slices.IndexFunc(rows, func(row session.Session) bool { return row.ID == selector }); index >= 0 {
		return rows[index], nil
	}

	if index := slices.IndexFunc(rows, func(row session.Session) bool { return row.Name == selector }); index >= 0 {
		return rows[index], nil
	}

	return session.Session{}, Wrap(CodeNotFound, session.ErrSessionNotFound)
}

func Lookup(id ID) (Entry, bool) {
	entry, ok := registry[id]

	return entry, ok
}

func All() []Entry {
	entries := make([]Entry, 0, len(order))
	for _, id := range order {
		entries = append(entries, registry[id])
	}

	return entries
}

// Run returns, on partial or uncertain failures, an output that describes what is known
// alongside the error.
func Run[In, Out any](
	ctx context.Context,
	dispatcher Dispatcher,
	definition *Definition[In, Out],
	at Location,
	caller Caller,
	input In,
) (Out, error) {
	var zero Out

	output, err := dispatcher.Dispatch(ctx, Invocation{Action: definition.ID, At: at, Caller: caller, Input: input})
	if err != nil {
		err = oops.In("action").With("action", string(definition.ID)).Wrap(err)
	}

	if output == nil {
		return zero, err
	}

	typed, ok := output.(Out)
	if !ok {
		return zero, errors.Join(err, Wrap(CodeFailed, ErrOutputTypeMismatch))
	}

	return typed, err
}

func RunAny(ctx context.Context, dispatcher Dispatcher, invocation Invocation) (any, error) {
	if _, ok := Lookup(invocation.Action); !ok {
		return nil, Wrap(CodeUnsupported, ErrUnknownAction)
	}

	output, err := dispatcher.Dispatch(ctx, invocation)
	if err != nil {
		return output, oops.In("action").With("action", string(invocation.Action)).Wrap(err)
	}

	return output, nil
}

func (dispatchers Dispatchers) Accepts(at Location, id ID) bool {
	return dispatchers.find(at, id) != nil
}

func (dispatchers Dispatchers) Dispatch(ctx context.Context, invocation Invocation) (any, error) {
	dispatcher := dispatchers.find(invocation.At, invocation.Action)
	if dispatcher == nil {
		return nil, Wrap(CodeUnsupported, ErrLocationRejected)
	}

	output, err := dispatcher.Dispatch(ctx, invocation)
	if err != nil {
		return output, oops.In("action").With("action", string(invocation.Action)).Wrap(err)
	}

	return output, nil
}

func (dispatchers Dispatchers) find(at Location, id ID) Dispatcher {
	for _, dispatcher := range dispatchers {
		if dispatcher != nil && dispatcher.Accepts(at, id) {
			return dispatcher
		}
	}

	return nil
}

func (router Router) Accepts(at Location, id ID) bool {
	dispatcher := router.dispatcher(at)

	return dispatcher != nil && dispatcher.Accepts(at, id)
}

func (router Router) Dispatch(ctx context.Context, invocation Invocation) (any, error) {
	dispatcher := router.dispatcher(invocation.At)
	if dispatcher == nil || !dispatcher.Accepts(invocation.At, invocation.Action) {
		return nil, Wrap(CodeUnsupported, ErrLocationRejected)
	}

	output, err := dispatcher.Dispatch(ctx, invocation)
	if err != nil {
		return output, oops.In("action").With("location", invocation.At.Kind.String()).Wrap(err)
	}

	return output, nil
}

func (router Router) dispatcher(at Location) Dispatcher {
	switch at.Kind {
	case LocationLocal:
		return router.Local
	case LocationPeer:
		return router.Peer
	case LocationOrigin:
		return router.Origin
	default:
		return nil
	}
}

func validSegment(segment string) bool {
	if segment == "" || segment[0] == '-' || segment[len(segment)-1] == '-' {
		return false
	}

	for _, character := range segment {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}

	return true
}
