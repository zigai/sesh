//go:build linux || darwin

package remote

import (
	"cmp"
	"slices"

	"github.com/zigai/sesh/internal/work"
)

// MaxWork bounds the work items one peer reports.
const MaxWork = 4096

// NewWorkRows describes a peer's work items, the most recently active first and at most MaxWork,
// without their resume commands and transcripts: resuming happens on the peer.
func NewWorkRows(items []work.Item) []work.Item {
	rows := slices.Clone(items)
	slices.SortStableFunc(rows, func(a, b work.Item) int { return cmp.Compare(b.TurnAt, a.TurnAt) })
	rows = rows[:min(len(rows), MaxWork)]

	for index := range rows {
		rows[index].Resume = []string{}
		rows[index].SessionPath = ""
	}

	return rows
}

func validWorkRows(rows []work.Item) bool {
	if len(rows) > MaxWork {
		return false
	}

	ids := make(map[string]bool, len(rows))

	for _, row := range rows {
		if ids[row.ID] || !validWorkRow(row) {
			return false
		}

		ids[row.ID] = true
	}

	return true
}

func validWorkRow(row work.Item) bool {
	if !ValidIdentifier(row.ID) || !ValidIdentifier(row.Conversation) || len(row.Resume) != 0 || row.SessionPath != "" ||
		(row.Task != "" && !ValidIdentifier(row.Task)) || !validPRLinks(row.PRs) {
		return false
	}

	for _, text := range []string{row.Harness, row.Title, row.Directory, row.Project, row.Branch, row.Server, row.Pane, row.FinishedBy} {
		if !ValidText(text) {
			return false
		}
	}

	return true
}

func validPRLinks(links []work.PRLink) bool {
	if len(links) > MaxWork {
		return false
	}

	for _, link := range links {
		if len(link.Issues) > MaxWork || !ValidText(link.URL) || !ValidText(string(link.State)) || !ValidText(link.Source) {
			return false
		}
	}

	return true
}
