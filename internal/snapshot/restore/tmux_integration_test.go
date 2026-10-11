//go:build (linux || darwin) && integration

package restore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/testsupport/testenv"
)

func TestPlannedSplitsConstructExactRectangles(t *testing.T) {
	for _, base := range []int{0, 1, 5} {
		t.Run(strconv.Itoa(base), func(t *testing.T) {
			runPlannedSplitsAtBase(t, base)
		})
	}
}

func runPlannedSplitsAtBase(t *testing.T, base int) {
	t.Helper()

	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if err := server.GlobalSessionOptions().SetBaseIndex(ctx, base); err != nil {
		t.Fatal(err)
	}

	if err := server.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	manifest := nestedLayoutFixture()

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	assertAroundLayout := func(step Step, resolve func() *restoredTarget) {
		if _, ok := step.(*ApplyLayoutStep); ok {
			resolve().assertRectangles(t, ctx)
		}
	}

	target := executeRestorePlan(t, ctx, server, manifest, plan, assertAroundLayout, assertAroundLayout)

	target.assertRectangles(t, ctx)

	_, links, err := target.sessions["s0"].WindowInfos(ctx)
	if err != nil || len(links) != 1 || links[0].Index != 5 || links[0].WindowID != target.windows["w0"].ID() {
		t.Fatalf("final sparse membership: %v, %v", links, err)
	}
}

func TestNativeGroupMembersWithDifferentActiveLastWindows(t *testing.T) {
	for _, base := range []int{0, 1, 5} {
		t.Run(fmt.Sprintf("base_%d", base), func(t *testing.T) {
			testNativeGroupAtBase(t, base)
		})
	}
}

func testNativeGroupAtBase(t *testing.T, base int) {
	t.Helper()

	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if err := server.GlobalSessionOptions().SetBaseIndex(ctx, base); err != nil {
		t.Fatal(err)
	}

	if err := server.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	unrelated, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "unrelated-fixture",
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	unrelatedID := unrelated.ID()

	_, uLinksBefore, err := unrelated.WindowInfos(ctx)
	if err != nil || len(uLinksBefore) != 1 {
		t.Fatalf("unrelated fixture initial links: %v, %v", uLinksBefore, err)
	}

	m := buildNativeGroupManifest()

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("ValidatePlan failed: %v", err)
	}

	target := executeRestorePlan(t, ctx, server, m, plan, nil, nil)

	verifyNativeGroupState(t, ctx, target, unrelatedID, uLinksBefore, unrelated)
}

func buildNativeGroupManifest() model.Manifest {
	m := makeBaseManifest()
	m.Sessions = []model.Session{
		{ID: "s0", Name: model.Bytes("member-lead"), Directory: model.Bytes("/tmp"), Group: "g0"},
		{ID: "s1", Name: model.Bytes("member-foll-1"), Directory: model.Bytes("/tmp"), Group: "g0"},
		{ID: "s2", Name: model.Bytes("member-foll-2"), Directory: model.Bytes("/tmp"), Group: "g0"},
	}
	m.Groups = []model.Group{
		{
			ID:      "g0",
			Name:    model.Bytes("native-source-group"),
			Members: []string{"s0", "s1", "s2"},
		},
	}
	m.Windows = []model.Window{
		{ID: "w0", Name: model.Bytes("win-alpha"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}},
		{ID: "w1", Name: model.Bytes("win-beta"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p1"}},
		{ID: "w2", Name: model.Bytes("win-gamma"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p2"}},
	}
	m.Panes = []model.Pane{
		{ID: "p0", Window: "w0", Index: 0, Width: 80, Height: 24, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p1", Window: "w1", Index: 0, Width: 80, Height: 24, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p2", Window: "w2", Index: 0, Width: 80, Height: 24, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
	}
	m.Links = []model.Link{
		{Session: "s0", Window: "w0", Index: 1, Active: true, Last: false},
		{Session: "s0", Window: "w1", Index: 3, Active: false, Last: true},
		{Session: "s0", Window: "w2", Index: 5, Active: false, Last: false},

		{Session: "s1", Window: "w0", Index: 1, Active: false, Last: false},
		{Session: "s1", Window: "w1", Index: 3, Active: true, Last: false},
		{Session: "s1", Window: "w2", Index: 5, Active: false, Last: true},

		{Session: "s2", Window: "w0", Index: 1, Active: false, Last: true},
		{Session: "s2", Window: "w1", Index: 3, Active: false, Last: false},
		{Session: "s2", Window: "w2", Index: 5, Active: true, Last: false},
	}

	return m
}

func verifyNativeGroupState(t *testing.T, ctx context.Context, target *restoredTarget, unrelatedID gotmux.SessionID, uLinksBefore []gotmux.WindowLinkInfo, unrelated gotmux.Session) {
	t.Helper()

	leadSess := target.sessions["s0"]
	foll1Sess := target.sessions["s1"]
	foll2Sess := target.sessions["s2"]

	if string(leadSess.ID()) == "s0" || string(foll1Sess.ID()) == "s1" || string(foll2Sess.ID()) == "s2" {
		t.Fatalf("runtime session IDs should differ from source IDs: %v, %v, %v", leadSess.ID(), foll1Sess.ID(), foll2Sess.ID())
	}

	assertNativeGroupCommonGroup(t, ctx, leadSess, foll1Sess, foll2Sess, "native-source-group")
	assertNativeGroupSharedWindows(t, ctx, leadSess, foll1Sess, foll2Sess)
	assertUnrelatedFixtureUnchanged(t, ctx, unrelated, unrelatedID, uLinksBefore)
}

func assertNativeGroupCommonGroup(t *testing.T, ctx context.Context, leadSess, foll1Sess, foll2Sess gotmux.Session, wantGroupName string) {
	t.Helper()

	leadInfo, err := leadSess.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	foll1Info, err := foll1Sess.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	foll2Info, err := foll2Sess.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	grp0, ok0 := leadInfo.Group.Get()
	grp1, ok1 := foll1Info.Group.Get()
	grp2, ok2 := foll2Info.Group.Get()

	if !ok0 || !ok1 || !ok2 {
		t.Fatalf("all sessions must have a group set: ok0=%v ok1=%v ok2=%v", ok0, ok1, ok2)
	}

	if grp0 != wantGroupName || grp1 != wantGroupName || grp2 != wantGroupName {
		t.Fatalf("runtime group mismatch across members: got (%q, %q, %q), want %q", grp0, grp1, grp2, wantGroupName)
	}
}

func assertNativeGroupSharedWindows(t *testing.T, ctx context.Context, leadSess, foll1Sess, foll2Sess gotmux.Session) {
	t.Helper()

	_, links0, err := leadSess.WindowInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}

	_, links1, err := foll1Sess.WindowInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}

	_, links2, err := foll2Sess.WindowInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(links0) != 3 || len(links1) != 3 || len(links2) != 3 {
		t.Fatalf("expected 3 windows in each session, got %d, %d, %d", len(links0), len(links1), len(links2))
	}

	for i := range 3 {
		if links0[i].Index != links1[i].Index || links1[i].Index != links2[i].Index {
			t.Fatalf("index mismatch at pos %d: %d, %d, %d", i, links0[i].Index, links1[i].Index, links2[i].Index)
		}

		if links0[i].WindowID != links1[i].WindowID || links1[i].WindowID != links2[i].WindowID {
			t.Fatalf("WindowID mismatch at pos %d: %v, %v, %v", i, links0[i].WindowID, links1[i].WindowID, links2[i].WindowID)
		}
	}

	assertActiveAndLast(t, links0, 1, 3, "s0")
	assertActiveAndLast(t, links1, 3, 5, "s1")
	assertActiveAndLast(t, links2, 5, 1, "s2")
}

func assertUnrelatedFixtureUnchanged(t *testing.T, ctx context.Context, unrelated gotmux.Session, unrelatedID gotmux.SessionID, uLinksBefore []gotmux.WindowLinkInfo) {
	t.Helper()

	uInfoAfter, err := unrelated.Info(ctx)
	if err != nil || uInfoAfter.ID != unrelatedID {
		t.Fatalf("unrelated fixture destroyed or mutated: %v, %v", uInfoAfter, err)
	}

	_, uLinksAfter, err := unrelated.WindowInfos(ctx)
	if err != nil || len(uLinksAfter) != 1 || uLinksAfter[0].WindowID != uLinksBefore[0].WindowID {
		t.Fatalf("unrelated fixture window mutated: before=%v, after=%v", uLinksBefore, uLinksAfter)
	}
}

func TestPreflightCheckGroupCollisionRejectsExistingSessionAndGroup(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "existing-session",
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.StartPolicyExistingOnly,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:  "existing-group",
		Start: gotmux.StartPolicyExistingOnly,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:  "group-leader",
		Group: "existing-group",
		Start: gotmux.StartPolicyExistingOnly,
	}); err != nil {
		t.Fatal(err)
	}

	if err := PreflightCheckGroupCollision(ctx, server, "existing-session"); err == nil {
		t.Fatal("expected collision error for existing session name, got nil")
	}

	if err := PreflightCheckGroupCollision(ctx, server, "existing-group"); err == nil {
		t.Fatal("expected collision error for existing group name, got nil")
	}

	if err := PreflightCheckGroupCollision(ctx, server, "fresh-group"); err != nil {
		t.Fatalf("expected fresh group to succeed, got %v", err)
	}
}

func TestSharedWindowsAcrossUnrelatedSessionsAndMultipleGroups(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if err := server.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	m := makeBaseManifest()
	m.Sessions = []model.Session{
		{ID: "s-solo", Name: model.Bytes("sess-solo"), Directory: model.Bytes("/tmp")},
		{ID: "s-g1-0", Name: model.Bytes("sess-g1-0"), Directory: model.Bytes("/tmp"), Group: "g1"},
		{ID: "s-g1-1", Name: model.Bytes("sess-g1-1"), Directory: model.Bytes("/tmp"), Group: "g1"},
		{ID: "s-g2-0", Name: model.Bytes("sess-g2-0"), Directory: model.Bytes("/tmp"), Group: "g2"},
		{ID: "s-g2-1", Name: model.Bytes("sess-g2-1"), Directory: model.Bytes("/tmp"), Group: "g2"},
	}
	m.Groups = []model.Group{
		{ID: "g1", Name: model.Bytes("group-one"), Members: []string{"s-g1-0", "s-g1-1"}},
		{ID: "g2", Name: model.Bytes("group-two"), Members: []string{"s-g2-0", "s-g2-1"}},
	}
	m.Windows = []model.Window{
		{ID: "w-shared", Name: model.Bytes("win-shared"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p-shared"}},
		{ID: "w-solo", Name: model.Bytes("win-solo"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p-solo"}},
		{ID: "w-g1", Name: model.Bytes("win-g1"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p-g1"}},
		{ID: "w-g2", Name: model.Bytes("win-g2"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p-g2"}},
	}
	m.Panes = []model.Pane{
		{ID: "p-shared", Window: "w-shared", Index: 0, Width: 80, Height: 24, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p-solo", Window: "w-solo", Index: 0, Width: 80, Height: 24, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p-g1", Window: "w-g1", Index: 0, Width: 80, Height: 24, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p-g2", Window: "w-g2", Index: 0, Width: 80, Height: 24, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
	}
	m.Links = []model.Link{
		{Session: "s-solo", Window: "w-solo", Index: 0, Active: true, Last: false},
		{Session: "s-solo", Window: "w-shared", Index: 4, Active: false, Last: true},

		{Session: "s-g1-0", Window: "w-g1", Index: 1, Active: true, Last: false},
		{Session: "s-g1-0", Window: "w-shared", Index: 2, Active: false, Last: true},
		{Session: "s-g1-1", Window: "w-g1", Index: 1, Active: false, Last: true},
		{Session: "s-g1-1", Window: "w-shared", Index: 2, Active: true, Last: false},

		{Session: "s-g2-0", Window: "w-g2", Index: 3, Active: true, Last: false},
		{Session: "s-g2-0", Window: "w-shared", Index: 5, Active: false, Last: true},
		{Session: "s-g2-1", Window: "w-g2", Index: 3, Active: false, Last: true},
		{Session: "s-g2-1", Window: "w-shared", Index: 5, Active: true, Last: false},
	}

	plan, err := BuildPlan(m, []string{"s-solo", "s-g1-0", "s-g2-0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("ValidatePlan failed: %v", err)
	}

	target := executeRestorePlan(t, ctx, server, m, plan, nil, nil)

	expectedSharedID := target.windows["w-shared"].ID()

	assertSlotWindowID(t, ctx, target.sessions["s-solo"], 4, expectedSharedID, "s-solo")
	assertSlotWindowID(t, ctx, target.sessions["s-g1-0"], 2, expectedSharedID, "s-g1-0")
	assertSlotWindowID(t, ctx, target.sessions["s-g1-1"], 2, expectedSharedID, "s-g1-1")
	assertSlotWindowID(t, ctx, target.sessions["s-g2-0"], 5, expectedSharedID, "s-g2-0")
	assertSlotWindowID(t, ctx, target.sessions["s-g2-1"], 5, expectedSharedID, "s-g2-1")

	soloInfo, err := target.sessions["s-solo"].Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := soloInfo.Group.Get(); ok {
		t.Fatalf("s-solo should not have a group")
	}

	g1Info, err := target.sessions["s-g1-0"].Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	g2Info, err := target.sessions["s-g2-0"].Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	g1Grp, ok1 := g1Info.Group.Get()
	g2Grp, ok2 := g2Info.Group.Get()

	if !ok1 || !ok2 || g1Grp == g2Grp {
		t.Fatalf("expected distinct runtime groups for g1 and g2: %q, %q", g1Grp, g2Grp)
	}
}

func TestSparseSlotRepeatedLinksAndSharedOnlyOwners(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	m := makeBaseManifest()
	m.Sessions = []model.Session{
		{ID: "s-owner", Name: model.Bytes("owner-session"), Directory: model.Bytes("/tmp")},
		{ID: "s-repeat", Name: model.Bytes("repeat-session"), Directory: model.Bytes("/tmp")},
		{ID: "s-shared-only", Name: model.Bytes("shared-only-session"), Directory: model.Bytes("/tmp")},
	}
	m.Windows = []model.Window{
		{ID: "w0", Name: model.Bytes("target-win"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}},
		{ID: "w-own", Name: model.Bytes("owner-own"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p1"}},
	}
	m.Panes = []model.Pane{
		{ID: "p0", Window: "w0", Index: 0, Width: 80, Height: 24, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p1", Window: "w-own", Index: 0, Width: 80, Height: 24, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
	}
	m.Links = []model.Link{
		{Session: "s-owner", Window: "w-own", Index: 0, Active: true, Last: false},
		{Session: "s-owner", Window: "w0", Index: 1, Active: false, Last: true},

		{Session: "s-repeat", Window: "w0", Index: 2, Active: true, Last: false},
		{Session: "s-repeat", Window: "w0", Index: 7, Active: false, Last: true},

		{Session: "s-shared-only", Window: "w0", Index: 6, Active: true, Last: false},
	}

	plan, err := BuildPlan(m, []string{"s-owner", "s-repeat", "s-shared-only"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("ValidatePlan failed: %v", err)
	}

	target := executeRestorePlan(t, ctx, server, m, plan, nil, nil)

	assertSparseRepeatedAndSharedLinks(t, ctx, target)
}

func assertSparseRepeatedAndSharedLinks(t *testing.T, ctx context.Context, target *restoredTarget) {
	t.Helper()

	_, rLinks, err := target.sessions["s-repeat"].WindowInfos(ctx)
	if err != nil || len(rLinks) != 2 {
		t.Fatalf("expected 2 links in s-repeat, got %v, %v", rLinks, err)
	}

	if rLinks[0].Index != 2 || rLinks[1].Index != 7 {
		t.Fatalf("unexpected slots in s-repeat: %v", rLinks)
	}

	if rLinks[0].WindowID != rLinks[1].WindowID || rLinks[0].WindowID != target.windows["w0"].ID() {
		t.Fatalf("repeated links must share exact WindowID: slot2=%v slot7=%v w0=%v", rLinks[0].WindowID, rLinks[1].WindowID, target.windows["w0"].ID())
	}

	_, soLinks, err := target.sessions["s-shared-only"].WindowInfos(ctx)
	if err != nil || len(soLinks) != 1 {
		t.Fatalf("expected 1 link in s-shared-only, got %v, %v", soLinks, err)
	}

	if soLinks[0].Index != 6 || soLinks[0].WindowID != target.windows["w0"].ID() {
		t.Fatalf("shared-only slot 6 mismatch: %v", soLinks[0])
	}
}

func TestDeepAsymmetricLayoutTinyCellsBordersAndZoom(t *testing.T) {
	for _, border := range []gotmux.PaneBorderStatus{gotmux.PaneBorderStatusOff, gotmux.PaneBorderStatusTop, gotmux.PaneBorderStatusBottom} {
		t.Run(string(border), func(t *testing.T) {
			verifyDeepLayoutAtBorder(t, border)
		})
	}
}

func verifyDeepLayoutAtBorder(t *testing.T, border gotmux.PaneBorderStatus) {
	t.Helper()
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if err := server.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	m := deepAsymmetricLayoutFixture()

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	if err := ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}

	wantRects := map[string][4]int{
		"p0": {0, 0, 29, 15},
		"p1": {0, 16, 29, 25},
		"p2": {30, 0, 50, 10},
		"p3": {30, 11, 24, 19},
		"p4": {55, 11, 25, 19},
		"p5": {30, 31, 50, 10},
		"p6": {81, 0, 40, 38},
		"p7": {81, 39, 19, 2},
		"p8": {101, 39, 20, 2},
	}

	assertAroundLayout := func(step Step, resolve func() *restoredTarget) {
		if _, ok := step.(*ApplyLayoutStep); ok {
			resolve().assertPaneRectangles(t, ctx, wantRects)
		}
	}

	target := executeRestorePlan(t, ctx, server, m, plan, assertAroundLayout, assertAroundLayout)

	win := target.windows["w0"]
	if err := win.Options().SetPaneBorderStatus(ctx, border); err != nil {
		t.Fatal(err)
	}

	expectedRects := computeExpectedBorderRectangles(wantRects, border)
	verifyDeepLayoutGeometry(t, ctx, win, target.panes["p3"], target, expectedRects)
}

func computeExpectedBorderRectangles(wantRects map[string][4]int, border gotmux.PaneBorderStatus) map[string][4]int {
	expectedRects := make(map[string][4]int, len(wantRects))
	for id, rect := range wantRects {
		left, top, width, height := rect[0], rect[1], rect[2], rect[3]

		switch border {
		case gotmux.PaneBorderStatusTop:
			if top == 0 {
				top++
				height--
			}
		case gotmux.PaneBorderStatusBottom:
			if top+height == 41 {
				height--
			}
		case gotmux.PaneBorderStatusOff:
		}

		expectedRects[id] = [4]int{left, top, width, height}
	}

	return expectedRects
}

func verifyDeepLayoutGeometry(t *testing.T, ctx context.Context, win gotmux.Window, p3Pane gotmux.Pane, target *restoredTarget, expectedRects map[string][4]int) {
	t.Helper()

	winInfo, err := win.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !winInfo.Zoomed {
		t.Fatal("planned zoom was not applied")
	}

	selected, err := win.ActivePane(ctx)
	if err != nil || selected.ID() != p3Pane.ID() {
		t.Fatalf("planned active pane not applied: %v, %v", selected.ID(), err)
	}

	if err := p3Pane.ToggleZoom(ctx); err != nil {
		t.Fatal(err)
	}

	target.assertPaneRectangles(t, ctx, expectedRects)

	if err := p3Pane.ToggleZoom(ctx); err != nil {
		t.Fatal(err)
	}

	zoomedInfo, err := win.Info(ctx)
	if err != nil || !zoomedInfo.Zoomed {
		t.Fatalf("expected window to be zoomed: %v, %v", zoomedInfo, err)
	}

	activePane, err := win.ActivePane(ctx)
	if err != nil || activePane.ID() != p3Pane.ID() {
		t.Fatalf("expected p3 to be active: got %v, want %v", activePane.ID(), p3Pane.ID())
	}
}

func TestPlannedConstructionWindowResizeContract(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if err := server.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	manifest := nestedLayoutFixture()

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	target := executeRestorePlan(t, ctx, server, manifest, plan, nil, nil)

	win := target.windows["w0"]

	target.assertRectangles(t, ctx)

	originalIDs := map[string]gotmux.PaneID{
		"p0": target.panes["p0"].ID(),
		"p1": target.panes["p1"].ID(),
		"p2": target.panes["p2"].ID(),
		"p3": target.panes["p3"].ID(),
	}

	verifyResizePanes(t, ctx, win, gotmux.Size{Width: 120, Height: 40}, originalIDs)

	verifyResizePanes(t, ctx, win, gotmux.Size{Width: 60, Height: 20}, originalIDs)
}

func assertPanesMatchOriginalIDs(t *testing.T, panes []gotmux.PaneInfo, originalIDs map[string]gotmux.PaneID) {
	t.Helper()

	liveIDs := make(map[gotmux.PaneID]bool, len(panes))
	for _, p := range panes {
		liveIDs[p.ID] = true
	}

	for name, wantID := range originalIDs {
		if !liveIDs[wantID] {
			t.Fatalf("pane %s (ID %v) missing after resize: got live IDs %v", name, wantID, liveIDs)
		}
	}
}

func assertLeftColumnNestedGeometry(t *testing.T, p0, p1 gotmux.PaneInfo, height int) {
	t.Helper()

	if p0.Left != 0 || p0.Top != 0 {
		t.Fatalf("p0 expected at (0, 0), got (%d, %d)", p0.Left, p0.Top)
	}

	if p1.Left != 0 || p1.Top != p0.Height+1 {
		t.Fatalf("p1 expected at (0, %d), got (%d, %d)", p0.Height+1, p1.Left, p1.Top)
	}

	if p0.Width != p1.Width {
		t.Fatalf("left column width mismatch: p0=%d, p1=%d", p0.Width, p1.Width)
	}

	if p0.Height+1+p1.Height != height {
		t.Fatalf("left column heights do not cover height %d: p0=%d + 1 + p1=%d = %d",
			height, p0.Height, p1.Height, p0.Height+1+p1.Height)
	}
}

func assertRightColumnNestedGeometry(t *testing.T, p2, p3 gotmux.PaneInfo, leftColWidth, width, height int) {
	t.Helper()

	wantRightLeft := leftColWidth + 1
	if p2.Left != wantRightLeft || p2.Top != 0 {
		t.Fatalf("p2 expected at (%d, 0), got (%d, %d)", wantRightLeft, p2.Left, p2.Top)
	}

	if p3.Left != wantRightLeft || p3.Top != p2.Height+1 {
		t.Fatalf("p3 expected at (%d, %d), got (%d, %d)", wantRightLeft, p2.Height+1, p3.Left, p3.Top)
	}

	if p2.Width != p3.Width {
		t.Fatalf("right column width mismatch: p2=%d, p3=%d", p2.Width, p3.Width)
	}

	if p2.Height+1+p3.Height != height {
		t.Fatalf("right column heights do not cover height %d: p2=%d + 1 + p3=%d = %d",
			height, p2.Height, p3.Height, p2.Height+1+p3.Height)
	}

	if leftColWidth+1+p2.Width != width {
		t.Fatalf("horizontal widths do not cover width %d: left=%d + 1 + right=%d = %d",
			width, leftColWidth, p2.Width, leftColWidth+1+p2.Width)
	}
}

func assertFourPaneNestedGeometryAndDividers(t *testing.T, panes []gotmux.PaneInfo, size gotmux.Size, originalIDs map[string]gotmux.PaneID) {
	t.Helper()

	byID := make(map[gotmux.PaneID]gotmux.PaneInfo, len(panes))
	for _, p := range panes {
		byID[p.ID] = p
	}

	p0 := byID[originalIDs["p0"]]
	p1 := byID[originalIDs["p1"]]
	p2 := byID[originalIDs["p2"]]
	p3 := byID[originalIDs["p3"]]

	assertLeftColumnNestedGeometry(t, p0, p1, size.Height)
	assertRightColumnNestedGeometry(t, p2, p3, p0.Width, size.Width, size.Height)

	cellArea := p0.Width*p0.Height + p1.Width*p1.Height + p2.Width*p2.Height + p3.Width*p3.Height
	dividerArea := size.Height + p0.Width + p2.Width
	totalArea := cellArea + dividerArea

	if totalArea != size.Width*size.Height {
		t.Fatalf("cellArea (%d) + dividerArea (%d) = %d != window area (%d)",
			cellArea, dividerArea, totalArea, size.Width*size.Height)
	}
}

func verifyResizePanes(t *testing.T, ctx context.Context, win gotmux.Window, size gotmux.Size, originalIDs map[string]gotmux.PaneID) {
	t.Helper()

	if err := win.Resize(ctx, size); err != nil {
		t.Fatalf("resize to %v failed: %v", size, err)
	}

	panes, err := win.Panes(ctx)
	if err != nil || len(panes) != 4 {
		t.Fatalf("expected 4 panes after resize: %v, %v", panes, err)
	}

	assertPanesMatchOriginalIDs(t, panes, originalIDs)
	assertFourPaneNestedGeometryAndDividers(t, panes, size, originalIDs)
}

func TestAdversarialTopologicalSchedulingPreservesPrerequisites(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if err := server.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	m := makeBaseManifest()
	m.Sessions = []model.Session{
		{ID: "s0", Name: model.Bytes("adversarial-lead"), Directory: model.Bytes("/tmp"), Group: "g0"},
		{ID: "s1", Name: model.Bytes("adversarial-foll"), Directory: model.Bytes("/tmp"), Group: "g0"},
	}
	m.Groups = []model.Group{
		{ID: "g0", Name: model.Bytes("adv-group"), Members: []string{"s0", "s1"}},
	}
	m.Windows = []model.Window{
		{
			ID: "w0", Name: model.Bytes("win-split"), Width: 80, Height: 24,
			Layout: model.Layout{
				Width: 80, Height: 24, Axis: "x", Children: []model.Layout{
					{Width: 39, Height: 24, Pane: "p0"},
					{Width: 40, Height: 24, Left: 40, Pane: "p1"},
				},
			},
		},
		{
			ID: "w1", Name: model.Bytes("win-single"), Width: 80, Height: 24,
			Layout: model.Layout{Width: 80, Height: 24, Pane: "p2"},
		},
	}
	m.Panes = []model.Pane{
		{ID: "p0", Window: "w0", Index: 0, Width: 39, Height: 24, Title: model.Bytes("adv-p0-init"), Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p1", Window: "w0", Index: 1, Width: 40, Height: 24, Left: 40, Title: model.Bytes("adv-p1-split"), Active: false, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p2", Window: "w1", Index: 0, Width: 80, Height: 24, Title: model.Bytes("adv-p2-init"), Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
	}
	m.Links = []model.Link{
		{Session: "s0", Window: "w0", Index: 1, Active: true, Last: false},
		{Session: "s0", Window: "w1", Index: 3, Active: false, Last: true},

		{Session: "s1", Window: "w0", Index: 1, Active: false, Last: true},
		{Session: "s1", Window: "w1", Index: 3, Active: true, Last: false},
	}

	plan, err := BuildPlan(m, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("ValidatePlan failed: %v", err)
	}

	target := executeRestorePlan(t, ctx, server, m, adversarialStepOrder(t, plan), nil, nil)

	assertAdversarialResults(t, ctx, target)
}

func assertAdversarialResults(t *testing.T, ctx context.Context, target *restoredTarget) {
	t.Helper()

	leadSess := target.sessions["s0"]
	follSess := target.sessions["s1"]

	_, lLinks, err := leadSess.WindowInfos(ctx)
	if err != nil || len(lLinks) != 2 {
		t.Fatalf("expected 2 links in leader: %v, %v", lLinks, err)
	}

	_, fLinks, err := follSess.WindowInfos(ctx)
	if err != nil || len(fLinks) != 2 {
		t.Fatalf("expected 2 links in follower: %v, %v", fLinks, err)
	}

	assertActiveAndLast(t, lLinks, 1, 3, "s0")
	assertActiveAndLast(t, fLinks, 3, 1, "s1")

	target.assertPaneRectangles(t, ctx, map[string][4]int{
		"p0": {0, 0, 39, 24},
		"p1": {40, 0, 40, 24},
	})

	w0Info, err := target.windows["w0"].Info(ctx)
	if err != nil || w0Info.Name != "win-split" {
		t.Fatalf("w0 name mismatch after adversarial execution: got %q, want %q", w0Info.Name, "win-split")
	}

	w1Info, err := target.windows["w1"].Info(ctx)
	if err != nil || w1Info.Name != "win-single" {
		t.Fatalf("w1 name mismatch after adversarial execution: got %q, want %q", w1Info.Name, "win-single")
	}

	for _, check := range []struct {
		paneID    string
		wantTitle string
	}{
		{"p0", "adv-p0-init"},
		{"p1", "adv-p1-split"},
		{"p2", "adv-p2-init"},
	} {
		pInfo, err := target.panes[check.paneID].Info(ctx)
		if err != nil {
			t.Fatalf("failed to query pane %s info: %v", check.paneID, err)
		}

		if pInfo.Title != check.wantTitle {
			t.Fatalf("pane %s title mismatch after adversarial execution: got %q, want %q", check.paneID, pInfo.Title, check.wantTitle)
		}
	}
}

func TestUnrelatedFixturesPreservedAcrossTargetOptions(t *testing.T) {
	for _, base := range []int{0, 1, 5} {
		t.Run(fmt.Sprintf("base_%d", base), func(t *testing.T) {
			server := tmuxtest.NewServer(t)

			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()

			if err := server.GlobalSessionOptions().SetBaseIndex(ctx, base); err != nil {
				t.Fatal(err)
			}

			if err := server.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
				t.Fatal(err)
			}

			u1, err := server.NewSession(ctx, gotmux.NewSessionOptions{
				Name: "unrelated-one", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly,
			})
			if err != nil {
				t.Fatal(err)
			}

			u2, err := server.NewSession(ctx, gotmux.NewSessionOptions{
				Name: "unrelated-two", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly,
			})
			if err != nil {
				t.Fatal(err)
			}

			manifest := nestedLayoutFixture()

			plan, err := BuildPlan(manifest, []string{"s0"})
			if err != nil {
				t.Fatal(err)
			}

			executeRestorePlan(t, ctx, server, manifest, plan, nil, nil)

			verifyUnrelatedSessionsIntact(t, ctx, u1, u2, base)
		})
	}
}

func verifyUnrelatedSessionsIntact(t *testing.T, ctx context.Context, u1, u2 gotmux.Session, base int) {
	t.Helper()

	u1Info, err := u1.Info(ctx)
	if err != nil || u1Info.Name != "unrelated-one" {
		t.Fatalf("unrelated-one mutated: %v, %v", u1Info, err)
	}

	_, u1Links, err := u1.WindowInfos(ctx)
	if err != nil || len(u1Links) != 1 || u1Links[0].Index != base {
		t.Fatalf("unrelated-one window links corrupted: %v", u1Links)
	}

	u2Info, err := u2.Info(ctx)
	if err != nil || u2Info.Name != "unrelated-two" {
		t.Fatalf("unrelated-two mutated: %v, %v", u2Info, err)
	}

	_, u2Links, err := u2.WindowInfos(ctx)
	if err != nil || len(u2Links) != 1 || u2Links[0].Index != base {
		t.Fatalf("unrelated-two window links corrupted: %v", u2Links)
	}
}

type restoredTarget struct {
	sessions map[string]gotmux.Session
	windows  map[string]gotmux.Window
	panes    map[string]gotmux.Pane
}

type restoreObserver func(step Step, resolve func() *restoredTarget)

func executeRestorePlan(
	t *testing.T,
	ctx context.Context,
	server *gotmux.Server,
	manifest model.Manifest,
	plan Plan,
	beforeStep, afterStep restoreObserver,
) *restoredTarget {
	t.Helper()

	journal := newMemoryJournal()

	if beforeStep != nil {
		journal.beforeIntent = func(step Step) error {
			beforeStep(step, func() *restoredTarget { return resolveRestoredTarget(t, ctx, server, plan, journal) })

			return nil
		}
	}

	if afterStep != nil {
		journal.beforeConfirmed = func(step Step) error {
			afterStep(step, func() *restoredTarget { return resolveRestoredTarget(t, ctx, server, plan, journal) })

			return nil
		}
	}

	if _, err := NewExecutor(server, manifest, DefaultExecuteConfig()).Execute(ctx, plan, journal); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	return resolveRestoredTarget(t, ctx, server, plan, journal)
}

type initialPaneSource struct {
	window string
	pane   string
}

func resolveRestoredTarget(t *testing.T, ctx context.Context, server *gotmux.Server, plan Plan, journal *memoryJournal) *restoredTarget {
	t.Helper()

	target := &restoredTarget{
		sessions: make(map[string]gotmux.Session),
		windows:  make(map[string]gotmux.Window),
		panes:    make(map[string]gotmux.Pane),
	}
	splitPanes := make(map[gotmux.PaneID]bool)

	var initialPanes []initialPaneSource

	for _, step := range plan.Steps {
		if initial, ok := target.recordStep(t, ctx, server, step, journal.state(step.ID()).RuntimeID, splitPanes); ok {
			initialPanes = append(initialPanes, initial)
		}
	}

	for _, initial := range initialPanes {
		if window, ok := target.windows[initial.window]; ok {
			target.panes[initial.pane] = resolveInitialPane(t, ctx, server, window, splitPanes)
		}
	}

	return target
}

func (target *restoredTarget) recordStep(
	t *testing.T,
	ctx context.Context,
	server *gotmux.Server,
	step Step,
	runtimeID string,
	splitPanes map[gotmux.PaneID]bool,
) (initialPaneSource, bool) {
	t.Helper()

	switch s := step.(type) {
	case *CreateSessionStep:
		if runtimeID != "" {
			target.sessions[s.SessionID] = resolveRuntimeSession(t, ctx, server, runtimeID)
		}

		return initialPaneSource{window: s.InitialWindowID, pane: s.InitialPaneID}, s.InitialWindowID != ""
	case *PlaceInitialWindowStep:
		if runtimeID != "" && s.WindowID != "" {
			target.windows[s.WindowID] = resolveRuntimeWindow(t, ctx, server, runtimeID)
		}
	case *CreateWindowStep:
		if runtimeID != "" {
			target.windows[s.WindowID] = resolveRuntimeWindow(t, ctx, server, runtimeID)
		}

		return initialPaneSource{window: s.WindowID, pane: s.InitialPaneID}, true
	case *SplitPaneStep:
		if runtimeID != "" {
			target.panes[s.PaneID] = resolveRuntimePane(t, ctx, server, gotmux.PaneID(runtimeID))
			splitPanes[gotmux.PaneID(runtimeID)] = true
		}
	}

	return initialPaneSource{}, false
}

func resolveRuntimeSession(t *testing.T, ctx context.Context, server *gotmux.Server, runtimeID string) gotmux.Session {
	t.Helper()

	session, err := server.Session(ctx, gotmux.SessionID(runtimeID))
	if err != nil {
		t.Fatalf("restored session %s: %v", runtimeID, err)
	}

	return session
}

func resolveRuntimeWindow(t *testing.T, ctx context.Context, server *gotmux.Server, runtimeID string) gotmux.Window {
	t.Helper()

	window, err := server.Window(ctx, gotmux.WindowID(runtimeID))
	if err != nil {
		t.Fatalf("restored window %s: %v", runtimeID, err)
	}

	return window
}

func resolveRuntimePane(t *testing.T, ctx context.Context, server *gotmux.Server, runtimeID gotmux.PaneID) gotmux.Pane {
	t.Helper()

	pane, err := server.Pane(ctx, runtimeID)
	if err != nil {
		t.Fatalf("restored pane %s: %v", runtimeID, err)
	}

	return pane
}

func resolveInitialPane(t *testing.T, ctx context.Context, server *gotmux.Server, window gotmux.Window, splitPanes map[gotmux.PaneID]bool) gotmux.Pane {
	t.Helper()

	panes, err := window.Panes(ctx)
	if err != nil {
		t.Fatalf("restored window %s panes: %v", window.ID(), err)
	}

	var initial []gotmux.PaneID

	for _, pane := range panes {
		if !splitPanes[pane.ID] {
			initial = append(initial, pane.ID)
		}
	}

	if len(initial) != 1 {
		t.Fatalf("restored window %s has initial panes %v, want exactly one", window.ID(), initial)
	}

	return resolveRuntimePane(t, ctx, server, initial[0])
}

func adversarialStepOrder(t *testing.T, plan Plan) Plan {
	t.Helper()

	remaining := slices.Clone(plan.Steps)
	completed := make(map[string]bool, len(remaining))
	ordered := make([]Step, 0, len(remaining))

	for len(remaining) != 0 {
		chosen := -1

		for i, step := range slices.Backward(remaining) {
			if dependenciesComplete(step, completed) {
				chosen = i

				break
			}
		}

		if chosen < 0 {
			t.Fatal("deadlock: no schedulable step in plan DAG")
		}

		step := remaining[chosen]
		ordered = append(ordered, step)
		completed[step.ID()] = true
		remaining = slices.Delete(remaining, chosen, chosen+1)
	}

	reordered := plan
	reordered.Steps = ordered

	return reordered
}

func (target *restoredTarget) assertRectangles(t *testing.T, ctx context.Context) {
	t.Helper()

	target.assertPaneRectangles(t, ctx, map[string][4]int{
		"p0": {0, 0, 39, 8}, "p1": {0, 9, 39, 15},
		"p2": {40, 0, 40, 17}, "p3": {40, 18, 40, 6},
	})
}

func (target *restoredTarget) assertPaneRectangles(t *testing.T, ctx context.Context, want map[string][4]int) {
	t.Helper()

	for id, expected := range want {
		pane, ok := target.panes[id]
		if !ok {
			t.Fatalf("pane %s was not restored", id)
		}

		info, err := pane.Info(ctx)
		if err != nil {
			t.Fatalf("failed to query pane %s: %v", id, err)
		}

		got := [4]int{info.Left, info.Top, info.Width, info.Height}
		if diff := cmp.Diff(expected, got); diff != "" {
			t.Fatalf("pane %s rectangle mismatch (-want +got):\n%s", id, diff)
		}
	}
}

func assertActiveAndLast(t *testing.T, links []gotmux.WindowLinkInfo, wantActive, wantLast int, sessionLabel string) {
	t.Helper()

	foundActive := false
	foundLast := false

	for _, l := range links {
		if l.Index == wantActive && l.Active {
			foundActive = true
		}

		if wantLast >= 0 && l.Index == wantLast && strings.Contains(l.Flags, "-") {
			foundLast = true
		}
	}

	if !foundActive {
		t.Fatalf("%s: expected active window at slot %d, but links were: %v", sessionLabel, wantActive, links)
	}

	if wantLast >= 0 && !foundLast {
		t.Fatalf("%s: expected last window with '-' flag at slot %d, but links were: %v", sessionLabel, wantLast, links)
	}
}

func assertSlotWindowID(t *testing.T, ctx context.Context, sess gotmux.Session, index int, wantWindowID gotmux.WindowID, label string) {
	t.Helper()

	_, links, err := sess.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("%s: WindowInfos failed: %v", label, err)
	}

	for _, l := range links {
		if l.Index == index {
			if l.WindowID != wantWindowID {
				t.Fatalf("%s slot %d: WindowID = %v, want %v", label, index, l.WindowID, wantWindowID)
			}

			return
		}
	}

	t.Fatalf("%s: slot %d not found in session links: %v", label, index, links)
}

func makeBaseManifest() model.Manifest {
	return model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  1000,
		Finished: 2000,
		Source: model.Source{
			TmuxVersion:    "3.6",
			LibraryVersion: "v0.3.0",
			Consistency:    "checkpoint",
		},
		Evidence: []model.Evidence{
			{Domain: "S01", Status: statusVerified},
			{Domain: "S02", Status: statusVerified},
			{Domain: "S03", Status: statusVerified},
			{Domain: "S04", Status: statusVerified},
			{Domain: "S05", Status: statusVerified},
			{Domain: "S06", Status: statusVerified},
			{Domain: "S07", Status: statusVerified},
			{Domain: "S08", Status: statusVerified},
			{Domain: "S09", Status: statusVerified},
			{Domain: "S10", Status: statusVerified},
		},
	}
}

func deepAsymmetricLayoutFixture() model.Manifest {
	m := makeBaseManifest()
	m.Sessions = []model.Session{
		{ID: "s0", Name: model.Bytes("main-deep"), Directory: model.Bytes("/tmp")},
	}
	m.Windows = []model.Window{
		{
			ID:     "w0",
			Name:   model.Bytes("deep-asym"),
			Width:  121,
			Height: 41,
			Zoomed: true,
			Layout: model.Layout{
				Width: 121, Height: 41, Axis: "x",
				Children: []model.Layout{
					{
						Width: 29, Height: 41, Axis: "y",
						Children: []model.Layout{
							{Width: 29, Height: 15, Pane: "p0"},
							{Width: 29, Height: 25, Top: 16, Pane: "p1"},
						},
					},
					{
						Width: 50, Height: 41, Left: 30, Axis: "y",
						Children: []model.Layout{
							{Width: 50, Height: 10, Left: 30, Pane: "p2"},
							{
								Width: 50, Height: 19, Left: 30, Top: 11, Axis: "x",
								Children: []model.Layout{
									{Width: 24, Height: 19, Left: 30, Top: 11, Pane: "p3"},
									{Width: 25, Height: 19, Left: 55, Top: 11, Pane: "p4"},
								},
							},
							{Width: 50, Height: 10, Left: 30, Top: 31, Pane: "p5"},
						},
					},
					{
						Width: 40, Height: 41, Left: 81, Axis: "y",
						Children: []model.Layout{
							{Width: 40, Height: 38, Left: 81, Pane: "p6"},
							{
								Width: 40, Height: 2, Left: 81, Top: 39, Axis: "x",
								Children: []model.Layout{
									{Width: 19, Height: 2, Left: 81, Top: 39, Pane: "p7"},
									{Width: 20, Height: 2, Left: 101, Top: 39, Pane: "p8"},
								},
							},
						},
					},
				},
			},
		},
	}
	m.Links = []model.Link{
		{Session: "s0", Window: "w0", Index: 0, Active: true, Last: false},
	}
	m.Panes = []model.Pane{
		{ID: "p0", Window: "w0", Index: 0, Width: 29, Height: 15, Left: 0, Top: 0, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p1", Window: "w0", Index: 1, Width: 29, Height: 25, Left: 0, Top: 16, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p2", Window: "w0", Index: 2, Width: 50, Height: 10, Left: 30, Top: 0, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p3", Window: "w0", Index: 3, Width: 24, Height: 19, Left: 30, Top: 11, Active: true, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p4", Window: "w0", Index: 4, Width: 25, Height: 19, Left: 55, Top: 11, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p5", Window: "w0", Index: 5, Width: 50, Height: 10, Left: 30, Top: 31, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p6", Window: "w0", Index: 6, Width: 40, Height: 38, Left: 81, Top: 0, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p7", Window: "w0", Index: 7, Width: 19, Height: 2, Left: 81, Top: 39, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
		{ID: "p8", Window: "w0", Index: 8, Width: 20, Height: 2, Left: 101, Top: 39, Directory: model.Bytes("/tmp"), Launch: model.Launch{Recovery: statusUnverified}},
	}

	return m
}

type independentPaneState struct {
	Title  string
	Left   int
	Top    int
	Width  int
	Height int
	Active bool
}

type independentWindowState struct {
	Name            string
	Width           int
	Height          int
	Zoomed          bool
	ActivePaneTitle string
	Panes           []independentPaneState
}

type independentSessionState struct {
	Name       string
	Group      string
	Slots      map[int]string
	ActiveSlot int
	LastSlot   int
}

type independentSourceState struct {
	Sessions      map[string]independentSessionState
	Windows       map[string]independentWindowState
	SourceSessIDs map[string]gotmux.SessionID
	SourceWinIDs  map[string]gotmux.WindowID
	SourcePaneIDs map[string]gotmux.PaneID
	SourcePIDs    map[string]int
}

func readIndependentSourceState(t *testing.T, ctx context.Context, server *gotmux.Server, sessionNames []string) *independentSourceState {
	t.Helper()

	state := &independentSourceState{
		Sessions:      make(map[string]independentSessionState),
		Windows:       make(map[string]independentWindowState),
		SourceSessIDs: make(map[string]gotmux.SessionID),
		SourceWinIDs:  make(map[string]gotmux.WindowID),
		SourcePaneIDs: make(map[string]gotmux.PaneID),
		SourcePIDs:    make(map[string]int),
	}

	for _, name := range sessionNames {
		sess, err := server.FindSession(ctx, name)
		if err != nil {
			t.Fatalf("FindSession(%s) failed: %v", name, err)
		}

		info, err := sess.Info(ctx)
		if err != nil {
			t.Fatalf("sess.Info failed: %v", err)
		}

		groupName := ""
		if grp, ok := info.Group.Get(); ok {
			groupName = grp
		}

		state.Sessions[name] = readSessionLinks(t, ctx, sess, name, groupName, state)
	}

	return state
}

func readSessionLinks(t *testing.T, ctx context.Context, sess gotmux.Session, name, groupName string, state *independentSourceState) independentSessionState {
	t.Helper()

	_, links, err := sess.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("WindowInfos failed: %v", err)
	}

	sessState := independentSessionState{
		Name:       name,
		Group:      groupName,
		Slots:      make(map[int]string),
		ActiveSlot: -1,
		LastSlot:   -1,
	}
	state.SourceSessIDs[name] = sess.ID()

	for _, l := range links {
		if l.Active {
			sessState.ActiveSlot = l.Index
		}

		if strings.Contains(l.Flags, "-") {
			sessState.LastSlot = l.Index
		}

		win := l.Handle().Window()

		wInfo, err := win.Info(ctx)
		if err != nil {
			t.Fatalf("win.Info failed: %v", err)
		}

		sessState.Slots[l.Index] = wInfo.Name
		state.SourceWinIDs[wInfo.Name] = win.ID()

		if _, exists := state.Windows[wInfo.Name]; !exists {
			state.Windows[wInfo.Name] = readIndependentWindowState(t, ctx, win, wInfo, state)
		}
	}

	return sessState
}

func readIndependentWindowState(t *testing.T, ctx context.Context, win gotmux.Window, wInfo gotmux.WindowInfo, state *independentSourceState) independentWindowState {
	t.Helper()

	panes, err := win.Panes(ctx)
	if err != nil {
		t.Fatalf("win.Panes failed: %v", err)
	}

	activePane, err := win.ActivePane(ctx)
	if err != nil {
		t.Fatalf("win.ActivePane failed: %v", err)
	}

	activePaneInfo, err := activePane.Info(ctx)
	if err != nil {
		t.Fatalf("activePane.Info failed: %v", err)
	}

	if wInfo.Zoomed {
		if err := activePane.ToggleZoom(ctx); err != nil {
			t.Fatalf("unzoom in readIndependentSourceState: %v", err)
		}

		panes, err = win.Panes(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}

	winState := independentWindowState{
		Name:            wInfo.Name,
		Width:           wInfo.Width,
		Height:          wInfo.Height,
		Zoomed:          wInfo.Zoomed,
		ActivePaneTitle: activePaneInfo.Title,
		Panes:           make([]independentPaneState, 0, len(panes)),
	}

	for _, p := range panes {
		state.SourcePaneIDs[p.Title] = p.ID
		state.SourcePIDs[p.Title] = p.PID

		winState.Panes = append(winState.Panes, independentPaneState{
			Title:  p.Title,
			Left:   p.Left,
			Top:    p.Top,
			Width:  p.Width,
			Height: p.Height,
			Active: p.Active,
		})
	}

	if wInfo.Zoomed {
		if err := activePane.ToggleZoom(ctx); err != nil {
			t.Fatalf("re-zoom in readIndependentSourceState: %v", err)
		}
	}

	slices.SortFunc(winState.Panes, func(a, b independentPaneState) int {
		if a.Top != b.Top {
			return a.Top - b.Top
		}

		return a.Left - b.Left
	})

	return winState
}

func createSourceGroup(t *testing.T, ctx context.Context, server *gotmux.Server) (gotmux.Session, gotmux.Session, gotmux.Window) {
	t.Helper()

	if err := server.GlobalSessionOptions().SetBaseIndex(ctx, 2); err != nil {
		t.Fatal(err)
	}

	if err := server.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	leadSess, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:  "src-group",
		Size:  gotmux.Size{Width: 80, Height: 24},
		Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create source-lead session: %v", err)
	}

	_, lLinks, err := leadSess.WindowInfos(ctx)
	if err != nil || len(lLinks) != 1 {
		t.Fatalf("expected 1 initial window link in lead, got %v: %v", lLinks, err)
	}

	initWin := lLinks[0].Handle().Window()
	if err := initWin.Rename(ctx, "win-shared"); err != nil {
		t.Fatalf("rename initWin: %v", err)
	}

	initPane, err := initWin.ActivePane(ctx)
	if err != nil {
		t.Fatalf("active pane of initWin: %v", err)
	}

	if err := initPane.SetTitle(ctx, "title-shared-p0"); err != nil {
		t.Fatalf("SetTitle initPane: %v", err)
	}

	follSess, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:  "source-foll",
		Group: "src-group",
		Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create source-foll session: %v", err)
	}

	if err := leadSess.Rename(ctx, "source-lead"); err != nil {
		t.Fatalf("rename source-lead session: %v", err)
	}

	return leadSess, follSess, initWin
}

func createSourceNestedWindow(t *testing.T, ctx context.Context, leadSess gotmux.Session) gotmux.WindowLink {
	t.Helper()

	slot5 := 5

	nestedLink, err := leadSess.NewWindow(ctx, gotmux.NewWindowOptions{
		Name:    "win-nested",
		Index:   &slot5,
		Program: gotmux.Exec("sleep", "300"),
	})
	if err != nil {
		t.Fatalf("create win-nested: %v", err)
	}

	nestedWin := nestedLink.Window()
	if err := nestedWin.Resize(ctx, gotmux.Size{Width: 120, Height: 40}); err != nil {
		t.Fatalf("resize win-nested: %v", err)
	}

	nestedP0, err := nestedWin.ActivePane(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := nestedP0.SetTitle(ctx, "title-nested-p0"); err != nil {
		t.Fatal(err)
	}

	nestedP1, err := nestedP0.Split(ctx, gotmux.SplitOptions{
		Direction: gotmux.DirectionHorizontal,
		Size:      gotmux.SplitSize{Cells: 50},
		Program:   gotmux.Exec("sleep", "300"),
	})
	if err != nil {
		t.Fatalf("split horizontal: %v", err)
	}

	if err := nestedP1.SetTitle(ctx, "title-nested-p1"); err != nil {
		t.Fatal(err)
	}

	nestedP2, err := nestedP1.Split(ctx, gotmux.SplitOptions{
		Direction: gotmux.DirectionVertical,
		Size:      gotmux.SplitSize{Cells: 15},
		Program:   gotmux.Exec("sleep", "300"),
	})
	if err != nil {
		t.Fatalf("split vertical: %v", err)
	}

	if err := nestedP2.SetTitle(ctx, ""); err != nil {
		t.Fatal(err)
	}

	if err := nestedP1.Select(ctx); err != nil {
		t.Fatalf("select nestedP1: %v", err)
	}

	if err := nestedP1.ToggleZoom(ctx); err != nil {
		t.Fatalf("zoom nestedP1: %v", err)
	}

	return nestedLink
}

func selectSlot(t *testing.T, ctx context.Context, sess gotmux.Session, slot int) {
	t.Helper()

	_, links, err := sess.WindowInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, l := range links {
		if l.Index == slot {
			if err := l.Handle().Select(ctx); err != nil {
				t.Fatal(err)
			}

			return
		}
	}

	t.Fatalf("slot %d not found in session", slot)
}

func configureGroupNavigation(t *testing.T, ctx context.Context, leadSess, follSess gotmux.Session, nestedLink gotmux.WindowLink) {
	t.Helper()

	if err := nestedLink.Select(ctx); err != nil {
		t.Fatal(err)
	}

	selectSlot(t, ctx, leadSess, 2)
	selectSlot(t, ctx, follSess, 2)
	selectSlot(t, ctx, follSess, 5)
}

func createSourceRepeatSession(t *testing.T, ctx context.Context, server *gotmux.Server, initWin gotmux.Window) gotmux.Session {
	t.Helper()

	repeatSess, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "src-repeat",
		Size:    gotmux.Size{Width: 80, Height: 24},
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create src-repeat session: %v", err)
	}

	slot3 := 3
	if _, err := initWin.Link(ctx, repeatSess, gotmux.LinkOptions{Index: &slot3}); err != nil {
		t.Fatalf("link win-shared to src-repeat:3: %v", err)
	}

	slot8 := 8
	if _, err := initWin.Link(ctx, repeatSess, gotmux.LinkOptions{Index: &slot8}); err != nil {
		t.Fatalf("link win-shared to src-repeat:8: %v", err)
	}

	_, rLinks, err := repeatSess.WindowInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, l := range rLinks {
		if l.Index != 3 && l.Index != 8 {
			if err := l.Handle().UnlinkWith(ctx, gotmux.UnlinkOptions{Force: true}); err != nil {
				t.Fatalf("unlink bootstrap window in repeat: %v", err)
			}

			break
		}
	}

	selectSlot(t, ctx, repeatSess, 3)
	selectSlot(t, ctx, repeatSess, 8)

	return repeatSess
}

func createActualSourceFixture(t *testing.T, ctx context.Context, server *gotmux.Server) (gotmux.Session, gotmux.Session, gotmux.Session) {
	t.Helper()

	leadSess, follSess, initWin := createSourceGroup(t, ctx, server)
	nestedLink := createSourceNestedWindow(t, ctx, leadSess)
	configureGroupNavigation(t, ctx, leadSess, follSess, nestedLink)
	repeatSess := createSourceRepeatSession(t, ctx, server, initWin)

	return leadSess, follSess, repeatSess
}

func findWindowIDAtSlot(t *testing.T, links []gotmux.WindowLinkInfo, slot int) gotmux.WindowID {
	t.Helper()

	for _, l := range links {
		if l.Index == slot {
			return l.WindowID
		}
	}

	t.Fatalf("slot %d not found in links: %v", slot, links)

	return ""
}

func findWindowHandle(t *testing.T, links []gotmux.WindowLinkInfo, slot int) gotmux.Window {
	t.Helper()

	for _, l := range links {
		if l.Index == slot {
			return l.Handle().Window()
		}
	}

	t.Fatalf("slot %d not found in links: %v", slot, links)

	return gotmux.Window{}
}

func commitObservationToRepo(t *testing.T, ctx context.Context, repoDir string, obs capture.Observation) string {
	t.Helper()

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	attempt, err := repo.Begin(ctx)
	if err != nil {
		t.Fatalf("repo.Begin failed: %v", err)
	}

	artifacts := make(map[string]io.Reader, len(obs.Artifacts))
	for id, data := range obs.Artifacts {
		artifacts[id] = bytes.NewReader(data)
	}

	gen, err := repo.Commit(ctx, attempt.ID, obs.Manifest, artifacts)
	if err != nil {
		t.Fatalf("repo.Commit failed: %v", err)
	}

	if err := repo.Close(); err != nil {
		t.Fatalf("repo.Close failed: %v", err)
	}

	return gen.ID
}

func runRoundtripWorkerSubprocess(t *testing.T, ctx context.Context, repoDir, genID, targetSocket string, sessionNames []string) {
	t.Helper()

	selfExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, selfExe, "-test.run=^TestStructuralRoundtripWorker$")
	cmd.Env = []string{
		testenv.IsolatedMarker + "=1",
		"SESH_STRUCTURAL_ROUNDTRIP_WORKER=1",
		"SESH_REPO_ROOT=" + repoDir,
		"SESH_GEN_ID=" + genID,
		"SESH_TARGET_SOCKET=" + targetSocket,
		"SESH_SELECTED=" + strings.Join(sessionNames, ","),
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
	}

	workerOutput, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("roundtrip worker subprocess failed: %v\nOutput:\n%s", err, string(workerOutput))
	}
}

func TestStructuralRoundtripWorker(t *testing.T) {
	if os.Getenv("SESH_STRUCTURAL_ROUNDTRIP_WORKER") == "" {
		t.Skip("subprocess entry point")
	}

	repoRoot := os.Getenv("SESH_REPO_ROOT")
	genID := os.Getenv("SESH_GEN_ID")
	targetSocket := os.Getenv("SESH_TARGET_SOCKET")
	selectedNames := strings.Split(os.Getenv("SESH_SELECTED"), ",")

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	repo, err := store.Open(ctx, repoRoot)
	if err != nil {
		t.Fatalf("worker: store.Open failed: %v", err)
	}

	manifest, err := repo.Read(ctx, genID)
	if err != nil {
		t.Fatalf("worker: repo.Read(%s) failed: %v", genID, err)
	}

	if err := repo.Close(); err != nil {
		t.Fatalf("worker: repo.Close failed: %v", err)
	}

	selectedIDs := make([]string, 0, len(selectedNames))
	for _, s := range manifest.Sessions {
		if slices.Contains(selectedNames, string(s.Name)) {
			selectedIDs = append(selectedIDs, s.ID)
		}
	}

	slices.Sort(selectedIDs)

	plan, err := BuildPlan(manifest, selectedIDs)
	if err != nil {
		t.Fatalf("worker: BuildPlan failed: %v", err)
	}

	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("worker: ValidatePlan failed: %v", err)
	}

	targetServer, err := gotmux.New(gotmux.Config{SocketPath: targetSocket})
	if err != nil {
		t.Fatalf("worker: tmux.New failed: %v", err)
	}

	executeAndVerifyOnTarget(t, ctx, targetServer, repoRoot, genID, manifest, plan)
}

func TestActualSourceDestructionRoundtrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	sourceServer := tmuxtest.NewServer(t)
	targetServer := tmuxtest.NewServer(t)

	if err := targetServer.GlobalSessionOptions().SetBaseIndex(ctx, 2); err != nil {
		t.Fatal(err)
	}

	if err := targetServer.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	scratch0, err := targetServer.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "scratch-pre-alloc-0",
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	scratch1, err := targetServer.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "scratch-pre-alloc-1",
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	unrelatedSess, err := targetServer.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "unrelated-target-sess",
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	unrelatedBeforeID := unrelatedSess.ID()

	_, uLinksBefore, err := unrelatedSess.WindowInfos(ctx)
	if err != nil || len(uLinksBefore) != 1 {
		t.Fatalf("unrelated before links: %v, %v", uLinksBefore, err)
	}

	uWinBefore := uLinksBefore[0].Handle().Window()
	uPaneInfoBefore := getFirstPaneInfo(t, ctx, uWinBefore)

	leadSess, follSess, repeatSess := createActualSourceFixture(t, ctx, sourceServer)

	sessionNames := []string{"source-lead", "source-foll", "src-repeat"}
	expected := readIndependentSourceState(t, ctx, sourceServer, sessionNames)

	obs, err := capture.Observe(ctx, sourceServer, []gotmux.SessionID{leadSess.ID(), follSess.ID(), repeatSess.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	genID := commitObservationToRepo(t, ctx, repoDir, obs)

	if err := sourceServer.Kill(ctx); err != nil {
		t.Fatalf("kill sourceServer failed: %v", err)
	}

	runRoundtripWorkerSubprocess(t, ctx, repoDir, genID, targetServer.Endpoint().SocketPath, sessionNames)

	verifyUnrelatedTargetIntact(t, ctx, targetServer, unrelatedBeforeID, uLinksBefore, uPaneInfoBefore)
	verifyTargetStructuralParity(t, ctx, targetServer, expected)

	_ = scratch0.Kill(ctx)
	_ = scratch1.Kill(ctx)
}

func createEmptyNameAndTitleSourceSession(t *testing.T, ctx context.Context, server *gotmux.Server) gotmux.Session {
	t.Helper()

	srcSess, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "source-empty",
		Size:    gotmux.Size{Width: 80, Height: 24},
		Start:   gotmux.StartPolicyExistingOnly,
		Program: gotmux.Exec("sleep", "300"),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, links, err := srcSess.WindowInfos(ctx)
	if err != nil || len(links) != 1 {
		t.Fatalf("expected 1 link, got %v: %v", links, err)
	}

	initWin := links[0].Handle().Window()
	if err := initWin.Rename(ctx, ""); err != nil {
		t.Fatalf("rename to empty: %v", err)
	}

	initPane, err := initWin.ActivePane(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := initPane.SetTitle(ctx, ""); err != nil {
		t.Fatalf("set title to empty: %v", err)
	}

	splitPane, err := initPane.Split(ctx, gotmux.SplitOptions{
		Direction: gotmux.DirectionHorizontal,
		Size:      gotmux.SplitSize{Cells: 40},
		Program:   gotmux.Exec("sleep", "300"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := splitPane.SetTitle(ctx, ""); err != nil {
		t.Fatalf("set split title to empty: %v", err)
	}

	return srcSess
}

func assertEmptyNameAndTitleTargetState(t *testing.T, ctx context.Context, targetServer *gotmux.Server, expected *independentSourceState) {
	t.Helper()

	targetSess, err := targetServer.FindSession(ctx, "source-empty")
	if err != nil {
		t.Fatalf("find target source-empty failed: %v", err)
	}

	_, targetLinks, err := targetSess.WindowInfos(ctx)
	if err != nil || len(targetLinks) != 1 {
		t.Fatalf("targetLinks mismatch: %v, %v", targetLinks, err)
	}

	targetWin := targetLinks[0].Handle().Window()

	targetWinInfo, err := targetWin.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if targetWinInfo.Name != "" {
		t.Fatalf("expected empty window name, got %q", targetWinInfo.Name)
	}

	targetPanes, err := targetWin.Panes(ctx)
	if err != nil || len(targetPanes) != 2 {
		t.Fatalf("expected 2 panes, got %v: %v", targetPanes, err)
	}

	for i, p := range targetPanes {
		if p.Title != "" {
			t.Fatalf("pane %d expected empty title, got %q", i, p.Title)
		}
	}

	expectedWin := expected.Windows[""]
	if targetWinInfo.Width != expectedWin.Width || targetWinInfo.Height != expectedWin.Height {
		t.Fatalf("window dimensions mismatch: got %dx%d, want %dx%d", targetWinInfo.Width, targetWinInfo.Height, expectedWin.Width, expectedWin.Height)
	}
}

func TestActualSourceDestructionEmptyNameAndTitleFixture(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	sourceServer := tmuxtest.NewServer(t)
	targetServer := tmuxtest.NewServer(t)

	if err := sourceServer.GlobalSessionOptions().SetBaseIndex(ctx, 2); err != nil {
		t.Fatal(err)
	}

	if err := sourceServer.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	if err := targetServer.GlobalSessionOptions().SetBaseIndex(ctx, 2); err != nil {
		t.Fatal(err)
	}

	if err := targetServer.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	srcSess := createEmptyNameAndTitleSourceSession(t, ctx, sourceServer)

	sessionNames := []string{"source-empty"}
	expected := readIndependentSourceState(t, ctx, sourceServer, sessionNames)

	obs, err := capture.Observe(ctx, sourceServer, []gotmux.SessionID{srcSess.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	genID := commitObservationToRepo(t, ctx, repoDir, obs)

	if err := sourceServer.Kill(ctx); err != nil {
		t.Fatalf("kill sourceServer failed: %v", err)
	}

	runRoundtripWorkerSubprocess(t, ctx, repoDir, genID, targetServer.Endpoint().SocketPath, sessionNames)
	assertEmptyNameAndTitleTargetState(t, ctx, targetServer, expected)
}

func getFirstPaneInfo(t *testing.T, ctx context.Context, win gotmux.Window) gotmux.PaneInfo {
	t.Helper()

	panes, err := win.Panes(ctx)
	if err != nil || len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %v: %v", panes, err)
	}

	return panes[0]
}

func verifyUnrelatedTargetIntact(t *testing.T, ctx context.Context, server *gotmux.Server, beforeID gotmux.SessionID, beforeLinks []gotmux.WindowLinkInfo, beforePane gotmux.PaneInfo) {
	t.Helper()

	sess, err := server.FindSession(ctx, "unrelated-target-sess")
	if err != nil {
		t.Fatalf("unrelated session missing: %v", err)
	}

	if sess.ID() != beforeID {
		t.Fatalf("unrelated session ID changed: got %v, want %v", sess.ID(), beforeID)
	}

	_, links, err := sess.WindowInfos(ctx)
	if err != nil || len(links) != len(beforeLinks) {
		t.Fatalf("unrelated session links corrupted: got %v, want %v", links, beforeLinks)
	}

	if links[0].Index != beforeLinks[0].Index || links[0].WindowID != beforeLinks[0].WindowID {
		t.Fatalf("unrelated link mismatch: got %v, want %v", links[0], beforeLinks[0])
	}

	win := links[0].Handle().Window()
	assertUnrelatedPaneIntact(t, ctx, win, beforePane)
}

func assertUnrelatedPaneIntact(t *testing.T, ctx context.Context, win gotmux.Window, beforePane gotmux.PaneInfo) {
	t.Helper()

	panes, err := win.Panes(ctx)
	if err != nil || len(panes) != 1 {
		t.Fatalf("unrelated panes corrupted: got %v", panes)
	}

	pInfo := panes[0]
	if pInfo.ID != beforePane.ID {
		t.Fatalf("unrelated pane ID changed: got %v, want %v", pInfo.ID, beforePane.ID)
	}

	if pInfo.PID != beforePane.PID {
		t.Fatalf("unrelated pane PID changed: got %v, want %v", pInfo.PID, beforePane.PID)
	}

	if pInfo.Left != beforePane.Left || pInfo.Top != beforePane.Top || pInfo.Width != beforePane.Width || pInfo.Height != beforePane.Height {
		t.Fatalf("unrelated pane geometry changed: got (%d,%d,%d,%d), want (%d,%d,%d,%d)",
			pInfo.Left, pInfo.Top, pInfo.Width, pInfo.Height,
			beforePane.Left, beforePane.Top, beforePane.Width, beforePane.Height)
	}
}

func assertTargetSessionAndGroupParity(t *testing.T, ctx context.Context, server *gotmux.Server, expected *independentSourceState) (gotmux.Session, gotmux.Session, gotmux.Session) {
	t.Helper()

	leadSess, err := server.FindSession(ctx, "source-lead")
	if err != nil {
		t.Fatalf("find target source-lead failed: %v", err)
	}

	follSess, err := server.FindSession(ctx, "source-foll")
	if err != nil {
		t.Fatalf("find target source-foll failed: %v", err)
	}

	repeatSess, err := server.FindSession(ctx, "src-repeat")
	if err != nil {
		t.Fatalf("find target src-repeat failed: %v", err)
	}

	assertDisjointSessionIDs(t, leadSess, follSess, repeatSess, expected)

	leadInfo, err := leadSess.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	follInfo, err := follSess.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	leadGrp, ok1 := leadInfo.Group.Get()

	follGrp, ok2 := follInfo.Group.Get()
	if !ok1 || !ok2 || leadGrp != "src-group" || follGrp != "src-group" {
		t.Fatalf("group membership mismatch: leadGrp=%q(ok=%v), follGrp=%q(ok=%v)", leadGrp, ok1, follGrp, ok2)
	}

	return leadSess, follSess, repeatSess
}

func assertDisjointSessionIDs(t *testing.T, leadSess, follSess, repeatSess gotmux.Session, expected *independentSourceState) {
	t.Helper()

	if leadSess.ID() == expected.SourceSessIDs["source-lead"] {
		t.Fatalf("target source-lead ID %v unexpectedly equals source ID %v", leadSess.ID(), expected.SourceSessIDs["source-lead"])
	}

	if follSess.ID() == expected.SourceSessIDs["source-foll"] {
		t.Fatalf("target source-foll ID %v unexpectedly equals source ID %v", follSess.ID(), expected.SourceSessIDs["source-foll"])
	}

	if repeatSess.ID() == expected.SourceSessIDs["src-repeat"] {
		t.Fatalf("target src-repeat ID %v unexpectedly equals source ID %v", repeatSess.ID(), expected.SourceSessIDs["src-repeat"])
	}
}

func assertTargetWindowSharingAndSlots(t *testing.T, ctx context.Context, leadSess, follSess, repeatSess gotmux.Session, expected *independentSourceState) ([]gotmux.WindowLinkInfo, []gotmux.WindowLinkInfo) {
	t.Helper()

	_, lLinks, err := leadSess.WindowInfos(ctx)
	if err != nil || len(lLinks) != 2 {
		t.Fatalf("leadLinks mismatch: got %v, %v", lLinks, err)
	}

	_, fLinks, err := follSess.WindowInfos(ctx)
	if err != nil || len(fLinks) != 2 {
		t.Fatalf("follLinks mismatch: got %v, %v", fLinks, err)
	}

	_, rLinks, err := repeatSess.WindowInfos(ctx)
	if err != nil || len(rLinks) != 2 {
		t.Fatalf("rLinks mismatch: got %v, %v", rLinks, err)
	}

	leadSlot2Win := findWindowIDAtSlot(t, lLinks, 2)
	leadSlot5Win := findWindowIDAtSlot(t, lLinks, 5)
	follSlot2Win := findWindowIDAtSlot(t, fLinks, 2)
	follSlot5Win := findWindowIDAtSlot(t, fLinks, 5)
	repeatSlot3Win := findWindowIDAtSlot(t, rLinks, 3)
	repeatSlot8Win := findWindowIDAtSlot(t, rLinks, 8)

	assertSharedWindowIDs(t, leadSlot2Win, leadSlot5Win, follSlot2Win, follSlot5Win, repeatSlot3Win, repeatSlot8Win, expected)

	assertActiveAndLast(t, lLinks, 2, 5, "target source-lead")
	assertActiveAndLast(t, fLinks, 5, 2, "target source-foll")
	assertActiveAndLast(t, rLinks, 8, 3, "target src-repeat")

	return lLinks, fLinks
}

func assertSharedWindowIDs(t *testing.T, leadSlot2, leadSlot5, follSlot2, follSlot5, repeatSlot3, repeatSlot8 gotmux.WindowID, expected *independentSourceState) {
	t.Helper()

	if leadSlot2 != follSlot2 {
		t.Fatalf("slot 2 window not shared across group: lead=%v, foll=%v", leadSlot2, follSlot2)
	}

	if leadSlot5 != follSlot5 {
		t.Fatalf("slot 5 window not shared across group: lead=%v, foll=%v", leadSlot5, follSlot5)
	}

	if repeatSlot3 != repeatSlot8 {
		t.Fatalf("src-repeat repeated links must share exact WindowID: slot3=%v, slot8=%v", repeatSlot3, repeatSlot8)
	}

	if repeatSlot3 != leadSlot2 {
		t.Fatalf("win-shared in repeat must equal win-shared in lead: repeat=%v, lead=%v", repeatSlot3, leadSlot2)
	}

	if leadSlot2 == expected.SourceWinIDs["win-shared"] {
		t.Fatalf("target win-shared ID %v equals source ID", leadSlot2)
	}

	if leadSlot5 == expected.SourceWinIDs["win-nested"] {
		t.Fatalf("target win-nested ID %v equals source ID", leadSlot5)
	}
}

func assertTargetNestedPanesGeometry(t *testing.T, ctx context.Context, winNested gotmux.Window, activeNested gotmux.Pane, expected *independentSourceState) {
	t.Helper()

	if err := activeNested.ToggleZoom(ctx); err != nil {
		t.Fatal(err)
	}

	targetNestedPanes, err := winNested.Panes(ctx)
	if err != nil || len(targetNestedPanes) != 3 {
		t.Fatalf("panes after unzoom: %v, %v", targetNestedPanes, err)
	}

	expectedNestedPanes := expected.Windows["win-nested"].Panes

	targetPanesSorted := make([]independentPaneState, 0, len(targetNestedPanes))
	for _, p := range targetNestedPanes {
		targetPanesSorted = append(targetPanesSorted, independentPaneState{
			Title:  p.Title,
			Left:   p.Left,
			Top:    p.Top,
			Width:  p.Width,
			Height: p.Height,
			Active: p.Handle().Equal(activeNested),
		})
	}

	slices.SortFunc(targetPanesSorted, func(a, b independentPaneState) int {
		if a.Top != b.Top {
			return a.Top - b.Top
		}

		return a.Left - b.Left
	})

	if diff := cmp.Diff(expectedNestedPanes, targetPanesSorted); diff != "" {
		t.Fatalf("win-nested pane geometry mismatch (-want +got):\n%s", diff)
	}

	if err := activeNested.ToggleZoom(ctx); err != nil {
		t.Fatal(err)
	}

	rezoomedInfo, err := winNested.Info(ctx)
	if err != nil || !rezoomedInfo.Zoomed {
		t.Fatalf("expected rezoomed: %v, %v", rezoomedInfo, err)
	}
}

func assertTargetWindowGeometryAndTitles(t *testing.T, ctx context.Context, lLinks []gotmux.WindowLinkInfo, expected *independentSourceState) {
	t.Helper()

	winShared := findWindowHandle(t, lLinks, 2)
	assertTargetSharedWindow(t, ctx, winShared, expected)

	winNested := findWindowHandle(t, lLinks, 5)

	wNestedInfo, err := winNested.Info(ctx)
	if err != nil || wNestedInfo.Name != "win-nested" || wNestedInfo.Width != 120 || wNestedInfo.Height != 40 {
		t.Fatalf("win-nested info mismatch: %v, %v", wNestedInfo, err)
	}

	if !wNestedInfo.Zoomed {
		t.Fatal("win-nested expected to be zoomed")
	}

	nPanes, err := winNested.Panes(ctx)
	if err != nil || len(nPanes) != 3 {
		t.Fatalf("winNested panes mismatch: %v, %v", nPanes, err)
	}

	activeNested := assertNestedWindowActivePane(t, ctx, winNested)

	for _, p := range nPanes {
		if p.ID == expected.SourcePaneIDs[p.Title] {
			t.Fatalf("target pane %s ID %v equals source pane ID", p.Title, p.ID)
		}
	}

	assertTargetNestedPanesGeometry(t, ctx, winNested, activeNested, expected)
}

func assertTargetSharedWindow(t *testing.T, ctx context.Context, winShared gotmux.Window, expected *independentSourceState) {
	t.Helper()

	wSharedInfo, err := winShared.Info(ctx)
	if err != nil || wSharedInfo.Name != "win-shared" || wSharedInfo.Width != 80 || wSharedInfo.Height != 24 {
		t.Fatalf("win-shared info mismatch: %v, %v", wSharedInfo, err)
	}

	sPanes, err := winShared.Panes(ctx)
	if err != nil || len(sPanes) != 1 {
		t.Fatalf("winShared panes mismatch: %v, %v", sPanes, err)
	}

	spInfo := sPanes[0]
	if spInfo.Title != "title-shared-p0" {
		t.Fatalf("winShared pane title mismatch: %v, %v", spInfo, err)
	}

	if spInfo.ID == expected.SourcePaneIDs["title-shared-p0"] {
		t.Fatalf("target pane ID %v equals source pane ID", spInfo.ID)
	}
}

func assertWindowResizeContract(t *testing.T, ctx context.Context, winShared gotmux.Window) {
	t.Helper()

	if err := winShared.Resize(ctx, gotmux.Size{Width: 100, Height: 30}); err != nil {
		t.Fatalf("resize winShared to 100x30 failed: %v", err)
	}

	sharedResizedInfo, err := winShared.Info(ctx)
	if err != nil || sharedResizedInfo.Width != 100 || sharedResizedInfo.Height != 30 {
		t.Fatalf("winShared size mismatch after resize: %v, %v", sharedResizedInfo, err)
	}
}

func assertNestedWindowActivePane(t *testing.T, ctx context.Context, winNested gotmux.Window) gotmux.Pane {
	t.Helper()

	activeNested, err := winNested.ActivePane(ctx)
	if err != nil {
		t.Fatal(err)
	}

	activeNestedInfo, err := activeNested.Info(ctx)
	if err != nil || activeNestedInfo.Title != "title-nested-p1" {
		t.Fatalf("winNested active pane title mismatch: %v, %v", activeNestedInfo, err)
	}

	if activeNestedInfo.Left != 0 || activeNestedInfo.Top != 0 || activeNestedInfo.Width != 120 || activeNestedInfo.Height != 40 {
		t.Fatalf("active nested pane while zoomed expected to fill window (0,0,120,40), got (%d,%d,%d,%d)",
			activeNestedInfo.Left, activeNestedInfo.Top, activeNestedInfo.Width, activeNestedInfo.Height)
	}

	return activeNested
}

func verifyTargetStructuralParity(t *testing.T, ctx context.Context, server *gotmux.Server, expected *independentSourceState) {
	t.Helper()

	leadSess, follSess, repeatSess := assertTargetSessionAndGroupParity(t, ctx, server, expected)
	lLinks, _ := assertTargetWindowSharingAndSlots(t, ctx, leadSess, follSess, repeatSess, expected)
	assertTargetWindowGeometryAndTitles(t, ctx, lLinks, expected)

	winShared := findWindowHandle(t, lLinks, 2)
	assertWindowResizeContract(t, ctx, winShared)
}
