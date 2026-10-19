//go:build linux || darwin

package work

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/history"
	"github.com/zigai/aht/v2/pkg/registry"

	"github.com/zigai/sesh/internal/agents"
)

// Recorder records the conversations of the interactive agents in one tmux server's panes, with
// the task whose window each runs in.
type Recorder struct {
	store Store
	panes Panes

	server string

	mu    sync.Mutex
	known State
	// tasks are the tasks whose windows conversations were last seen in.
	tasks map[string]string
}

// Panes reads the socket of the recorded tmux server and the task of each pane in a task window.
type Panes func(context.Context) (string, map[string]string, error)

// NewRecorder records into store the agents of the tmux server panes reads.
func NewRecorder(store Store, panes Panes) *Recorder {
	return &Recorder{store: store, panes: panes, server: "", mu: sync.Mutex{}, known: emptyState(), tasks: map[string]string{}}
}

// Record records the conversations sessions show. It writes the work state only when a
// conversation is new or changed since the last write.
func (r *Recorder) Record(ctx context.Context, sessions []aht.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sightings := r.sightings(sessions)

	probe := r.known
	probe.Items = append([]Item(nil), r.known.Items...)

	if !probe.Observe(sightings, time.Now(), func(string) (string, string) { return "", "" }) {
		return nil
	}

	socket, taskPanes, err := r.panes(ctx)
	if err != nil {
		return err
	}

	r.server = agents.NormalizeServerSocket(socket)
	kept := sightings[:0]

	for _, seen := range sightings {
		if !agents.SameServerSocket(seen.Server, r.server) {
			continue
		}

		seen.Task = taskPanes[seen.Pane]
		r.tasks[seen.Conversation] = seen.Task
		kept = append(kept, seen)
	}

	titled := withHistory(ctx, kept, r.known)

	state, err := r.store.Update(ctx, func(state *State) error {
		state.Observe(titled, time.Now(), func(dir string) (string, string) { return branchOf(ctx, dir), projectOf(ctx, dir) })

		return nil
	})
	if err != nil {
		return err
	}

	r.known = state

	return nil
}

func (r *Recorder) sightings(sessions []aht.Session) []Sighting {
	var sightings []Sighting

	for _, agent := range agents.ObservedAgentSessions(sessions) {
		if r.server != "" && !agents.SameServerSocket(agent.ServerSocket, r.server) {
			continue
		}

		sightings = append(sightings, Sighting{
			Harness: agent.Harness, Conversation: agent.HarnessSessionID, SessionPath: agent.SessionPath,
			Resume: agent.ResumeCommand, Title: "", Directory: agent.CWD, Project: agent.ProjectRoot, Task: r.tasks[agent.HarnessSessionID],
			Server: agent.ServerSocket, Pane: agent.PaneID, PanePID: agent.PanePID, TurnAt: agent.ActivityChanged,
			PromptAt: agent.PromptAt,
		})
	}

	return sightings
}

// withHistory fills in the title and project of the conversations the work state has none for
// yet from their history: the title the harness or the user gave, else the first prompt.
func withHistory(ctx context.Context, sightings []Sighting, known State) []Sighting {
	complete := make(map[string]bool, len(known.Items))

	for _, item := range known.Items {
		if item.Title != "" && item.Project != "" {
			complete[item.Harness+"\x00"+item.Conversation] = true
		}
	}

	for index, seen := range sightings {
		if complete[seen.Harness+"\x00"+seen.Conversation] || seen.SessionPath == "" {
			continue
		}

		catalog := history.Catalog{
			Sources: []history.Source{{Harness: registry.Harness(seen.Harness), Path: seen.SessionPath}}, IndexPath: "", Progress: nil,
		}

		var query history.ListQuery

		query.Limit = 1

		listed, err := catalog.List(ctx, query)
		if err != nil || len(listed.Matches) == 0 {
			continue
		}

		conversation := listed.Matches[0].Conversation
		sightings[index].Title = conversationTitle(conversation)

		if seen.Project == "" {
			sightings[index].Project = conversation.ProjectRoot
		}
	}

	return sightings
}

func conversationTitle(conversation history.Conversation) string {
	for _, title := range []string{conversation.CustomTitle, conversation.AITitle, conversation.Title, conversation.Prompt} {
		if line, _, _ := strings.Cut(strings.TrimSpace(title), "\n"); line != "" {
			return line
		}
	}

	return ""
}
