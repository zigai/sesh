package tui

import (
	"slices"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/inbox"
	"github.com/zigai/sesh/internal/session"
)

func seedInboxState(model *Model, state inbox.State) *memoryInbox {
	store := &memoryInbox{state: inbox.State{
		Version: inbox.Version,
		Snoozes: slices.Clone(state.Snoozes),
		Unseen:  slices.Clone(state.Unseen),
	}}
	model.deps.Inbox = store
	model.inbox = state

	return store
}

func snoozeNextMessage(t *testing.T, model *Model, name string) {
	t.Helper()

	selectInboxRow(t, model, name)
	runHandler(
		t,
		model,
		model.invokeAction(
			action.InboxSnooze.ID,
			action.Values{"until": {config.SnoozeNextMessage}},
		),
	)
}

func storedInboxKeys(store *memoryInbox) ([]string, []string) {
	snoozed := make([]string, 0, len(store.state.Snoozes))
	for _, snooze := range store.state.Snoozes {
		snoozed = append(snoozed, snooze.Key)
	}

	unseen := make([]string, 0, len(store.state.Unseen))
	for _, mark := range store.state.Unseen {
		unseen = append(unseen, mark.Key)
	}

	slices.Sort(snoozed)
	slices.Sort(unseen)

	return snoozed, unseen
}

func assertStoredInboxKeys(t *testing.T, store *memoryInbox, snoozed []string, unseen []string) {
	t.Helper()

	gotSnoozed, gotUnseen := storedInboxKeys(store)
	if !slices.Equal(gotSnoozed, snoozed) {
		t.Errorf("snoozed keys = %v, want %v", gotSnoozed, snoozed)
	}

	if !slices.Equal(gotUnseen, unseen) {
		t.Errorf("unseen keys = %v, want %v", gotUnseen, unseen)
	}
}

func TestInboxUpdatesDropLocalEntriesThatNoLongerAffectARow(t *testing.T) {
	now := time.Now()
	model := newInboxTestModel(t, nil, []session.AgentSession{
		inboxTestAgent("target", "%1", session.AgentStatusIdle, now.Add(-time.Hour)),
		inboxTestAgent("replied", "%2", session.AgentStatusIdle, now.Add(-time.Minute)),
		inboxTestAgent("resting", "%3", session.AgentStatusIdle, now.Add(-2*time.Hour)),
		inboxTestAgent("working", "%4", session.AgentStatusRunning, now.Add(-time.Minute)),
		inboxTestAgent("viewed", "%5", session.AgentStatusIdle, now.Add(-2*time.Hour)),
		inboxTestAgent("unread", "%6", session.AgentStatusIdle, now.Add(-2*time.Hour)),
	})
	markViewed(model, "%5", now.Add(-time.Minute))
	markViewed(model, "%6", now.Add(-time.Hour))

	hourAgo := now.Add(-time.Hour).UnixNano()
	store := seedInboxState(model, inbox.State{
		Version: inbox.Version,
		Snoozes: []inbox.Snooze{
			{Key: "/claude/conversation-replied", Until: 0, SnoozedAt: hourAgo},
			{Key: "/claude/conversation-resting", Until: 0, SnoozedAt: hourAgo},
			{Key: "/claude/conversation-working", Until: 0, SnoozedAt: hourAgo},
			{Key: "/claude/conversation-gone", Until: 0, SnoozedAt: hourAgo},
			{
				Key:       "/claude/conversation-later",
				Until:     now.Add(time.Hour).Unix(),
				SnoozedAt: hourAgo,
			},
			{
				Key:       "/claude/conversation-elapsed",
				Until:     now.Add(-time.Minute).Unix(),
				SnoozedAt: hourAgo,
			},
			{Key: "devbox/claude/conversation-away", Until: 0, SnoozedAt: hourAgo},
		},
		Unseen: []inbox.Unseen{
			{Key: "/claude/conversation-viewed", At: hourAgo},
			{Key: "/claude/conversation-unread", At: now.Add(-time.Minute).UnixNano()},
			{Key: "/claude/conversation-gone", At: hourAgo},
			{Key: "devbox/claude/conversation-away", At: hourAgo},
		},
	})
	refreshInbox(model)

	snoozeNextMessage(t, model, "target")

	assertStoredInboxKeys(
		t,
		store,
		[]string{
			"/claude/conversation-later",
			"/claude/conversation-resting",
			"/claude/conversation-target",
			"/claude/conversation-working",
			"devbox/claude/conversation-away",
		},
		[]string{"/claude/conversation-unread", "devbox/claude/conversation-away"},
	)
}

func TestInboxUpdatesDropEntriesOfConnectedHostsOnly(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Remote.Enabled = true
	cfg.Remote.Connections = []config.RemoteConnectionConfig{
		{ID: inboxTestHost, Host: inboxTestHost},
		{ID: "laptop", Host: "laptop"},
	}

	now := time.Now()
	model := loaderTestModel(t, cfg, Dependencies{})
	model.sessions = []session.Session{
		{
			ID:         "$1",
			Name:       testSupportSessionAlphaName,
			Kind:       session.KindLive,
			Origin:     session.OriginLocal,
			AgentTotal: 1,
			Agents: []session.AgentSession{
				inboxTestAgent("here", "%1", session.AgentStatusIdle, now.Add(-time.Hour)),
			},
		},
	}
	model.handleRemoteCatalog(remoteCatalogMsg{catalog: RemoteCatalog{
		GeneratedAt: now,
		Connections: []RemoteConnection{
			{
				ID: inboxTestHost, Label: inboxTestHost, Connected: true, LastSeen: now,
				Sessions: []session.Session{
					{
						ID:         "$1",
						Name:       "shop",
						Kind:       session.KindLive,
						AgentTotal: 1,
						Agents: []session.AgentSession{
							inboxTestAgent("there", "%1", session.AgentStatusRunning, now),
						},
					},
				},
			},
			{
				ID:        "laptop",
				Label:     "laptop",
				Connected: false,
				LastSeen:  now.Add(-time.Hour),
				LastError: "connection_failed",
			},
		},
	}})
	model.displayMode = displayModeInbox

	hourAgo := now.Add(-time.Hour).UnixNano()
	store := seedInboxState(model, inbox.State{
		Version: inbox.Version,
		Snoozes: []inbox.Snooze{
			{Key: "devbox/claude/conversation-there", Until: 0, SnoozedAt: hourAgo},
			{Key: "devbox/claude/conversation-gone", Until: 0, SnoozedAt: hourAgo},
			{Key: "laptop/claude/conversation-away", Until: 0, SnoozedAt: hourAgo},
		},
		Unseen: []inbox.Unseen{
			{Key: "devbox/claude/conversation-gone", At: hourAgo},
			{Key: "laptop/claude/conversation-away", At: hourAgo},
		},
	})
	refreshInbox(model)

	snoozeNextMessage(t, model, "here")

	assertStoredInboxKeys(
		t,
		store,
		[]string{
			"/claude/conversation-here",
			"devbox/claude/conversation-there",
			"laptop/claude/conversation-away",
		},
		[]string{"laptop/claude/conversation-away"},
	)
}

func TestInboxUpdatesKeepEntriesTheyHaveNotLoaded(t *testing.T) {
	now := time.Now()
	model := newInboxTestModel(t, nil, []session.AgentSession{
		inboxTestAgent("target", "%1", session.AgentStatusIdle, now.Add(-time.Hour)),
	})

	store := seedInboxState(model, inbox.State{Version: inbox.Version, Snoozes: nil, Unseen: nil})
	store.state.Put(
		inbox.Snooze{Key: "/claude/conversation-newer", Until: 0, SnoozedAt: now.UnixNano()},
	)
	store.state.MarkUnseen("/claude/conversation-newer", now)

	snoozeNextMessage(t, model, "target")

	assertStoredInboxKeys(
		t,
		store,
		[]string{"/claude/conversation-newer", "/claude/conversation-target"},
		[]string{"/claude/conversation-newer"},
	)
}
