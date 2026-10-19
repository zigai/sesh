//go:build linux || darwin

package remote

import (
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/task"
)

const MaxTasks = 4096

// TaskRow is a task on a peer: its record and where its window is in the peer's tmux. The
// record carries no resume command; waking happens on the peer.
type TaskRow struct {
	Record      task.Record `json:"record"`
	Present     bool        `json:"present"`
	SessionID   string      `json:"sessionId"`
	SessionName string      `json:"sessionName"`
	WindowID    string      `json:"windowId"`
	WindowIndex int         `json:"windowIndex"`
	WindowName  string      `json:"windowName"`
	AgentPane   string      `json:"agentPane"`
	Stalled     bool        `json:"stalled"`
}

func NewTaskRow(live task.Live) TaskRow {
	record := live.Record
	record.Resume = task.Resume{Harness: "", Session: "", Command: []string{}}
	record.Owner = task.Process{PID: 0, Birth: ""}

	return TaskRow{
		Record:      record,
		Present:     live.Present,
		SessionID:   string(live.SessionID),
		SessionName: live.SessionName,
		WindowID:    string(live.WindowID),
		WindowIndex: live.WindowIndex,
		WindowName:  live.WindowName,
		AgentPane:   string(live.AgentPane),
		Stalled:     live.Stalled,
	}
}

func CatalogTasks(state ConnectionState) []task.Live {
	tasks := make([]task.Live, 0, len(state.Snapshot.Tasks))

	for _, row := range state.Snapshot.Tasks {
		tasks = append(tasks, task.Live{
			Record: row.Record, Present: row.Present, SessionID: gotmux.SessionID(row.SessionID),
			SessionName: row.SessionName, WindowID: gotmux.WindowID(row.WindowID), WindowIndex: row.WindowIndex, WindowName: row.WindowName,
			AgentPane: gotmux.PaneID(row.AgentPane), AgentPID: 0, Stalled: row.Stalled,
		})
	}

	return tasks
}

func validTaskRows(rows []TaskRow) bool {
	if len(rows) > MaxTasks {
		return false
	}

	records := make([]task.Record, 0, len(rows))

	for _, row := range rows {
		if !validTaskRow(row) {
			return false
		}

		records = append(records, row.Record)
	}

	return task.ValidateRecords(records) == nil
}

func validTaskRow(row TaskRow) bool {
	record := row.Record

	for _, text := range []string{
		row.SessionID, row.SessionName, row.WindowID, row.WindowName, row.AgentPane, record.Project, record.Harness,
		record.Directory, record.Branch, record.Base, record.Title, record.PR.URL,
	} {
		if !ValidText(text) {
			return false
		}
	}

	if !ValidIdentifier(record.ID) || row.WindowIndex < 0 || len(record.Resume.Command) != 0 || len(record.Runs) > MaxActions {
		return false
	}

	for _, run := range record.Runs {
		if !ValidText(run.Action) || !ValidText(run.Window) {
			return false
		}
	}

	return true
}
