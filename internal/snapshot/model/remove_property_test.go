package model

import (
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

type survivorSets struct {
	sessionIDs   map[string]bool
	windowIDs    map[string]bool
	linkKeys     map[string]bool
	paneIDs      map[string]bool
	artifactIDs  map[string]bool
	groupMembers map[string][]string
}

func expectedSurvivors(m Manifest, removed map[string]bool) survivorSets {
	sessions := make(map[string]bool, len(m.Sessions))
	for _, session := range m.Sessions {
		if !removed[session.ID] {
			sessions[session.ID] = true
		}
	}

	windows := make(map[string]bool, len(m.Windows))
	links := make(map[string]bool, len(m.Links))

	for _, link := range m.Links {
		if sessions[link.Session] {
			windows[link.Window] = true
			links[linkKey(link)] = true
		}
	}

	panes := make(map[string]bool, len(m.Panes))
	artifacts := make(map[string]bool, len(m.Artifacts))

	for _, pane := range m.Panes {
		if !windows[pane.Window] {
			continue
		}

		panes[pane.ID] = true

		if pane.Terminal.Artifact != "" {
			artifacts[pane.Terminal.Artifact] = true
		}
	}

	groups := make(map[string][]string, len(m.Groups))

	for _, group := range m.Groups {
		members := make([]string, 0, len(group.Members))

		for _, member := range group.Members {
			if sessions[member] {
				members = append(members, member)
			}
		}

		if len(members) > 0 {
			groups[group.ID] = members
		}
	}

	return survivorSets{
		sessionIDs: sessions, windowIDs: windows, linkKeys: links,
		paneIDs: panes, artifactIDs: artifacts, groupMembers: groups,
	}
}

func sortedKeys(values map[string]bool) []string {
	return slices.Sorted(maps.Keys(values))
}

func checkSurvivorGraph(tb rapid.TB, m Manifest, selection []string, removal Removal) {
	tb.Helper()

	removed := make(map[string]bool, len(selection))
	for _, id := range selection {
		removed[id] = true
	}

	want := expectedSurvivors(m, removed)

	checkEmptyResult(tb, m, want, removal)
	checkRetainedSets(tb, want, removal)
	checkRemovedSets(tb, m, want, removal)
}

func checkEmptyResult(tb rapid.TB, m Manifest, want survivorSets, removal Removal) {
	tb.Helper()

	if len(want.sessionIDs) == 0 {
		if !removal.Empty() {
			tb.Fatal("removal of every session reported a restorable result")
		}

		if diff := cmp.Diff(Manifest{}, removal.Retained); diff != "" {
			tb.Fatal("empty result carried a partial graph\n", diff)
		}

		return
	}

	if removal.Empty() {
		tb.Fatal("surviving sessions were reported as an empty result")
	}

	if err := removal.Retained.Validate(); err != nil {
		tb.Fatal("survivor graph is invalid\n", err)
	}

	assertSurvivors(tb, m, removal.Retained)
}

func checkRetainedSets(tb rapid.TB, want survivorSets, removal Removal) {
	tb.Helper()

	retained := removal.Retained

	checks := []struct {
		family string
		want   []string
		got    []string
	}{
		{"sessions", sortedKeys(want.sessionIDs), ids(retained.Sessions, sessionIdentity)},
		{"windows", sortedKeys(want.windowIDs), ids(retained.Windows, windowIdentity)},
		{"links", sortedKeys(want.linkKeys), ids(retained.Links, linkKey)},
		{"panes", sortedKeys(want.paneIDs), ids(retained.Panes, paneIdentity)},
		{"artifacts", sortedKeys(want.artifactIDs), ids(retained.Artifacts, artifactIdentity)},
	}

	for _, check := range checks {
		if diff := cmp.Diff(check.want, check.got); diff != "" {
			tb.Fatalf("unexpected retained %s\n%s", check.family, diff)
		}
	}

	groups := make(map[string][]string, len(retained.Groups))
	for _, group := range retained.Groups {
		groups[group.ID] = group.Members
	}

	if diff := cmp.Diff(want.groupMembers, groups); diff != "" {
		tb.Fatal("unexpected retained groups\n", diff)
	}
}

func checkRemovedSets(tb rapid.TB, m Manifest, want survivorSets, removal Removal) {
	tb.Helper()

	removed := make(map[string]bool, len(m.Sessions))
	for _, session := range m.Sessions {
		if !want.sessionIDs[session.ID] {
			removed[session.ID] = true
		}
	}

	if diff := cmp.Diff(sortedKeys(removed), removal.Removed); diff != "" {
		tb.Fatal("unexpected removed sessions\n", diff)
	}

	var dropped []string

	for _, artifact := range m.Artifacts {
		if !want.artifactIDs[artifact.ID] {
			dropped = append(dropped, artifact.ID)
		}
	}

	if diff := cmp.Diff(dropped, removal.Artifacts); diff != "" {
		tb.Fatal("unexpected removed artifacts\n", diff)
	}
}

func TestRemovalSurvivorGraphProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := drawManifest(rt)
		if err := m.Validate(); err != nil {
			rt.Fatal("generator produced an invalid graph\n", err)
		}

		selection := drawSelection(rt, m)

		removal, err := RemoveSessions(m, selection)
		if err != nil {
			rt.Fatal(err)
		}

		checkSurvivorGraph(rt, m, selection, removal)
	})
}

func TestRemovalIsMonotoneUnderLargerSelections(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := drawManifest(rt)
		selection := drawSelection(rt, m)

		subset := make([]string, 0, len(selection))
		for _, id := range selection {
			if rapid.Bool().Draw(rt, "inSubset") {
				subset = append(subset, id)
			}
		}

		small, err := RemoveSessions(m, subset)
		if err != nil {
			rt.Fatal(err)
		}

		large, err := RemoveSessions(m, selection)
		if err != nil {
			rt.Fatal(err)
		}

		if small.Empty() && !large.Empty() {
			rt.Fatal("a larger removal retained more sessions")
		}

		if !withinCounts(entityCounts(large.Retained), entityCounts(small.Retained)) {
			rt.Fatalf("larger removal retained more entities: %v > %v", entityCounts(large.Retained), entityCounts(small.Retained))
		}

		if !subsetOf(small.Removed, large.Removed) {
			rt.Fatal("larger removal did not report every smaller removal")
		}

		if !subsetOf(small.Artifacts, large.Artifacts) {
			rt.Fatal("larger removal did not report every smaller artifact removal")
		}

		assertRetainedEqual(rt, large.Retained.Sessions, indexSessions(small.Retained), sessionIdentity, sharedSurvivor)
		assertRetainedEqual(rt, large.Retained.Windows, indexWindows(small.Retained), windowIdentity, sharedSurvivor)
		assertRetainedEqual(rt, large.Retained.Panes, indexPanes(small.Retained), paneIdentity, sharedSurvivor)
		assertRetainedEqual(rt, large.Retained.Artifacts, indexArtifacts(small.Retained), artifactIdentity, sharedSurvivor)
		assertRetainedEqual(rt, large.Retained.Links, indexLinks(small.Retained), linkKey, sharedSurvivor)
	})
}

const sharedSurvivor = "larger removal perturbed a shared survivor"

func TestRemovalCommutesAcrossDisjointSelections(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := drawManifest(rt)

		unique := distinct(drawSelection(rt, m))
		first, second := splitSelection(rt, unique)

		withoutFirst, err := RemoveSessions(m, first)
		if err != nil {
			rt.Fatal(err)
		}

		withoutSecond, err := RemoveSessions(m, second)
		if err != nil {
			rt.Fatal(err)
		}

		if withoutFirst.Empty() || withoutSecond.Empty() {
			return
		}

		firstThenSecond, err := RemoveSessions(withoutFirst.Retained, second)
		if err != nil {
			rt.Fatal(err)
		}

		secondThenFirst, err := RemoveSessions(withoutSecond.Retained, first)
		if err != nil {
			rt.Fatal(err)
		}

		if diff := cmp.Diff(firstThenSecond.Retained, secondThenFirst.Retained); diff != "" {
			rt.Fatal("disjoint removals did not commute\n", diff)
		}

		checkSurvivorGraph(rt, m, unique, Removal{
			Retained:  firstThenSecond.Retained,
			Removed:   sortedDistinct(unique),
			Artifacts: artifactDiff(m, firstThenSecond.Retained),
		})

		checkRepeatedRemoval(rt, m, first, withoutFirst)
	})
}

func checkRepeatedRemoval(rt *rapid.T, m Manifest, first []string, withoutFirst Removal) {
	rt.Helper()

	reversed, err := RemoveSessions(m, reverse(first))
	if err != nil {
		rt.Fatal(err)
	}

	if diff := cmp.Diff(withoutFirst.Retained, reversed.Retained); diff != "" {
		rt.Fatal("repeated selection is not idempotent\n", diff)
	}

	for _, id := range first {
		if _, err := RemoveSessions(withoutFirst.Retained, []string{id}); !errors.Is(err, ErrInvalid) {
			rt.Fatal("already removed identity was accepted", err)
		}
	}
}

func splitSelection(rt *rapid.T, values []string) ([]string, []string) {
	var first, second []string

	for _, value := range values {
		if rapid.Bool().Draw(rt, "inFirst") {
			first = append(first, value)
			continue
		}

		second = append(second, value)
	}

	return first, second
}

func entityCounts(m Manifest) [6]int {
	return [6]int{len(m.Sessions), len(m.Groups), len(m.Windows), len(m.Links), len(m.Panes), len(m.Artifacts)}
}

func withinCounts(larger, smaller [6]int) bool {
	for index := range larger {
		if larger[index] > smaller[index] {
			return false
		}
	}

	return true
}

func subsetOf(subset, superset []string) bool {
	values := make(map[string]bool, len(superset))
	for _, value := range superset {
		values[value] = true
	}

	for _, value := range subset {
		if !values[value] {
			return false
		}
	}

	return true
}

func distinct(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))

	for _, value := range values {
		if seen[value] {
			continue
		}

		seen[value] = true
		unique = append(unique, value)
	}

	return unique
}

func reverse(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	reversed := make([]string, 0, len(values))

	for _, value := range slices.Backward(values) {
		reversed = append(reversed, value)
	}

	return reversed
}

func sortedDistinct(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	sorted := slices.Clone(distinct(values))
	slices.Sort(sorted)

	return sorted
}

func artifactDiff(before, after Manifest) []string {
	retained := make(map[string]bool, len(after.Artifacts))
	for _, artifact := range after.Artifacts {
		retained[artifact.ID] = true
	}

	var dropped []string

	for _, artifact := range before.Artifacts {
		if !retained[artifact.ID] {
			dropped = append(dropped, artifact.ID)
		}
	}

	return dropped
}

func drawSelection(rt *rapid.T, m Manifest) []string {
	selection := make([]string, 0, len(m.Sessions))

	for _, session := range m.Sessions {
		if !rapid.Bool().Draw(rt, "selected") {
			continue
		}

		selection = append(selection, session.ID)

		if rapid.Bool().Draw(rt, "duplicate") {
			selection = append(selection, session.ID)
		}
	}

	return rapid.Permutation(selection).Draw(rt, "selectionOrder")
}

func drawManifest(rt *rapid.T) Manifest {
	sessionCount := rapid.IntRange(1, 5).Draw(rt, "sessionCount")
	windowCount := rapid.IntRange(1, 3).Draw(rt, "windowCount")
	poolSize := rapid.IntRange(0, 3).Draw(rt, "artifactCount")

	m := Manifest{
		Format: "sesh", Version: Schema, Started: 1, Finished: 2,
		Source: Source{
			TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "observed",
			Options:     drawOptions(rt, "server"),
			Environment: drawEnvironment(rt, "GLOBAL"),
		},
		Evidence: unverifiedEvidence(),
	}

	pool := make([]Artifact, 0, poolSize)

	for index := range poolSize {
		digest := rapid.SliceOfN(rapid.ByteRange(0, 255), 32, 32).Draw(rt, "digest")
		pool = append(pool, Artifact{
			ID:     fmt.Sprintf("a%d", index),
			Kind:   "terminal-text",
			Length: int64(rapid.IntRange(0, 65536).Draw(rt, "artifactLength")),
			SHA256: hex.EncodeToString(digest),
		})
	}

	for index := range windowCount {
		window, panes := drawWindow(rt, index, pool)
		m.Windows = append(m.Windows, window)
		m.Panes = append(m.Panes, panes...)
	}

	referenced := make(map[string]bool, len(pool))

	for _, pane := range m.Panes {
		if pane.Terminal.Artifact != "" {
			referenced[pane.Terminal.Artifact] = true
		}
	}

	for _, artifact := range pool {
		if referenced[artifact.ID] {
			m.Artifacts = append(m.Artifacts, artifact)
		}
	}

	links := drawLinks(rt, sessionCount, windowCount)

	sessions, groups := drawSessions(rt, links)
	m.Sessions, m.Groups = sessions, groups

	for index := range sessionCount {
		m.Links = append(m.Links, links[index]...)
	}

	return m
}

func drawWindow(rt *rapid.T, index int, pool []Artifact) (Window, []Pane) {
	width := rapid.SampledFrom([]int{80, 81, 100}).Draw(rt, "width")
	height := rapid.SampledFrom([]int{24, 30}).Draw(rt, "height")
	paneCount := rapid.IntRange(1, 2).Draw(rt, "paneCount")
	zoomed := rapid.Bool().Draw(rt, "zoomed")
	windowID := fmt.Sprintf("w%d", index)

	leaves := []Layout{{Width: width, Height: height, Pane: paneID(index, 0)}}
	if paneCount > 1 {
		first := width / 2
		leaves = []Layout{
			{Width: first, Height: height, Pane: paneID(index, 0)},
			{Width: width - first - 1, Height: height, Left: first + 1, Pane: paneID(index, 1)},
		}
	}

	layout := leaves[0]
	if paneCount > 1 {
		layout = Layout{Width: width, Height: height, Axis: "x", Children: leaves}
	}

	window := Window{
		ID: windowID, Name: Bytes(fmt.Sprintf("window-%d", index)), Width: width, Height: height,
		Layout: layout, Zoomed: zoomed, Options: drawOptions(rt, "window"),
	}

	panes := make([]Pane, 0, paneCount)
	paneIndex := rapid.IntRange(0, 1).Draw(rt, "paneIndex")

	for position, leaf := range leaves {
		geometry := leaf
		if zoomed && position == 0 {
			geometry = Layout{Width: width, Height: height}
		}

		artifact := ""
		if len(pool) > 0 && rapid.Bool().Draw(rt, "artifact") {
			artifact = pool[rapid.IntRange(0, len(pool)-1).Draw(rt, "artifactIndex")].ID
		}

		panes = append(panes, Pane{
			ID: leaf.Pane, Window: windowID, Index: paneIndex,
			Title: Bytes(fmt.Sprintf("title-%d-%d", index, position)), Directory: Bytes(fmt.Sprintf("/srv/%d", index)),
			Width: geometry.Width, Height: geometry.Height, Left: geometry.Left, Top: geometry.Top,
			Active: position == 0, Dead: rapid.Bool().Draw(rt, "dead"),
			Options: drawOptions(rt, "pane"), Terminal: drawTerminal(rt, artifact), Launch: drawLaunch(rt, index),
		})

		paneIndex += rapid.IntRange(1, 3).Draw(rt, "paneIndexStep")
	}

	return window, panes
}

func drawTerminal(rt *rapid.T, artifact string) Terminal {
	return Terminal{
		Artifact:  artifact,
		History:   rapid.IntRange(0, 1000).Draw(rt, "history"),
		Alternate: rapid.Bool().Draw(rt, "alternate"),
		CursorX:   rapid.IntRange(0, 200).Draw(rt, "cursorX"),
		CursorY:   rapid.IntRange(0, 200).Draw(rt, "cursorY"),
		Mode:      Bytes(rapid.SampledFrom([]string{"", "k", "ks"}).Draw(rt, "mode")),
	}
}

func drawLaunch(rt *rapid.T, index int) Launch {
	if !rapid.Bool().Draw(rt, "restart") {
		return Launch{Recovery: "unverified", Birth: Bytes(fmt.Sprintf("birth-%d", index))}
	}

	return Launch{
		Executable:  Bytes("/bin/tool"),
		Arguments:   []Bytes{Bytes("tool"), Bytes(fmt.Sprintf("--pane-%d", index))},
		Directory:   Bytes(fmt.Sprintf("/srv/%d", index)),
		Environment: drawEnvironment(rt, "PATH"),
		Birth:       Bytes(fmt.Sprintf("birth-%d", index)),
		Recovery:    "restart",
	}
}

func drawOptions(rt *rapid.T, scope string) []Option {
	count := rapid.IntRange(0, 2).Draw(rt, "optionCount")
	options := make([]Option, 0, count)

	for index := range count {
		present := rapid.Bool().Draw(rt, "present")
		option := Option{
			Scope: scope, Name: Bytes(fmt.Sprintf("@opt%d", index)), Value: nil,
			Present: present, Inherited: rapid.Bool().Draw(rt, "inherited"),
		}

		if present {
			option.Value = Bytes(fmt.Sprintf("value-%d", index))
		}

		options = append(options, option)
	}

	return options
}

func drawEnvironment(rt *rapid.T, prefix string) []Variable {
	count := rapid.IntRange(0, 2).Draw(rt, "environmentCount")
	environment := make([]Variable, 0, count)

	for index := range count {
		name := Bytes(fmt.Sprintf("%s%d", prefix, index))

		if rapid.Bool().Draw(rt, "removed") {
			environment = append(environment, Variable{Name: name, Value: nil, State: "removed", Hidden: false})
			continue
		}

		environment = append(environment, Variable{
			Name: name, Value: Bytes(fmt.Sprintf("value-%d", index)), State: "set",
			Hidden: rapid.Bool().Draw(rt, "hidden"),
		})
	}

	return environment
}

func drawLinks(rt *rapid.T, sessionCount, windowCount int) [][]Link {
	shapeCount := rapid.IntRange(1, 3).Draw(rt, "shapeCount")
	shapes := make([][]Link, 0, shapeCount)

	for range shapeCount {
		shapes = append(shapes, drawShape(rt, windowCount))
	}

	links := make([][]Link, sessionCount)

	for session := range sessionCount {
		shape := shapes[rapid.IntRange(0, shapeCount-1).Draw(rt, "shape")]
		links[session] = make([]Link, 0, len(shape))

		for _, link := range shape {
			links[session] = append(links[session], Link{
				Session: sessionID(session), Window: link.Window, Index: link.Index,
				Active: false, Last: false,
				Flags: Bytes(rapid.SampledFrom([]string{"", "*", "-"}).Draw(rt, "flags")),
			})
		}
	}

	referenced := make(map[string]bool, windowCount)

	for _, session := range links {
		for _, link := range session {
			referenced[link.Window] = true
		}
	}

	for window := range windowCount {
		if referenced[windowID(window)] {
			continue
		}

		cover := links[0][len(links[0])-1].Index + rapid.IntRange(1, 3).Draw(rt, "coverIndex")
		links[0] = append(links[0], Link{
			Session: sessionID(0), Window: windowID(window), Index: cover,
			Active: false, Last: false,
			Flags: Bytes(rapid.SampledFrom([]string{"", "*", "-"}).Draw(rt, "flags")),
		})
	}

	for session := range links {
		active := rapid.IntRange(0, len(links[session])-1).Draw(rt, "activeLink")
		links[session][active].Active = true

		if len(links[session]) > 1 && rapid.Bool().Draw(rt, "lastLink") {
			last := rapid.IntRange(0, len(links[session])-2).Draw(rt, "lastLinkIndex")
			if last >= active {
				last++
			}

			links[session][last].Last = true
		}
	}

	return links
}

func drawShape(rt *rapid.T, windowCount int) []Link {
	shape := make([]Link, 0, windowCount)

	for window := range windowCount {
		if rapid.Bool().Draw(rt, "shapeWindow") {
			shape = append(shape, Link{Window: windowID(window)})
		}
	}

	if len(shape) == 0 {
		shape = append(shape, Link{Window: windowID(0)})
	}

	next := rapid.IntRange(0, 2).Draw(rt, "shapeStart")

	for index := range shape {
		shape[index].Index = next
		next += rapid.IntRange(1, 3).Draw(rt, "shapeStep")
	}

	if rapid.Bool().Draw(rt, "shapeDuplicate") {
		duplicate := shape[rapid.IntRange(0, len(shape)-1).Draw(rt, "shapeDuplicateWindow")]
		shape = append(shape, Link{Window: duplicate.Window, Index: next})
	}

	return shape
}

func clusterSessions(links [][]Link) [][]int {
	shapes := make([]string, len(links))
	for index := range links {
		shapes[index] = linkShape(links[index])
	}

	clusters := make([][]int, 0, len(links))

	for index := range links {
		placed := false

		for position := range clusters {
			if shapes[clusters[position][0]] != shapes[index] {
				continue
			}

			clusters[position] = append(clusters[position], index)
			placed = true

			break
		}

		if !placed {
			clusters = append(clusters, []int{index})
		}
	}

	return clusters
}

func drawSessions(rt *rapid.T, links [][]Link) ([]Session, []Group) {
	clusters := clusterSessions(links)

	groups := make([]Group, 0, len(links))
	assigned := make([]string, len(links))
	groupIndex := make(map[string]int, len(links))
	nextGroup := 0

	for _, cluster := range clusters {
		if !rapid.Bool().Draw(rt, "grouped") {
			continue
		}

		slotOf := make(map[int]string, len(cluster))

		for position, session := range cluster {
			slot := rapid.IntRange(0, position).Draw(rt, "slot")

			id, found := slotOf[slot]
			if !found {
				id = fmt.Sprintf("g%d", nextGroup)
				nextGroup++
				slotOf[slot] = id
				groupIndex[id] = len(groups)
				groups = append(groups, Group{ID: id, Name: Bytes("group-" + id)})
			}

			assigned[session] = id
			groups[groupIndex[id]].Members = append(groups[groupIndex[id]].Members, sessionID(session))
		}
	}

	if len(groups) == 0 && rapid.Bool().Draw(rt, "nilGroups") {
		groups = nil
	}

	sessions := make([]Session, 0, len(links))

	for index := range links {
		sessions = append(sessions, Session{
			ID: sessionID(index), Name: Bytes(fmt.Sprintf("name-%d", index)),
			Directory: Bytes(fmt.Sprintf("/srv/%d", index)), Group: assigned[index],
			Options: drawOptions(rt, "session"), Environment: drawEnvironment(rt, "ENV"),
		})
	}

	return sessions, groups
}

func linkShape(links []Link) string {
	parts := make([]string, 0, len(links))

	for _, link := range links {
		parts = append(parts, fmt.Sprintf("%d:%s", link.Index, link.Window))
	}

	slices.Sort(parts)

	return strings.Join(parts, ",")
}

func sessionID(index int) string {
	return fmt.Sprintf("s%d", index)
}

func windowID(index int) string {
	return fmt.Sprintf("w%d", index)
}

func paneID(window, pane int) string {
	return fmt.Sprintf("p%d-%d", window, pane)
}
