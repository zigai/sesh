package model

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/samber/oops"
	"pgregory.net/rapid"
)

func removalFixture() Manifest {
	return Manifest{
		Format: "sesh", Version: Schema, Started: 1, Finished: 2,
		Source: Source{
			TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "observed",
			Options:     []Option{{Scope: "server", Name: Bytes("@server"), Value: Bytes("server-value"), Present: true}},
			Environment: []Variable{{Name: Bytes("GLOBAL"), Value: Bytes("global"), State: "set"}},
		},
		Evidence: unverifiedEvidence(),
		Sessions: []Session{
			{
				ID: "s0", Name: Bytes("alpha"), Directory: Bytes("/srv/alpha"), Group: "g0",
				Options:     []Option{{Scope: "session", Name: Bytes("@prefix"), Value: Bytes("a"), Present: true}},
				Environment: []Variable{{Name: Bytes("TERM"), Value: Bytes("xterm"), State: "set"}},
			},
			{
				ID: "s1", Name: Bytes("beta"), Directory: Bytes("/srv/beta"), Group: "g0",
				Options:     []Option{{Scope: "session", Name: Bytes("@prefix"), Value: Bytes("b"), Present: true}},
				Environment: []Variable{{Name: Bytes("TERM"), Value: Bytes("screen"), State: "set"}},
			},
			{ID: "s2", Name: Bytes("gamma"), Directory: Bytes("/srv/gamma"), Group: ""},
			{
				ID: "s3", Name: Bytes("delta"), Directory: Bytes("/srv/delta"), Group: "g1",
				Options:     []Option{{Scope: "session", Name: Bytes("@unset"), Value: nil, Present: false, Inherited: true}},
				Environment: []Variable{{Name: Bytes("GONE"), Value: nil, State: "removed"}},
			},
			{ID: "s4", Name: Bytes("epsilon"), Directory: Bytes("/srv/epsilon"), Group: ""},
		},
		Groups: []Group{
			{ID: "g0", Name: Bytes("shared"), Members: []string{"s0", "s1"}},
			{ID: "g1", Name: Bytes("solo"), Members: []string{"s3"}},
		},
		Windows: []Window{
			{
				ID: "w0", Name: Bytes("editor"), Width: 80, Height: 24,
				Layout: Layout{Width: 80, Height: 24, Pane: "p0"},
				Options: []Option{
					{Scope: "window", Name: Bytes("pane-border-status"), Value: Bytes("top"), Present: true},
					{Scope: "window", Name: Bytes("@sticky"), Value: Bytes("yes"), Present: true},
				},
			},
			{
				ID: "w1", Name: Bytes("split"), Width: 80, Height: 24,
				Layout: Layout{Width: 80, Height: 24, Axis: "x", Children: []Layout{
					{Width: 40, Height: 24, Left: 0, Top: 0, Pane: "p1"},
					{Width: 39, Height: 24, Left: 41, Top: 0, Pane: "p2"},
				}},
				Options: []Option{{Scope: "window", Name: Bytes("pane-border-status"), Value: Bytes("bottom"), Present: true}},
			},
			{
				ID: "w2", Name: Bytes("zoomed"), Width: 100, Height: 30, Zoomed: true,
				Layout: Layout{Width: 100, Height: 30, Axis: "x", Children: []Layout{
					{Width: 50, Height: 30, Left: 0, Top: 0, Pane: "p3"},
					{Width: 49, Height: 30, Left: 51, Top: 0, Pane: "p4"},
				}},
				Options: []Option{{Scope: "window", Name: Bytes("@zoom"), Value: Bytes("kept"), Present: true}},
			},
		},
		Links: []Link{
			{Session: "s0", Window: "w0", Index: 3, Active: true, Flags: Bytes("*")},
			{Session: "s1", Window: "w0", Index: 3, Active: true, Flags: Bytes("*")},
			{Session: "s2", Window: "w0", Index: 7, Active: true, Flags: Bytes("*")},
			{Session: "s2", Window: "w0", Index: 11, Last: true, Flags: Bytes("-")},
			{Session: "s3", Window: "w1", Index: 0, Active: true, Flags: Bytes("*")},
			{Session: "s4", Window: "w2", Index: 2, Active: true, Flags: Bytes("*")},
		},
		Panes: []Pane{
			{
				ID: "p0", Window: "w0", Index: 0, Title: Bytes("banner"), Directory: Bytes("/srv/alpha"),
				Width: 80, Height: 23, Left: 0, Top: 1, Active: true,
				Terminal: Terminal{Artifact: "a0", History: 120, CursorX: 3, CursorY: 2, Mode: Bytes("k")},
				Launch:   Launch{Recovery: "unverified", Birth: Bytes("1700000000:11")},
			},
			{
				ID: "p1", Window: "w1", Index: 0, Title: Bytes("left"), Directory: Bytes("/srv/delta"),
				Width: 40, Height: 23, Left: 0, Top: 0, Active: true,
				Terminal: Terminal{Artifact: "a1", History: 5, CursorX: 0, CursorY: 0, Mode: Bytes("")},
				Launch:   Launch{Recovery: "unverified"},
			},
			{
				ID: "p2", Window: "w1", Index: 5, Title: Bytes("right"), Directory: Bytes("/srv/delta"),
				Width: 39, Height: 23, Left: 41, Top: 0, Dead: true,
				Terminal: Terminal{Artifact: "a1", History: 6, CursorX: 1, CursorY: 4, Mode: Bytes("a")},
				Launch:   Launch{Recovery: "unverified"},
			},
			{
				ID: "p3", Window: "w2", Index: 4, Title: Bytes("zoom"), Directory: Bytes("/srv/epsilon"),
				Width: 100, Height: 30, Left: 0, Top: 0, Active: true,
				Terminal: Terminal{Artifact: "a2", History: 9, Alternate: true, CursorX: 7, CursorY: 8, Mode: Bytes("ks")},
				Launch: Launch{
					Executable:  Bytes("/bin/tool"),
					Arguments:   []Bytes{Bytes("tool"), {'r', 'a', 'w', 0xff}},
					Directory:   Bytes("/srv/epsilon"),
					Environment: []Variable{{Name: Bytes("PATH"), Value: Bytes("/usr/bin"), State: "set"}},
					Birth:       Bytes("1700000000:99"),
					Recovery:    "restart",
				},
			},
			{
				ID: "p4", Window: "w2", Index: 6, Title: Bytes("hidden"), Directory: Bytes("/srv/epsilon"),
				Width: 49, Height: 30, Left: 51, Top: 0,
				Terminal: Terminal{Artifact: "", History: 0, CursorX: 0, CursorY: 0, Mode: Bytes("")},
				Launch:   Launch{Recovery: "unverified"},
			},
		},
		Artifacts: []Artifact{
			{ID: "a0", Kind: "terminal-text", Length: 2048, SHA256: strings.Repeat("a1", 32)},
			{ID: "a1", Kind: "terminal-text", Length: 512, SHA256: strings.Repeat("b2", 32)},
			{ID: "a2", Kind: "terminal-text", Length: 64, SHA256: strings.Repeat("c3", 32)},
		},
	}
}

func unverifiedEvidence() []Evidence {
	domains := []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"}
	evidence := make([]Evidence, 0, len(domains))

	for _, domain := range domains {
		evidence = append(evidence, Evidence{Domain: domain, Status: "unverified", Reason: "not_captured"})
	}

	return evidence
}

func removeSessions(tb rapid.TB, m Manifest, selected []string) Removal {
	tb.Helper()

	removal, err := RemoveSessions(m, selected)
	if err != nil {
		tb.Fatal(err)
	}

	return removal
}

func sessionIdentity(session Session) string {
	return session.ID
}

func windowIdentity(window Window) string {
	return window.ID
}

func paneIdentity(pane Pane) string {
	return pane.ID
}

func artifactIdentity(artifact Artifact) string {
	return artifact.ID
}

func linkKey(link Link) string {
	return link.Session + "@" + strconv.Itoa(link.Index)
}

func indexSessions(m Manifest) map[string]Session {
	index := make(map[string]Session, len(m.Sessions))
	for _, session := range m.Sessions {
		index[session.ID] = session
	}

	return index
}

func indexWindows(m Manifest) map[string]Window {
	index := make(map[string]Window, len(m.Windows))
	for _, window := range m.Windows {
		index[window.ID] = window
	}

	return index
}

func indexPanes(m Manifest) map[string]Pane {
	index := make(map[string]Pane, len(m.Panes))
	for _, pane := range m.Panes {
		index[pane.ID] = pane
	}

	return index
}

func indexArtifacts(m Manifest) map[string]Artifact {
	index := make(map[string]Artifact, len(m.Artifacts))
	for _, artifact := range m.Artifacts {
		index[artifact.ID] = artifact
	}

	return index
}

func indexGroups(m Manifest) map[string]Group {
	index := make(map[string]Group, len(m.Groups))
	for _, group := range m.Groups {
		index[group.ID] = group
	}

	return index
}

func indexLinks(m Manifest) map[string]Link {
	index := make(map[string]Link, len(m.Links))
	for _, link := range m.Links {
		index[linkKey(link)] = link
	}

	return index
}

func ids[T any](values []T, identity func(T) string) []string {
	if len(values) == 0 {
		return nil
	}

	identities := make([]string, 0, len(values))
	for _, value := range values {
		identities = append(identities, identity(value))
	}

	slices.Sort(identities)

	return identities
}

func assertRetainedEqual[T any](tb rapid.TB, retained []T, source map[string]T, identity func(T) string, missing string) {
	tb.Helper()

	for _, value := range retained {
		original, found := source[identity(value)]
		if !found {
			tb.Fatal(missing)
		}

		if diff := cmp.Diff(original, value); diff != "" {
			tb.Fatal(diff)
		}
	}
}

func assertSurvivors(tb rapid.TB, source, retained Manifest) {
	tb.Helper()

	assertRetainedEqual(tb, retained.Sessions, indexSessions(source), sessionIdentity, "retained session is absent from the source graph")
	assertRetainedEqual(tb, retained.Windows, indexWindows(source), windowIdentity, "retained window is absent from the source graph")
	assertRetainedEqual(tb, retained.Panes, indexPanes(source), paneIdentity, "retained pane is absent from the source graph")
	assertRetainedEqual(tb, retained.Artifacts, indexArtifacts(source), artifactIdentity, "retained artifact is absent from the source graph")
	assertRetainedEqual(tb, retained.Links, indexLinks(source), linkKey, "retained link is absent from the source graph")
	assertGroupNames(tb, source, retained)
	assertProvenanceRetained(tb, source, retained)
}

func assertGroupNames(tb rapid.TB, source, retained Manifest) {
	tb.Helper()

	groups := indexGroups(source)

	for _, group := range retained.Groups {
		original, found := groups[group.ID]
		if !found {
			tb.Fatal("retained group is absent from the source graph")
		}

		if diff := cmp.Diff(original.Name, group.Name); diff != "" {
			tb.Fatal(diff)
		}
	}
}

func assertProvenanceRetained(tb rapid.TB, source, retained Manifest) {
	tb.Helper()

	if diff := cmp.Diff(source.Source, retained.Source); diff != "" {
		tb.Fatal(diff)
	}

	if diff := cmp.Diff(source.Evidence, retained.Evidence); diff != "" {
		tb.Fatal(diff)
	}

	if retained.Format != source.Format || retained.Version != source.Version ||
		retained.Started != source.Started || retained.Finished != source.Finished {
		tb.Fatal("retained provenance changed")
	}
}

func scrubManifest(m *Manifest) {
	m.Format, m.Source.TmuxVersion = "scrubbed", "scrubbed"
	m.Source.Options = scrubOptions(m.Source.Options)
	m.Source.Environment = scrubVariables(m.Source.Environment)

	for index := range m.Evidence {
		m.Evidence[index] = Evidence{Domain: "S01", Status: "verified"}
	}

	for index := range m.Sessions {
		clobber(&m.Sessions[index].Name)
		clobber(&m.Sessions[index].Directory)
		m.Sessions[index].Options = scrubOptions(m.Sessions[index].Options)
		m.Sessions[index].Environment = scrubVariables(m.Sessions[index].Environment)
	}

	for index := range m.Groups {
		clobber(&m.Groups[index].Name)

		for member := range m.Groups[index].Members {
			m.Groups[index].Members[member] = "scrubbed"
		}
	}

	for index := range m.Windows {
		clobber(&m.Windows[index].Name)
		m.Windows[index].Options = scrubOptions(m.Windows[index].Options)
		scrubLayout(&m.Windows[index].Layout)
	}

	for index := range m.Links {
		clobber(&m.Links[index].Flags)
	}

	for index := range m.Panes {
		clobber(&m.Panes[index].Title)
		clobber(&m.Panes[index].Directory)
		m.Panes[index].Options = scrubOptions(m.Panes[index].Options)
		clobber(&m.Panes[index].Terminal.Mode)
		clobber(&m.Panes[index].Launch.Executable)
		clobber(&m.Panes[index].Launch.Directory)
		clobber(&m.Panes[index].Launch.Birth)

		for argument := range m.Panes[index].Launch.Arguments {
			clobber(&m.Panes[index].Launch.Arguments[argument])
		}

		m.Panes[index].Launch.Environment = scrubVariables(m.Panes[index].Launch.Environment)
	}
}

func scrubOptions(options []Option) []Option {
	for index := range options {
		clobber(&options[index].Name)
		clobber(&options[index].Value)
	}

	return options
}

func scrubVariables(environment []Variable) []Variable {
	for index := range environment {
		clobber(&environment[index].Name)
		clobber(&environment[index].Value)
	}

	return environment
}

func scrubLayout(layout *Layout) {
	layout.Width++
	layout.Pane = "scrubbed-pane"

	for index := range layout.Children {
		scrubLayout(&layout.Children[index])
	}
}

func clobber(value *Bytes) {
	for index := range *value {
		(*value)[index] = 'Z'
	}
}

func encodeOriginal(tb rapid.TB, m Manifest) []byte {
	tb.Helper()

	data, err := Encode(m)
	if err != nil {
		tb.Fatal(err)
	}

	return data
}

func TestRemovalKeepsSharedWindowAndTrimsGroups(t *testing.T) {
	m := removalFixture()

	removal := removeSessions(t, m, []string{"s2"})
	checkSurvivorGraph(t, m, []string{"s2"}, removal)

	if diff := cmp.Diff([]string{"s0", "s1"}, indexGroups(removal.Retained)["g0"].Members); diff != "" {
		t.Fatal("group membership was not preserved", diff)
	}

	if _, found := indexSessions(removal.Retained)["s2"]; found {
		t.Fatal("removed session survived")
	}

	if _, found := indexLinks(removal.Retained)[linkKey(Link{Session: "s2", Index: 11})]; found {
		t.Fatal("removed session retained a link")
	}
}

func TestRemovalDropsUnreferencedWindowPanesAndArtifacts(t *testing.T) {
	m := removalFixture()

	removal := removeSessions(t, m, []string{"s3"})
	checkSurvivorGraph(t, m, []string{"s3"}, removal)

	if diff := cmp.Diff([]string{"a1"}, removal.Artifacts); diff != "" {
		t.Fatal("artifact shared by the removed panes was not reported", diff)
	}

	if diff := cmp.Diff([]string{"w0", "w2"}, ids(removal.Retained.Windows, windowIdentity)); diff != "" {
		t.Fatal(diff)
	}
}

func TestRemovalKeepsOneMemberGroup(t *testing.T) {
	m := removalFixture()

	removal := removeSessions(t, m, []string{"s1"})
	checkSurvivorGraph(t, m, []string{"s1"}, removal)

	group, found := indexGroups(removal.Retained)["g0"]
	if !found {
		t.Fatal("group with one remaining member was removed")
	}

	if diff := cmp.Diff([]string{"s0"}, group.Members); diff != "" {
		t.Fatal(diff)
	}

	if session := indexSessions(removal.Retained)["s0"]; session.Group != "g0" {
		t.Fatal("surviving member lost its group reference")
	}
}

func TestRemovalKeepsWindowsSharedWithUnrelatedSessions(t *testing.T) {
	m := removalFixture()

	removal := removeSessions(t, m, []string{"s0", "s1"})
	checkSurvivorGraph(t, m, []string{"s0", "s1"}, removal)

	links := indexLinks(removal.Retained)
	for _, index := range []int{7, 11} {
		if link, found := links[linkKey(Link{Session: "s2", Index: index})]; !found || link.Window != "w0" {
			t.Fatal("removal dropped a link to a window another session still uses", link)
		}
	}

	windows := ids(removal.Retained.Windows, windowIdentity)
	if diff := cmp.Diff([]string{"w0", "w1", "w2"}, windows); diff != "" {
		t.Fatal(diff)
	}

	if len(indexPanes(removal.Retained)) != 5 || len(indexArtifacts(removal.Retained)) != 3 {
		t.Fatal("shared windows lost their panes or artifacts")
	}
}

func TestRemovalOfEverySessionIsExplicitEmptyResult(t *testing.T) {
	m := removalFixture()

	selection := []string{"s3", "s0", "s1", "s2", "s4", "s2"}
	removal := removeSessions(t, m, selection)

	if !removal.Empty() {
		t.Fatal("removal of every session reported a restorable result")
	}

	if diff := cmp.Diff(Manifest{}, removal.Retained); diff != "" {
		t.Fatal("empty result carried a partial graph", diff)
	}

	if diff := cmp.Diff([]string{"s0", "s1", "s2", "s3", "s4"}, removal.Removed); diff != "" {
		t.Fatal(diff)
	}

	if diff := cmp.Diff([]string{"a0", "a1", "a2"}, removal.Artifacts); diff != "" {
		t.Fatal(diff)
	}

	if _, err := Encode(removal.Retained); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty result was serializable as a snapshot", err)
	}

	if _, err := RemoveSessions(removal.Retained, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty result was accepted as a source graph", err)
	}
}

func TestEmptySelectionReturnsIndependentCopy(t *testing.T) {
	m := removalFixture()

	for _, selection := range [][]string{nil, {}} {
		removal := removeSessions(t, m, selection)

		if removal.Empty() {
			t.Fatal("empty selection emptied the graph")
		}

		if diff := cmp.Diff(m, removal.Retained); diff != "" {
			t.Fatal(diff)
		}

		if !bytes.Equal(encodeOriginal(t, m), encodeOriginal(t, removal.Retained)) {
			t.Fatal("empty selection changed the retained encoding")
		}

		if removal.Removed != nil || removal.Artifacts != nil {
			t.Fatal("empty selection reported removed entities", removal.Removed, removal.Artifacts)
		}
	}
}

func TestRemovalDoesNotAliasSourceNestedState(t *testing.T) {
	for name, selection := range map[string][]string{"partial": {"s2"}, "none": nil} {
		t.Run(name, func(t *testing.T) {
			source := removalFixture()
			sourceBytes := encodeOriginal(t, source)

			removal := removeSessions(t, source, selection)
			retainedBytes := encodeOriginal(t, removal.Retained)

			scrubManifest(&removal.Retained)

			if data := encodeOriginal(t, source); !bytes.Equal(data, sourceBytes) {
				t.Fatal("result shares nested state with the source graph")
			}

			second := removeSessions(t, source, selection)
			scrubManifest(&source)

			if data := encodeOriginal(t, second.Retained); !bytes.Equal(data, retainedBytes) {
				t.Fatal("source graph shares nested state with the result")
			}
		})
	}
}

func TestRemovalIsDeterministic(t *testing.T) {
	m := removalFixture()

	first := removeSessions(t, m, []string{"s3", "s0"})
	second := removeSessions(t, m, []string{"s0", "s3", "s0"})

	if diff := cmp.Diff(first.Retained, second.Retained); diff != "" {
		t.Fatal(diff)
	}

	if diff := cmp.Diff(first.Removed, second.Removed); diff != "" {
		t.Fatal(diff)
	}

	if diff := cmp.Diff(first.Artifacts, second.Artifacts); diff != "" {
		t.Fatal(diff)
	}

	if !bytes.Equal(encodeOriginal(t, first.Retained), encodeOriginal(t, second.Retained)) {
		t.Fatal("retained encoding is not deterministic")
	}

	reordered := removalFixture()
	slices.Reverse(reordered.Sessions)

	removal := removeSessions(t, reordered, []string{"s4", "s0"})
	if diff := cmp.Diff([]string{"s0", "s4"}, removal.Removed); diff != "" {
		t.Fatal(diff)
	}

	order := make([]string, 0, len(removal.Retained.Sessions))
	for _, session := range removal.Retained.Sessions {
		order = append(order, session.ID)
	}

	if diff := cmp.Diff([]string{"s3", "s2", "s1"}, order); diff != "" {
		t.Fatal("survivors did not keep their source order", diff)
	}
}

func TestRemovalPreservesSparseIndexesLinksAndLayout(t *testing.T) {
	m := removalFixture()
	original := indexWindows(m)

	removal := removeSessions(t, m, []string{"s3"})

	expected := map[string]Link{
		"s0@3":  {Session: "s0", Window: "w0", Index: 3, Active: true, Flags: Bytes("*")},
		"s1@3":  {Session: "s1", Window: "w0", Index: 3, Active: true, Flags: Bytes("*")},
		"s2@7":  {Session: "s2", Window: "w0", Index: 7, Active: true, Flags: Bytes("*")},
		"s2@11": {Session: "s2", Window: "w0", Index: 11, Last: true, Flags: Bytes("-")},
		"s4@2":  {Session: "s4", Window: "w2", Index: 2, Active: true, Flags: Bytes("*")},
	}
	if diff := cmp.Diff(expected, indexLinks(removal.Retained)); diff != "" {
		t.Fatal(diff)
	}

	retained := indexWindows(removal.Retained)

	if diff := cmp.Diff(original["w0"].Options, retained["w0"].Options); diff != "" {
		t.Fatal("window options changed", diff)
	}

	if diff := cmp.Diff(original["w0"].Layout, retained["w0"].Layout); diff != "" {
		t.Fatal("window layout changed", diff)
	}

	if !retained["w2"].Zoomed {
		t.Fatal("zoomed window flag changed")
	}

	if diff := cmp.Diff(original["w2"].Layout, retained["w2"].Layout); diff != "" {
		t.Fatal("zoomed window layout changed", diff)
	}

	panes := indexPanes(removal.Retained)
	if panes["p0"].Top != 1 || panes["p0"].Height != 23 || panes["p0"].Left != 0 {
		t.Fatal("pane geometry changed")
	}

	indexes := make(map[string]int, len(panes))
	for id, pane := range panes {
		indexes[id] = pane.Index
	}

	if diff := cmp.Diff(map[string]int{"p0": 0, "p3": 4, "p4": 6}, indexes); diff != "" {
		t.Fatal("pane indexes changed", diff)
	}
}

func TestRemovalRejectsMalformedAndUnknownSelection(t *testing.T) {
	m := removalFixture()

	cases := map[string]struct {
		selection []string
		reason    string
	}{
		"malformed upper case": {[]string{"S0"}, "malformed_session"},
		"malformed separator":  {[]string{"s_0"}, "malformed_session"},
		"malformed traversal":  {[]string{"../s0"}, "malformed_session"},
		"malformed empty":      {[]string{""}, "malformed_session"},
		"malformed long":       {[]string{strings.Repeat("a", 65)}, "malformed_session"},
		"unknown identity":     {[]string{"s9"}, "unknown_session"},
		"unknown hexadecimal":  {[]string{"9f9f"}, "unknown_session"},
		"name is not identity": {[]string{"alpha"}, "unknown_session"},
		"mixed with unknown":   {[]string{"s0", "s9"}, "unknown_session"},
		"mixed with malformed": {[]string{"s0", "s1_"}, "malformed_session"},
		"excessive bounds":     {slices.Repeat([]string{"s0"}, MaxEntities+1), "bounds"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			removal, err := RemoveSessions(m, testCase.selection)
			if !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid selection was accepted", err)
			}

			diagnostic, ok := oops.AsOops(err)
			if !ok || diagnostic.Code() != "snapshot_invalid" {
				t.Fatal("missing structured selection code")
			}

			context := diagnostic.Context()
			if context["operation"] != "remove" || context["phase"] != "selection" || context["reason"] != testCase.reason {
				t.Fatal("missing selection classification", context)
			}

			if diff := cmp.Diff(Manifest{}, removal.Retained); diff != "" {
				t.Fatal("failed selection returned a partial graph", diff)
			}

			if len(removal.Removed) != 0 || len(removal.Artifacts) != 0 {
				t.Fatal("failed selection reported removed entities")
			}
		})
	}
}

func TestRemovalValidatesSourceGraphFirst(t *testing.T) {
	m := removalFixture()
	m.Links[0].Window = "missing-window"

	_, err := RemoveSessions(m, []string{"unknown-identity"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid graph was accepted", err)
	}

	if diagnostic, ok := oops.AsOops(err); !ok || diagnostic.Context()["operation"] != "validate" {
		t.Fatal("selection error replaced the graph error", diagnostic)
	}

	if _, err := RemoveSessions(Manifest{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero manifest was accepted", err)
	}

	mVersion := removalFixture()
	mVersion.Version = Schema + 1

	_, errVersion := RemoveSessions(mVersion, nil)
	if !errors.Is(errVersion, ErrVersion) {
		t.Fatal("unsupported version error was not preserved", errVersion)
	}

	if diagnostic, ok := oops.AsOops(errVersion); !ok || diagnostic.Code() != "snapshot_version_unsupported" {
		t.Fatal("wrong diagnostic code for unsupported version", diagnostic)
	}
}

func TestRemovalDoesNotUpgradeEvidence(t *testing.T) {
	m := removalFixture()
	m.Source.Consistency = "checkpoint"

	if m.Complete() {
		t.Fatal("invalid synthetic complete fixture")
	}

	removal := removeSessions(t, m, []string{"s0", "s1", "s2"})

	if removal.Retained.Complete() {
		t.Fatal("rewrite upgraded unverified evidence")
	}

	for _, evidence := range removal.Retained.Evidence {
		if evidence.Status != "unverified" || evidence.Reason != "not_captured" {
			t.Fatal("evidence changed during removal", evidence)
		}
	}

	if removal.Retained.Source.Consistency != "checkpoint" {
		t.Fatal("source provenance changed during removal")
	}
}

func TestRemovalPreservesNilSlices(t *testing.T) {
	m := fixture()
	m.Groups = nil
	m.Sessions[0].Group = ""
	m.Sessions[1].Group = ""

	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, selection := range [][]string{nil, {"s1"}} {
		removal := removeSessions(t, m, selection)

		if removal.Retained.Groups != nil {
			t.Fatalf("Groups became non-nil: %#v", removal.Retained.Groups)
		}

		if removal.Retained.Artifacts != nil {
			t.Fatalf("Artifacts became non-nil: %#v", removal.Retained.Artifacts)
		}

		if err := removal.Retained.Validate(); err != nil {
			t.Fatal("retained graph is invalid", err)
		}

		if selection == nil {
			if diff := cmp.Diff(m, removal.Retained); diff != "" {
				t.Fatal("no-op removal perturbed manifest with nil slices", diff)
			}
		}
	}
}

func TestRemovalResultSlicesDoNotAlias(t *testing.T) {
	m := removalFixture()

	first := removeSessions(t, m, []string{"s3"})
	if len(first.Removed) == 0 || len(first.Artifacts) == 0 {
		t.Fatal("expected removed sessions and dropped artifacts")
	}

	first.Removed[0] = "corrupted"
	first.Artifacts[0] = "corrupted"

	second := removeSessions(t, m, []string{"s3"})
	if second.Removed[0] == "corrupted" || second.Artifacts[0] == "corrupted" {
		t.Fatal("mutating result slices altered subsequent removal results")
	}
}
