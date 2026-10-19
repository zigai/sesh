package task

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

func Variables(record Record) map[string]string {
	project := filepath.Base(record.Project)
	if record.Kind == KindLoose {
		project = ""
	}

	return map[string]string{
		"task": record.ID, "title": record.Title, "branch": record.Branch, "base": record.Base,
		"project": project, "worktree": record.Directory,
	}
}

func Render(prompt string, record Record) string {
	variables := Variables(record)

	pairs := make([]string, 0, len(variables)+len(variables))
	for name, value := range variables {
		pairs = append(pairs, "{"+name+"}", value)
	}

	return strings.NewReplacer(pairs...).Replace(prompt)
}

func (s *Service) RecordRun(ctx context.Context, id string, run Run) error {
	_, err := s.store.Update(ctx, func(ledger *Ledger) error {
		record, found := ledger.Find(id)
		if !found {
			return ErrNotFound
		}

		runs := make([]Run, 0, len(record.Runs)+1)
		for _, previous := range record.Runs {
			if previous.Action != run.Action {
				runs = append(runs, previous)
			}
		}

		runs = append(runs, run)
		record.Runs = runs
		ledger.Put(record)

		return nil
	})

	return taskError("record_run", err)
}

// Retitle records the title a task's agent reports and names the task window after it, unless
// the user renamed the window.
func (s *Service) Retitle(ctx context.Context, id, title string) (bool, error) {
	title = windowTitle(title)
	if title == "" {
		return false, nil
	}

	record, live, err := s.task(ctx, id)
	if err != nil {
		return false, taskError("retitle_task", err)
	}

	if record.Title == title {
		return false, nil
	}

	record.Title = title

	if live.WindowName == record.WindowName {
		window, err := s.server.Window(ctx, live.WindowID)
		if err != nil {
			return false, taskError("retitle_task", fmt.Errorf("find task window: %w", err))
		}

		if err := window.Rename(ctx, title); err != nil {
			return false, taskError("retitle_task", fmt.Errorf("rename task window: %w", err))
		}

		record.WindowName = title
	}

	return true, taskError("retitle_task", s.put(ctx, record))
}

func windowTitle(title string) string {
	// tmux escapes backslashes in window names, so the name would not read back as set.
	title = strings.ReplaceAll(strings.ToValidUTF8(title, ""), `\`, "/")

	return strings.Join(strings.FieldsFunc(title, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}
