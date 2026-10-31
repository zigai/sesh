//go:build linux || darwin

package store

import (
	"errors"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

func TestRepositoryHistoriesNeverFallback(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		outcomes := rapid.SliceOfN(rapid.SampledFrom([]string{"committed", "pending", "failed", "incomplete"}), 1, 12).Draw(rt, "outcomes")
		r := openRepository(t, filepath.Join(t.TempDir(), "repository"))

		var committed []string

		for _, outcome := range outcomes {
			if id := applySaveOutcome(t, r, outcome); id != "" {
				committed = append(committed, id)
			}

			var want error

			switch {
			case len(committed) == 0 && outcome != "pending":
				want = ErrUnavailable
			case outcome != "committed":
				want = ErrRecoveryRequired
			}

			_, err := r.Current(t.Context())
			if !errors.Is(err, want) {
				rt.Fatalf("current after %s: %v, want %v", outcome, err, want)
			}

			for _, id := range committed {
				if _, err := r.Read(t.Context(), id); err != nil {
					rt.Fatal("prior explicit source lost", err)
				}
			}
		}
	})
}

func applySaveOutcome(t *testing.T, r *Repository, outcome string) string {
	t.Helper()

	switch outcome {
	case "committed":
		return commitCompleteFixture(t, r).ID
	case "incomplete":
		return commitFixture(t, r).ID
	default:
		attempt, err := r.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		if outcome == "failed" {
			if err := r.Fail(t.Context(), attempt.ID); err != nil {
				t.Fatal(err)
			}
		}

		return ""
	}
}
