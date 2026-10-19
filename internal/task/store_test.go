package task_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zigai/sesh/internal/task"
)

var errRejected = errors.New("rejected")

func TestLoadOfAMissingLedgerIsEmpty(t *testing.T) {
	ledger, err := task.NewStore(filepath.Join(t.TempDir(), "state", "tasks.json")).Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if ledger.LastID != 0 || len(ledger.Tasks) != 0 || len(ledger.Projects) != 0 {
		t.Errorf("Load() = %+v, want an empty ledger", ledger)
	}
}

func TestUpdatePersistsRecordsAndProjectState(t *testing.T) {
	store := task.NewStore(filepath.Join(t.TempDir(), "state", "tasks.json"))

	_, err := store.Update(t.Context(), func(ledger *task.Ledger) error {
		id := ledger.NextID()
		ledger.Put(projectRecord(id))
		ledger.SetProject(task.ProjectState{Root: "/code/shop", LastNumber: 3})

		return nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	ledger, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	record, found := ledger.Find("t1")
	if !found || record.Branch != "wt/main/3" || record.Directory != "/worktrees/shop/main-3" {
		t.Errorf("Find(t1) = %+v, %t", record, found)
	}

	if state := ledger.Project("/code/shop"); state.LastNumber != 3 {
		t.Errorf("Project() = %+v", state)
	}
}

func TestConcurrentUpdatesKeepEveryRecordAndUniqueIDs(t *testing.T) {
	store := task.NewStore(filepath.Join(t.TempDir(), "state", "tasks.json"))

	const writers = 8

	var group sync.WaitGroup

	for range writers {
		group.Go(func() {
			_, err := store.Update(t.Context(), func(ledger *task.Ledger) error {
				record := task.EmptyRecord()
				record.ID, record.Kind, record.State, record.Harness, record.Directory = ledger.NextID(), task.KindLoose, task.StateActive, "claude", "/tmp"
				ledger.Put(record)

				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}

	group.Wait()

	ledger, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(ledger.Tasks) != writers || ledger.LastID != writers {
		t.Fatalf("ledger has %d tasks and last ID %d, want %d of each", len(ledger.Tasks), ledger.LastID, writers)
	}
}

func TestRejectedUpdateLeavesTheLedgerUnchanged(t *testing.T) {
	store := task.NewStore(filepath.Join(t.TempDir(), "state", "tasks.json"))

	if _, err := store.Update(t.Context(), func(ledger *task.Ledger) error {
		ledger.SetProject(task.ProjectState{Root: "/code/shop", LastNumber: 1})

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := store.Update(t.Context(), func(ledger *task.Ledger) error {
		ledger.SetProject(task.ProjectState{Root: "/code/shop", LastNumber: 2})

		return errRejected
	})
	if !errors.Is(err, errRejected) {
		t.Fatalf("Update() error = %v, want the change's error", err)
	}

	ledger, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if state := ledger.Project("/code/shop"); state.LastNumber != 1 {
		t.Errorf("Project() = %+v, want the state before the rejected update", state)
	}
}

func TestCorruptOrUnknownLedgersAreRejected(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"not json", "{"},
		{"unknown version", `{"version":1,"lastId":0,"tasks":[],"projects":[]}`},
		{"missing field", `{"version":2,"tasks":[],"projects":[]}`},
		{"unknown field", `{"version":2,"lastId":0,"tasks":[],"projects":[],"extra":1}`},
		{
			"duplicate task id",
			`{"version":2,"lastId":2,"projects":[],"tasks":[` + record("t1") + `,` + record("t1") + `]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(dir, "tasks.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			store := task.NewStore(path)
			if _, err := store.Load(t.Context()); err == nil {
				t.Error("Load() error = nil, want rejection")
			}

			if _, err := store.Update(t.Context(), func(*task.Ledger) error { return nil }); err == nil {
				t.Error("Update() error = nil, want rejection")
			}

			if content, err := os.ReadFile(path); err != nil || string(content) != tt.content {
				t.Errorf("ledger file changed to %q", content)
			}
		})
	}
}

func record(id string) string {
	return `{"id":"` + id + `","kind":"loose","state":"active","project":"","number":0,"harness":"pi",` +
		`"directory":"/tmp","branch":"","base":"","createdAt":1}`
}

func projectRecord(id string) task.Record {
	record := task.EmptyRecord()
	record.ID, record.Kind, record.State, record.Project, record.Number = id, task.KindProject, task.StateActive, "/code/shop", 3
	record.Harness, record.Directory, record.Branch, record.Base = "pi", "/worktrees/shop/main-3", "wt/main/3", "main"
	record.CreatedAt = 1700000000

	return record
}
