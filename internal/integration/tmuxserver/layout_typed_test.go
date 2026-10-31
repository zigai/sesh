package tmuxserver

import (
	"reflect"
	"slices"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const typedSplitSample = `{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,` +
	`"c":[{"t":"p","w":40,"h":24,"x":0,"y":0,"l":0,"i":1,"I":"%0"},` +
	`{"t":"p","w":39,"h":24,"x":41,"y":0,"a":true,"i":2,"I":"%2"}]}}`

const typedLeafSample = `{"V":2,"L":{"t":"p","w":80,"h":24,"x":0,"y":0,"a":true,"i":1,"I":"%1"}}`

func TestDecodeTypedLayoutSplit(t *testing.T) {
	panes := map[string]string{"%0": "p-0", "%2": "p-2"}

	decoded, err := DecodeLayout(gotmux.Layout(typedSplitSample), panes)
	if err != nil {
		t.Fatalf("DecodeLayout failed: %v", err)
	}

	node := decoded.Tiled

	if node.Axis != "x" {
		t.Fatalf("axis = %q, want x", node.Axis)
	}

	if node.Width != 80 || node.Height != 24 || node.Left != 0 || node.Top != 0 {
		t.Fatalf("root geometry = %dx%d at %d,%d, want 80x24 at 0,0", node.Width, node.Height, node.Left, node.Top)
	}

	assertSplitChildren(t, node.Children)
}

func assertSplitChildren(t *testing.T, children []model.Layout) {
	t.Helper()

	if len(children) != 2 {
		t.Fatalf("children = %d, want 2", len(children))
	}

	want := []model.Layout{
		{Width: 40, Height: 24, Left: 0, Top: 0, Pane: "p-0"},
		{Width: 39, Height: 24, Left: 41, Top: 0, Pane: "p-2"},
	}

	for index, child := range children {
		if child.Pane != want[index].Pane || child.Width != want[index].Width || child.Left != want[index].Left {
			t.Fatalf("child %d = %#v, want %#v", index, child, want[index])
		}

		if child.Axis != "" || len(child.Children) != 0 {
			t.Fatalf("child %d must be a leaf, got %#v", index, child)
		}
	}
}

func TestDecodeTypedLayoutLeaf(t *testing.T) {
	decoded, err := DecodeLayout(gotmux.Layout(typedLeafSample), map[string]string{"%1": "p-1"})
	if err != nil {
		t.Fatalf("DecodeLayout failed: %v", err)
	}

	node := decoded.Tiled

	if node.Pane != "p-1" || node.Axis != "" || len(node.Children) != 0 {
		t.Fatalf("node = %#v, want single leaf pane p-1", node)
	}

	if node.Width != 80 || node.Height != 24 {
		t.Fatalf("node geometry = %dx%d, want 80x24", node.Width, node.Height)
	}
}

func TestDecodeTypedLayoutRejections(t *testing.T) {
	panes := map[string]string{"%0": "p-0", "%2": "p-2"}

	cases := map[string]string{
		"unknown version":      `{"V":3,"L":{"t":"p","w":80,"h":24,"x":0,"y":0,"I":"%0"}}`,
		"unknown node type":    `{"V":2,"L":{"t":"z","w":80,"h":24,"x":0,"y":0}}`,
		"unknown pane id":      `{"V":2,"L":{"t":"p","w":80,"h":24,"x":0,"y":0,"I":"%9"}}`,
		"missing pane id":      `{"V":2,"L":{"t":"p","w":80,"h":24,"x":0,"y":0}}`,
		"single child branch":  `{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":80,"h":24,"x":0,"y":0,"I":"%0"}]}}`,
		"leaf with children":   `{"V":2,"L":{"t":"p","w":80,"h":24,"x":0,"y":0,"I":"%0","c":[{"t":"p","w":80,"h":24,"x":0,"y":0,"I":"%2"}]}}`,
		"negative dimension":   `{"V":2,"L":{"t":"p","w":-1,"h":24,"x":0,"y":0,"I":"%0"}}`,
		"negative offset":      `{"V":2,"L":{"t":"p","w":80,"h":24,"x":-1,"y":0,"I":"%0"}}`,
		"not a document":       `{"L":{"t":"p","w":80,"h":24,"x":0,"y":0,"I":"%0"}}`,
		"malformed json":       `{"V":2,"L":`,
		"branch without child": `{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0}}`,
		"only floating panes": `{"V":2,"L":{"t":"v","w":80,"h":24,"x":0,"y":0,"c":[` +
			`{"t":"p","w":20,"h":5,"x":1,"y":1,"i":1,"z":0,"I":"%0"},{"t":"p","w":20,"h":5,"x":30,"y":10,"a":true,"i":2,"z":1,"I":"%2"}]}}`,
	}

	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeLayout(gotmux.Layout(value), panes); err == nil {
				t.Fatalf("accepted invalid typed layout %q", value)
			}
		})
	}
}

func TestDecodeNativeLayoutUnchanged(t *testing.T) {
	body := "80x24,0,0{40x24,0,0,0,39x24,41,0,2}"
	value := gotmux.Layout(layoutChecksum(body) + "," + body)

	decoded, err := DecodeLayout(value, map[string]string{"%0": "p-0", "%2": "p-2"})
	if err != nil {
		t.Fatalf("native decode failed: %v", err)
	}

	node := decoded.Tiled

	if node.Axis != "x" || len(node.Children) != 2 {
		t.Fatalf("native node = %#v, want x branch with 2 children", node)
	}

	if node.Children[1].Pane != "p-2" || node.Children[1].Left != 41 {
		t.Fatalf("native child = %#v, want pane p-2 at left 41", node.Children[1])
	}
}

func TestDecodeTypedLayoutSeparatesFloatingPanes(t *testing.T) {
	panes := map[string]string{"%0": "p-0", "%1": "p-1", "%2": "p-2", "%3": "p-3"}

	tests := []struct {
		name         string
		layout       string
		wantTiled    model.Layout
		wantFloating []string
		wantRecent   string
		wantLast     string
	}{
		{
			name: "floating pane over a single tiled pane",
			layout: `{"V":2,"L":{"t":"v","w":80,"h":24,"x":0,"y":0,"c":[` +
				`{"t":"p","w":80,"h":24,"x":0,"y":0,"a":true,"i":1,"I":"%0"},` +
				`{"t":"p","w":1,"h":1,"x":40,"y":20,"l":0,"i":2,"z":0,"I":"%1"}]}}`,
			wantTiled:    model.Layout{Width: 80, Height: 24, Pane: "p-0"},
			wantFloating: []string{"p-1"},
			wantRecent:   "p-0",
			wantLast:     "",
		},
		{
			name: "nested floating pane collapses its split",
			layout: `{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[` +
				`{"t":"v","w":40,"h":24,"x":0,"y":0,"c":[` +
				`{"t":"p","w":40,"h":24,"x":0,"y":0,"l":1,"i":1,"I":"%0"},` +
				`{"t":"p","w":30,"h":10,"x":5,"y":5,"a":true,"i":2,"z":0,"I":"%1"}]},` +
				`{"t":"p","w":39,"h":24,"x":41,"y":0,"l":0,"i":3,"I":"%2"}]}}`,
			wantTiled: model.Layout{Width: 80, Height: 24, Axis: "x", Children: []model.Layout{
				{Width: 40, Height: 24, Pane: "p-0"},
				{Width: 39, Height: 24, Left: 41, Pane: "p-2"},
			}},
			wantFloating: []string{"p-1"},
			wantRecent:   "p-2",
			wantLast:     "p-0",
		},
		{
			name: "several floating panes keep stacking order",
			layout: `{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[` +
				`{"t":"p","w":40,"h":24,"x":0,"y":0,"a":true,"i":1,"I":"%0"},` +
				`{"t":"p","w":10,"h":5,"x":2,"y":2,"i":2,"z":1,"I":"%3"},` +
				`{"t":"p","w":39,"h":24,"x":41,"y":0,"l":0,"i":3,"I":"%2"},` +
				`{"t":"p","w":10,"h":5,"x":50,"y":2,"i":4,"z":0,"I":"%1"}]}}`,
			wantTiled: model.Layout{Width: 80, Height: 24, Axis: "x", Children: []model.Layout{
				{Width: 40, Height: 24, Pane: "p-0"},
				{Width: 39, Height: 24, Left: 41, Pane: "p-2"},
			}},
			wantFloating: []string{"p-3", "p-1"},
			wantRecent:   "p-0",
			wantLast:     "p-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoded, err := DecodeLayout(gotmux.Layout(tt.layout), panes)
			if err != nil {
				t.Fatalf("DecodeLayout failed: %v", err)
			}

			if !reflect.DeepEqual(decoded.Tiled, tt.wantTiled) {
				t.Fatalf("tiled layout = %#v, want %#v", decoded.Tiled, tt.wantTiled)
			}

			if !slices.Equal(decoded.Floating, tt.wantFloating) {
				t.Fatalf("floating panes = %v, want %v", decoded.Floating, tt.wantFloating)
			}

			if decoded.Recent != tt.wantRecent {
				t.Fatalf("recent pane = %q, want %q", decoded.Recent, tt.wantRecent)
			}

			if decoded.Last != tt.wantLast {
				t.Fatalf("last pane = %q, want %q", decoded.Last, tt.wantLast)
			}
		})
	}
}

func TestDecodeNativeLayoutSeparatesFloatingPanes(t *testing.T) {
	body := "80x24,0,0[80x24,0,0,0,20x5,3,3,1]<20x5,3,3,1>"
	value := gotmux.Layout(layoutChecksum(body) + "," + body)

	decoded, err := DecodeLayout(value, map[string]string{"%0": "p-0", "%1": "p-1"})
	if err != nil {
		t.Fatalf("native decode failed: %v", err)
	}

	want := model.Layout{Width: 80, Height: 24, Pane: "p-0"}
	if !reflect.DeepEqual(decoded.Tiled, want) || !slices.Equal(decoded.Floating, []string{"p-1"}) {
		t.Fatalf("decoded = %#v, want tiled %#v and floating [p-1]", decoded, want)
	}
}
