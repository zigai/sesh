package action

import (
	"fmt"
	"time"

	"github.com/zigai/sesh/internal/session"
)

var (
	SessionCreate = define(Descriptor{
		ID:      "session.create",
		Title:   "Create session",
		Summary: "Create a live tmux session",
		Help:    "Create a live tmux session. Without --dir the session starts in tmux's default directory.",
		Subject: SubjectNone,
		Arity:   ArityOne,
		Params: []Param{
			newParam("name", ParamText, "Session name").required().positional().fromQuery(),
			newParam("dir", ParamPath, "Initial working directory").withShort("d").suggest(SuggestSessionDirectory),
		},
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteOnByDefault,
		InPicker:    false,
		check:       nil,
	}, bindCreateSession, func(out CreatedSession) string {
		return "Created session " + out.Session.Name
	})

	SessionRename = define(Descriptor{
		ID:          "session.rename",
		Title:       "Rename session",
		Summary:     "Rename a live tmux session",
		Help:        "Rename a live tmux session and update its switch history.",
		Subject:     SubjectLiveSession,
		Arity:       ArityOne,
		Params:      []Param{newParam("name", ParamText, "New session name").required().positional()},
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteOnByDefault,
		InPicker:    false,
		check:       nil,
	}, func(subject Subject, values Values) (RenameSession, error) {
		return RenameSession{Session: RefOf(subject.Sessions[0]), Name: values.String("name")}, nil
	}, func(out RenamedSession) string {
		return fmt.Sprintf("Renamed session %s to %s", out.PreviousName, out.Name)
	})

	SessionKill = define(Descriptor{
		ID:          "session.kill",
		Title:       "Kill session",
		Summary:     "Kill a live tmux session",
		Help:        "Kill a live tmux session. Refuses an attached session unless --force is given.",
		Subject:     SubjectLiveSession,
		Arity:       ArityOne,
		Params:      []Param{forceParam("Kill even if clients are attached")},
		NeedsClient: false,
		Interactive: false,
		Destructive: true,
		Remote:      RemoteOffByDefault,
		InPicker:    false,
		check:       refuseAttached,
	}, func(subject Subject, values Values) (KillSession, error) {
		return KillSession{Session: RefOf(subject.Sessions[0]), Force: values.Bool("force")}, nil
	}, func(out KilledSession) string {
		return "Killed session " + out.Session.Name
	})

	SessionDelete = define(Descriptor{
		ID:      "session.delete",
		Title:   "Delete session",
		Summary: "Delete a session from live tmux and the snapshot repository",
		Help: "Delete a session from live tmux and every saved generation of this tmux server.\n\n" +
			"Live state is killed first so a concurrent save cannot re-create the session; attachments and save " +
			"races are checked again before anything is destroyed. Refuses an attached session unless --force is given.",
		Subject:     SubjectSession,
		Arity:       ArityOne,
		Params:      []Param{forceParam("Delete even if clients are attached")},
		NeedsClient: false,
		Interactive: false,
		Destructive: true,
		Remote:      RemoteOffByDefault,
		InPicker:    false,
		check:       refuseAgentPaneOrAttached,
	}, func(subject Subject, values Values) (DeleteSession, error) {
		return DeleteSession{Session: RefOf(subject.Sessions[0]), Force: values.Bool("force")}, nil
	}, func(out DeletedSession) string {
		return fmt.Sprintf("Deleted session %s (live killed: %t, saved generations: %d)", out.Name, out.LiveKilled, out.SavedGenerations)
	})

	SessionSwitch = define(Descriptor{
		ID:      "session.switch",
		Title:   "Switch to session",
		Summary: "Switch the invoking tmux client to a live session",
		Help: "Switch the invoking tmux client to a live session and record switch history.\n\n" +
			"The invoking client must be the single attached client displaying the session that owns the " +
			"caller's pane. The switch is verified before it is reported as successful.",
		Subject:     SubjectLiveSession,
		Arity:       ArityOne,
		Params:      nil,
		NeedsClient: true,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteNever,
		InPicker:    false,
		check:       nil,
	}, func(subject Subject, _ Values) (SwitchSession, error) {
		return SwitchSession{Session: RefOf(subject.Sessions[0])}, nil
	}, func(out SwitchedSession) string {
		return "Switched to session " + out.Session.Name
	})
)

// SessionRef identifies one session incarnation. Live references carry the tmux server and
// session creation times so a replaced session is never mistaken for the selected one.
type SessionRef struct {
	Kind            session.Kind `json:"kind"`
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Socket          string       `json:"socket"`
	ServerPID       int          `json:"serverPid"`
	ServerStartedAt time.Time    `json:"serverStartedAt"`
	CreatedAt       time.Time    `json:"createdAt"`
	Generation      string       `json:"generation"`
}

type CreateSession struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
}

type CreatedSession struct {
	Session   SessionRef `json:"session"`
	Directory string     `json:"directory"`
}

type RenameSession struct {
	Session SessionRef `json:"session"`
	Name    string     `json:"name"`
}

type RenamedSession struct {
	PreviousName   string `json:"previousName"`
	Name           string `json:"name"`
	HistoryUpdated bool   `json:"historyUpdated"`
}

type KillSession struct {
	Session SessionRef `json:"session"`
	Force   bool       `json:"force"`
}

type KilledSession struct {
	Session SessionRef `json:"session"`
}

type DeleteSession struct {
	Session SessionRef `json:"session"`
	Force   bool       `json:"force"`
}

type DeletedSession struct {
	Name                 string   `json:"name"`
	LiveKilled           bool     `json:"liveKilled"`
	SavedDeleted         bool     `json:"savedDeleted"`
	SavedGenerations     int      `json:"savedGenerations"`
	SavedSessions        int      `json:"savedSessions"`
	PreservedGenerations []string `json:"preservedGenerations"`
	PendingRewrites      []string `json:"pendingRewrites"`
}

type SwitchSession struct {
	Session SessionRef `json:"session"`
}

type SwitchedSession struct {
	Session SessionRef `json:"session"`
}

// RefOf captures the identity of a catalog session.
func RefOf(target session.Session) SessionRef {
	return SessionRef{
		Kind:            target.Kind,
		ID:              target.ID,
		Name:            target.Name,
		Socket:          target.TmuxServerSocket,
		ServerPID:       target.RuntimeIdentity.TmuxServerPID,
		ServerStartedAt: target.RuntimeIdentity.TmuxServerStartedAt,
		CreatedAt:       target.RuntimeIdentity.TmuxSessionCreatedAt,
		Generation:      target.Generation,
	}
}

func (ref SessionRef) Saved() bool {
	return ref.Kind == session.KindSaved
}

// Session describes the referenced session as a catalog row at a location, carrying the
// identity later actions check before they act on it.
func (ref SessionRef) Session(at Location) session.Session {
	var target session.Session

	target.ID = ref.ID
	target.Name = ref.Name
	target.Kind = ref.Kind
	target.Generation = ref.Generation
	target.TmuxServerSocket = ref.Socket
	target.RuntimeIdentity.TmuxServerSocket = ref.Socket
	target.RuntimeIdentity.TmuxServerPID = ref.ServerPID
	target.RuntimeIdentity.TmuxServerStartedAt = ref.ServerStartedAt
	target.RuntimeIdentity.TmuxSessionCreatedAt = ref.CreatedAt

	switch at.Kind {
	case LocationPeer:
		target.Origin = session.OriginSSH
		target.RuntimeIdentity.ConnectionID = at.Connection
	case LocationOrigin:
		target.Origin = session.OriginBridge
	case LocationLocal:
		target.Origin = session.OriginLocal
	}

	return target
}

func bindCreateSession(_ Subject, values Values) (CreateSession, error) {
	return CreateSession{Name: values.String("name"), Directory: values.String("dir")}, nil
}

func forceParam(usage string) Param {
	return newParam("force", ParamBool, usage).withShort("f")
}

// refuseAgentPaneOrAttached keeps a selected agent pane from standing for its whole session:
// deleting removes the session and every saved generation of it.
func refuseAgentPaneOrAttached(subject Subject) error {
	if subject.Pane != "" {
		return Wrap(CodeInvalidArgument, ErrWholeSessionRequired)
	}

	return refuseAttached(subject)
}

func refuseAttached(subject Subject) error {
	for _, target := range subject.Sessions {
		if !target.Saved() && target.Attached {
			return Wrap(CodeAttached, ErrSessionAttached)
		}
	}

	return nil
}
