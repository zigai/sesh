package inbox_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/zigai/sesh/internal/inbox"
)

func TestLoadOfMissingStateIsEmpty(t *testing.T) {
	emptyDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(emptyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"missing file":      filepath.Join(emptyDir, "inbox.json"),
		"missing directory": statePath(t),
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			state, err := inbox.NewStore(path).Load(t.Context())
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			want := inbox.State{Version: 1, Snoozes: []inbox.Snooze{}, Unseen: []inbox.Unseen{}}
			if diff := cmp.Diff(want, state); diff != "" {
				t.Fatalf("Load() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStoreRejectsRelativeOrUncleanPath(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{
		"relative":        "inbox.json",
		"dot segment":     dir + "/./inbox.json",
		"dot-dot segment": dir + "/sub/../inbox.json",
		"doubled slash":   dir + "//inbox.json",
	}

	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			store := inbox.NewStore(path)

			if _, err := store.Load(t.Context()); !errors.Is(err, inbox.ErrPath) {
				t.Errorf("Load() error = %v, want ErrPath", err)
			}

			if _, err := store.Update(t.Context(), func(*inbox.State) error { return nil }); !errors.Is(err, inbox.ErrPath) {
				t.Errorf("Update() error = %v, want ErrPath", err)
			}
		})
	}

	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("rejected paths left entries %v (err %v)", entries, err)
	}
}

func TestUpdateRejectsNilChange(t *testing.T) {
	path := statePath(t)

	if _, err := inbox.NewStore(path).Update(t.Context(), nil); !errors.Is(err, inbox.ErrNilUpdate) {
		t.Fatalf("Update(nil) error = %v, want ErrNilUpdate", err)
	}

	requireNoFile(t, path)
}

func TestUpdatePersistsSnoozesAndUnseenMarks(t *testing.T) {
	path := statePath(t)
	seen := time.Unix(1_700_000_100, 42)

	returned, err := inbox.NewStore(path).Update(t.Context(), func(state *inbox.State) error {
		state.Put(inbox.Snooze{Key: "agent/one", Until: 1_700_003_600, SnoozedAt: 1_700_000_000})
		state.Put(inbox.Snooze{Key: "agent/two", Until: 0, SnoozedAt: 1_700_000_050})
		state.MarkUnseen("agent/three", seen)

		return nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	loaded, err := inbox.NewStore(path).Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := inbox.State{
		Version: 1,
		Snoozes: []inbox.Snooze{
			{Key: "agent/one", Until: 1_700_003_600, SnoozedAt: 1_700_000_000},
			{Key: "agent/two", Until: 0, SnoozedAt: 1_700_000_050},
		},
		Unseen: []inbox.Unseen{{Key: "agent/three", At: 1_700_000_100_000_000_042}},
	}
	if diff := cmp.Diff(want, loaded); diff != "" {
		t.Fatalf("Load() after Update() mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(want, returned); diff != "" {
		t.Fatalf("Update() result mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdatePropagatesChangeErrorWithoutWriting(t *testing.T) {
	path := seededStore(t)
	before := readFile(t, path)

	_, err := inbox.NewStore(path).Update(t.Context(), func(state *inbox.State) error {
		state.Put(inbox.Snooze{Key: "agent/new", Until: 0, SnoozedAt: 1})
		return errChangeRefused
	})
	if !errors.Is(err, errChangeRefused) {
		t.Fatalf("Update() error = %v, want the change error", err)
	}

	requireFile(t, path, before)
}

func TestUpdateRejectsInvalidStateWithoutWriting(t *testing.T) {
	cases := map[string]func(*inbox.State){
		"duplicate snooze key": func(s *inbox.State) {
			s.Snoozes = append(s.Snoozes, inbox.Snooze{Key: "agent/seed", Until: 0, SnoozedAt: 5})
		},
		"empty snooze key": func(s *inbox.State) {
			s.Put(inbox.Snooze{Key: "", Until: 0, SnoozedAt: 5})
		},
		"zero snoozed at": func(s *inbox.State) {
			s.Put(inbox.Snooze{Key: "agent/new", Until: 0, SnoozedAt: 0})
		},
		"negative snoozed at": func(s *inbox.State) {
			s.Put(inbox.Snooze{Key: "agent/new", Until: 0, SnoozedAt: -1})
		},
		"negative until": func(s *inbox.State) {
			s.Put(inbox.Snooze{Key: "agent/new", Until: -1, SnoozedAt: 5})
		},
		"duplicate unseen key": func(s *inbox.State) {
			s.Unseen = append(s.Unseen, inbox.Unseen{Key: "agent/seen", At: 5})
		},
		"empty unseen key": func(s *inbox.State) {
			s.Unseen = append(s.Unseen, inbox.Unseen{Key: "", At: 5})
		},
		"zero unseen time": func(s *inbox.State) {
			s.Unseen = append(s.Unseen, inbox.Unseen{Key: "agent/new", At: 0})
		},
		"too many snoozes": func(s *inbox.State) {
			s.Snoozes = snoozes(4097)
		},
		"too many unseen marks": func(s *inbox.State) {
			s.Unseen = unseenMarks(4097)
		},
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			path := seededStore(t)
			before := readFile(t, path)

			_, err := inbox.NewStore(path).Update(t.Context(), func(state *inbox.State) error {
				change(state)
				return nil
			})
			if !errors.Is(err, inbox.ErrInvalid) {
				t.Fatalf("Update() error = %v, want ErrInvalid", err)
			}

			requireFile(t, path, before)
		})
	}
}

func TestUpdateAcceptsTheEntryLimit(t *testing.T) {
	path := statePath(t)

	_, err := inbox.NewStore(path).Update(t.Context(), func(state *inbox.State) error {
		state.Snoozes = snoozes(4096)
		state.Unseen = unseenMarks(4096)

		return nil
	})
	if err != nil {
		t.Fatalf("Update() with 4096 entries each error = %v", err)
	}

	loaded, err := inbox.NewStore(path).Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(loaded.Snoozes) != 4096 || len(loaded.Unseen) != 4096 {
		t.Fatalf("Load() returned %d snoozes and %d unseen marks, want 4096 each", len(loaded.Snoozes), len(loaded.Unseen))
	}
}

func TestUpdateRejectsStateThatEncodesOverTheSizeLimit(t *testing.T) {
	path := seededStore(t)
	before := readFile(t, path)

	_, err := inbox.NewStore(path).Update(t.Context(), func(state *inbox.State) error {
		state.Put(inbox.Snooze{Key: strings.Repeat("k", 1<<20), Until: 0, SnoozedAt: 5})
		return nil
	})
	if !errors.Is(err, inbox.ErrTooBig) {
		t.Fatalf("Update() error = %v, want ErrTooBig", err)
	}

	requireFile(t, path, before)
}

func TestLoadRejectsMalformedFile(t *testing.T) {
	cases := []struct {
		name string
		data string
		want error
	}{
		{"newer version", `{"version":2,"snoozes":[],"unseen":[]}`, inbox.ErrVersion},
		{"unknown member", `{"version":1,"snoozes":[],"unseen":[],"pinned":[]}`, inbox.ErrInvalid},
		{"unknown snooze member", `{"version":1,"snoozes":[{"key":"a","until":0,"since":1,"note":"x"}],"unseen":[]}`, inbox.ErrInvalid},
		{"missing unseen list", `{"version":1,"snoozes":[]}`, inbox.ErrInvalid},
		{"missing snooze field", `{"version":1,"snoozes":[{"key":"a","until":0}],"unseen":[]}`, inbox.ErrInvalid},
		{"not json", `version 1`, inbox.ErrInvalid},
		{"duplicate key on disk", `{"version":1,"snoozes":[{"key":"a","until":0,"since":1},{"key":"a","until":0,"since":2}],"unseen":[]}`, inbox.ErrInvalid},
		{"oversized file", `{"version":1,"snoozes":[],"unseen":[]}` + strings.Repeat(" ", 1<<20), inbox.ErrTooBig},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := statePath(t)
			if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := inbox.NewStore(path).Load(t.Context()); !errors.Is(err, tc.want) {
				t.Errorf("Load() error = %v, want %v", err, tc.want)
			}

			if _, err := inbox.NewStore(path).Update(t.Context(), func(*inbox.State) error { return nil }); !errors.Is(err, tc.want) {
				t.Errorf("Update() error = %v, want %v", err, tc.want)
			}

			requireFile(t, path, []byte(tc.data))
		})
	}
}

func TestPutReplacesAnExistingSnooze(t *testing.T) {
	state := inbox.State{Version: 1, Snoozes: []inbox.Snooze{
		{Key: "agent/one", Until: 100, SnoozedAt: 10},
		{Key: "agent/two", Until: 0, SnoozedAt: 20},
	}}

	state.Put(inbox.Snooze{Key: "agent/one", Until: 300, SnoozedAt: 30})

	want := []inbox.Snooze{
		{Key: "agent/one", Until: 300, SnoozedAt: 30},
		{Key: "agent/two", Until: 0, SnoozedAt: 20},
	}
	if diff := cmp.Diff(want, state.Snoozes, sortSnoozes); diff != "" {
		t.Fatalf("Snoozes after Put() mismatch (-want +got):\n%s", diff)
	}
}

func TestRemoveDeletesOnlyTheGivenSnooze(t *testing.T) {
	state := inbox.State{Version: 1, Snoozes: []inbox.Snooze{
		{Key: "agent/one", Until: 100, SnoozedAt: 10},
		{Key: "agent/two", Until: 0, SnoozedAt: 20},
	}}

	state.Remove("agent/one")

	want := []inbox.Snooze{{Key: "agent/two", Until: 0, SnoozedAt: 20}}
	if diff := cmp.Diff(want, state.Snoozes); diff != "" {
		t.Fatalf("Snoozes after Remove() mismatch (-want +got):\n%s", diff)
	}
}

func TestMarkUnseenReplacesAndClearUnseenDeletesOnlyTheGivenKey(t *testing.T) {
	state := inbox.State{Version: 1, Unseen: []inbox.Unseen{
		{Key: "agent/one", At: 10},
		{Key: "agent/two", At: 20},
	}}

	state.MarkUnseen("agent/one", time.Unix(0, 30))
	state.ClearUnseen("agent/two")

	want := []inbox.Unseen{{Key: "agent/one", At: 30}}
	if diff := cmp.Diff(want, state.Unseen); diff != "" {
		t.Fatalf("Unseen mismatch (-want +got):\n%s", diff)
	}
}

func TestExpireDropsElapsedTimedSnoozesOnly(t *testing.T) {
	state := inbox.State{Version: 1, Snoozes: []inbox.Snooze{
		{Key: "elapsed", Until: 999, SnoozedAt: 1},
		{Key: "due now", Until: 1000, SnoozedAt: 1},
		{Key: "pending", Until: 1001, SnoozedAt: 1},
		{Key: "untimed", Until: 0, SnoozedAt: 1},
	}}

	state.Expire(time.Unix(1000, 500))

	want := []inbox.Snooze{
		{Key: "pending", Until: 1001, SnoozedAt: 1},
		{Key: "untimed", Until: 0, SnoozedAt: 1},
	}
	if diff := cmp.Diff(want, state.Snoozes, sortSnoozes); diff != "" {
		t.Fatalf("Snoozes after Expire() mismatch (-want +got):\n%s", diff)
	}
}

func TestConcurrentUpdatesFromSeparateStoresKeepEveryChange(t *testing.T) {
	path := statePath(t)
	keys := []string{"k0", "k1", "k2", "k3", "k4", "k5", "k6", "k7"}

	var wg sync.WaitGroup

	errs := make([]error, len(keys))
	for i, key := range keys {
		wg.Go(func() {
			_, errs[i] = inbox.NewStore(path).Update(t.Context(), func(state *inbox.State) error {
				state.Put(inbox.Snooze{Key: key, Until: 0, SnoozedAt: int64(i + 1)})
				return nil
			})
		})
	}

	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	loaded, err := inbox.NewStore(path).Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	got := make([]string, 0, len(loaded.Snoozes))
	for _, snooze := range loaded.Snoozes {
		got = append(got, snooze.Key)
	}

	if diff := cmp.Diff(keys, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Fatalf("snooze keys after concurrent updates mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdateDoesNotPublishAfterCancellation(t *testing.T) {
	t.Run("canceled before the call", func(t *testing.T) {
		path := seededStore(t)
		before := readFile(t, path)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := inbox.NewStore(path).Update(ctx, func(state *inbox.State) error {
			state.Remove("agent/seed")
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Update() error = %v, want context.Canceled", err)
		}

		requireFile(t, path, before)
	})

	t.Run("canceled during the change", func(t *testing.T) {
		path := seededStore(t)
		before := readFile(t, path)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		_, err := inbox.NewStore(path).Update(ctx, func(state *inbox.State) error {
			state.Remove("agent/seed")
			cancel()

			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Update() error = %v, want context.Canceled", err)
		}

		requireFile(t, path, before)
	})
}

var errChangeRefused = errors.New("change refused")

var sortSnoozes = cmpopts.SortSlices(func(a, b inbox.Snooze) bool { return a.Key < b.Key })

func statePath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "state", "inbox.json")
}

func seededStore(t *testing.T) string {
	t.Helper()

	path := statePath(t)

	_, err := inbox.NewStore(path).Update(t.Context(), func(state *inbox.State) error {
		state.Put(inbox.Snooze{Key: "agent/seed", Until: 0, SnoozedAt: 1})
		state.MarkUnseen("agent/seen", time.Unix(0, 2))

		return nil
	})
	if err != nil {
		t.Fatalf("seed Update() error = %v", err)
	}

	return path
}

func snoozes(count int) []inbox.Snooze {
	out := make([]inbox.Snooze, count)
	for i := range out {
		out[i] = inbox.Snooze{Key: fmt.Sprintf("agent/%d", i), Until: 0, SnoozedAt: 1}
	}

	return out
}

func unseenMarks(count int) []inbox.Unseen {
	out := make([]inbox.Unseen, count)
	for i := range out {
		out[i] = inbox.Unseen{Key: fmt.Sprintf("agent/%d", i), At: 1}
	}

	return out
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func requireFile(t *testing.T, path string, want []byte) {
	t.Helper()

	if got := readFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("state file changed:\n%s\nwant\n%s", got, want)
	}
}

func requireNoFile(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file %s exists or is unreadable: %v", path, err)
	}
}
