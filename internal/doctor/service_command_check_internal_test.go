//go:build linux || darwin

package doctor

import (
	"slices"
	"testing"
)

func TestSplitExecLineFollowsSystemdQuotingAndPrefixes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value string
		want  []string
	}{
		{value: `/bin/sesh action autosave-final-save --deadline 20s`, want: []string{"/bin/sesh", "action", "autosave-final-save", "--deadline", "20s"}},
		{value: `-/bin/sesh daemon tmux --socket "/tmp/a b"`, want: []string{"/bin/sesh", "daemon", "tmux", "--socket", "/tmp/a b"}},
		{value: `  /bin/sesh   --name 'it''s' "q\"uote"`, want: []string{"/bin/sesh", "--name", "its", `q"uote`}},
		{value: `/bin/sesh --path "%%h/$$HOME" ""`, want: []string{"/bin/sesh", "--path", "%h/$HOME", ""}},
		{value: `@/bin/sesh sesh-alias daemon coordinator`, want: []string{"/bin/sesh", "daemon", "coordinator"}},
		{value: `:+!/bin/sesh doctor`, want: []string{"/bin/sesh", "doctor"}},
	} {
		if got := splitExecLine(tc.value); !slices.Equal(got, tc.want) {
			t.Errorf("splitExecLine(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestUnitLinesJoinContinuationsAndDropComments(t *testing.T) {
	t.Parallel()

	data := []byte("# ExecStart=/bin/sesh old\n[Service]\nExecStart=/bin/sesh daemon \\\n  coordinator\n; note\n")

	want := []string{"[Service]", "ExecStart=/bin/sesh daemon  coordinator"}
	if got := unitLines(data); !slices.Equal(got, want) {
		t.Fatalf("unitLines = %q, want %q", got, want)
	}
}
