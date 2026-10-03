package snapshot

import (
	"context"
	"slices"
	"strconv"
	"time"

	prettytable "github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	defaultHistoryLimit = 20
	historyTimeLayout   = "2006-01-02 15:04"
	shortDigest         = 12
)

// historyEntry is one save attempt as the history table shows it, newest first.
type historyEntry struct {
	attempt  store.Attempt
	savedAt  time.Time
	sessions int
	saved    bool
	current  bool
}

// loadHistoryEntries returns the newest limit attempts (every attempt when limit is zero) with
// the save time and session count of each one that produced a generation.
func loadHistoryEntries(ctx context.Context, repo *store.Repository, catalog store.Catalog, limit int) []historyEntry {
	var currentSeq uint64

	generations := make(map[string]bool, len(catalog.Generations))
	for _, g := range catalog.Generations {
		generations[g.ID] = true

		if g.ID == catalog.Current {
			currentSeq = g.Sequence
		}
	}

	entries := make([]historyEntry, 0, min(len(catalog.Attempts), max(limit, 0)))

	for _, attempt := range slices.Backward(catalog.Attempts) {
		if limit > 0 && len(entries) == limit {
			break
		}

		entry := historyEntry{
			attempt:  attempt,
			saved:    generations[attempt.ID],
			current:  attempt.ID == catalog.Current || (currentSeq > 0 && attempt.Sequence == currentSeq),
			savedAt:  time.Time{},
			sessions: 0,
		}

		if entry.saved {
			if manifest, err := repo.Read(ctx, attempt.ID); err == nil {
				entry.savedAt = time.Unix(0, manifest.Finished)
				entry.sessions = len(manifest.Sessions)
			}
		}

		entries = append(entries, entry)
	}

	return entries
}

// historyResult names an attempt's outcome. Every observation is saved incomplete because tmux
// cannot be checkpointed atomically, so incomplete and committed both read as saved.
func historyResult(state string) string {
	switch state {
	case "committed", "incomplete":
		return "saved"
	case "failed":
		return "failed"
	case "pending":
		return "unfinished"
	default:
		return state
	}
}

func historyRows(entries []historyEntry, full bool) []prettytable.Row {
	rows := make([]prettytable.Row, 0, len(entries))

	for _, entry := range entries {
		savedAt, sessions, current := "", "", ""
		if !entry.savedAt.IsZero() {
			// time.Unix already yields the reader's local time.
			savedAt = entry.savedAt.Format(historyTimeLayout)
		}

		if entry.saved {
			sessions = strconv.Itoa(entry.sessions)
		}

		if entry.current {
			current = "yes"
		}

		row := prettytable.Row{entry.attempt.Sequence, savedAt, historyResult(entry.attempt.State), sessions, current}

		switch {
		case full:
			row = append(row, entry.attempt.ID, entry.attempt.ManifestSHA256)
		case entry.saved:
			row = append(row, entry.attempt.ID[:min(len(entry.attempt.ID), shortGenerationID)])
		default:
			row = append(row, "")
		}

		rows = append(rows, row)
	}

	return rows
}

func renderHistoryTable(cmd *cobra.Command, entries []historyEntry, total int, full bool) error {
	if total == 0 {
		return clibase.WriteLine(cmd, "No snapshot history found.")
	}

	columns := []clibase.TableColumn{
		{Heading: "#", Fixed: true, MinWidth: colWidthID, Wrap: nil},
		{Heading: "Saved", Fixed: true, MinWidth: len(historyTimeLayout), Wrap: nil},
		{Heading: "Result", Fixed: true, MinWidth: colWidthState, Wrap: nil},
		{Heading: "Sessions", Fixed: true, MinWidth: colWidthID, Wrap: nil},
		{Heading: "Current", Fixed: true, MinWidth: colWidthID, Wrap: nil},
	}

	if full {
		columns = append(columns,
			clibase.TableColumn{Heading: "Generation", Fixed: false, MinWidth: colWidthSession, Wrap: clibase.WrapIdentifier},
			clibase.TableColumn{Heading: "Digest", Fixed: false, MinWidth: colWidthDigest, Wrap: clibase.WrapIdentifier},
		)
	} else {
		columns = append(columns, clibase.TableColumn{Heading: "Generation", Fixed: true, MinWidth: shortGenerationID, Wrap: nil})
	}

	if err := clibase.WriteResponsiveTable(cmd, columns, historyRows(entries, full), full); err != nil {
		return err
	}

	if len(entries) < total {
		if err := clibase.WriteOutput(cmd, "Showing the newest %d of %d save attempts; --all shows every one.\n", len(entries), total); err != nil {
			return err
		}
	}

	if len(entries) > 0 && historyResult(entries[0].attempt.State) != "saved" {
		return clibase.WriteLine(cmd, "The latest save did not finish; restore uses the newest saved generation (Current).")
	}

	return nil
}
