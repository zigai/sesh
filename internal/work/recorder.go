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
	"github.com/zigai/sesh/internal/task"
)

type Recorder struct {
	store Store
	panes Panes

	server string

	mu                 sync.Mutex
	known              State
	taskByConversation map[string]string
}

type Panes func(context.Context) (task.PaneTasks, error)

func NewRecorder(store Store, panes Panes) *Recorder {
	return &Recorder{store: store, panes: panes, server: "", mu: sync.Mutex{}, known: emptyState(), taskByConversation: map[string]string{}}
}

// Record records the conversations sessions show. It writes the work state only when a
// conversation is new or changed since the last write.
func (r *Recorder) Record(ctx context.Context, sessions []aht.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sightings := r.sightings(sessions)
	if !r.known.changedBy(sightings, time.Now()) {
		return nil
	}

	panes, err := r.panes(ctx)
	if err != nil {
		return err
	}

	r.server = agents.NormalizeServerSocket(panes.Socket)
	kept := sightings[:0]

	for _, seen := range sightings {
		if !agents.SameServerSocket(seen.Server, r.server) {
			continue
		}

		seen.Task = panes.TaskByPane[seen.Pane]
		r.taskByConversation[seen.Conversation] = seen.Task
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
			Resume: agent.ResumeCommand, Title: "", Directory: agent.CWD, Project: agent.ProjectRoot, Task: r.taskByConversation[agent.HarnessSessionID],
			Server: agent.ServerSocket, Pane: agent.PaneID, PanePID: agent.PanePID, TurnAt: agent.ActivityChanged,
			PromptAt: agent.PromptAt,
		})
	}

	return sightings
}

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
