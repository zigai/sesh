package tmuxserver

import (
	"encoding/json"
	"fmt"
	"math/bits"
	"slices"
	"strconv"
	"strings"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	maxLayoutDepth     = 64
	typedLayoutVersion = 2
	minBranchChildren  = 2
	activeRank         = -1
)

type layoutReader struct {
	text     string
	position int
	panes    map[string]string
}

// WindowLayout lists Floating panes in stacking order; they take no space in Tiled.
// Recent and Last are the most and second most recently used tiled panes, known only
// from typed layouts.
type WindowLayout struct {
	Tiled    model.Layout
	Floating []string
	Recent   string
	Last     string
}

type typedDecoder struct {
	panes      map[string]string
	floating   []string
	recent     string
	recentRank int
	last       string
	lastRank   int
}

func DecodeLayout(value gotmux.Layout, panes map[string]string) (WindowLayout, error) {
	text := string(value)
	if strings.HasPrefix(strings.TrimSpace(text), "{") {
		return decodeTypedLayout(text, panes)
	}

	checksum, body, ok := strings.Cut(text, ",")
	if !ok || checksum != layoutChecksum(body) {
		return WindowLayout{}, model.ErrInvalid
	}

	reader := layoutReader{text: body, position: 0, panes: panes}

	node, err := reader.node(0)
	if err != nil {
		return WindowLayout{}, model.ErrInvalid
	}

	floating, err := reader.floating()
	if err != nil || reader.position != len(body) {
		return WindowLayout{}, model.ErrInvalid
	}

	if len(floating) == 0 {
		return WindowLayout{Tiled: node, Floating: nil, Recent: "", Last: ""}, nil
	}

	tiled, present := withoutPanes(node, floating)
	if !present {
		return WindowLayout{}, model.ErrInvalid
	}

	return WindowLayout{Tiled: tiled, Floating: floating, Recent: "", Last: ""}, nil
}

func EncodeLayout(node model.Layout, panes map[string]gotmux.PaneID) (gotmux.Layout, error) {
	var body strings.Builder
	if err := appendLayout(&body, node, panes, 0); err != nil {
		return "", err
	}

	return gotmux.Layout(layoutChecksum(body.String()) + "," + body.String()), nil
}

func (r *layoutReader) number() (int, error) {
	start := r.position
	for r.position < len(r.text) && r.text[r.position] >= '0' && r.text[r.position] <= '9' {
		r.position++
	}

	if start == r.position {
		return 0, model.ErrInvalid
	}

	value, err := strconv.Atoi(r.text[start:r.position])
	if err != nil || value < 0 {
		return 0, model.ErrInvalid
	}

	return value, nil
}

func (r *layoutReader) consume(char byte) bool {
	if r.position == len(r.text) || r.text[r.position] != char {
		return false
	}

	r.position++

	return true
}

func (r *layoutReader) node(depth int) (model.Layout, error) {
	var node model.Layout
	if depth > maxLayoutDepth {
		return node, model.ErrInvalid
	}

	fields := []*int{&node.Width, &node.Height, &node.Left, &node.Top}
	separators := []byte{'x', ',', ','}

	for index, field := range fields {
		value, err := r.number()
		if err != nil {
			return node, err
		}

		*field = value

		if index < len(separators) && !r.consume(separators[index]) {
			return node, model.ErrInvalid
		}
	}

	if r.consume(',') {
		id, err := r.number()
		if err != nil {
			return node, err
		}

		node.Pane = r.panes["%"+strconv.Itoa(id)]
		if node.Pane == "" {
			return node, model.ErrInvalid
		}

		return node, nil
	}

	return r.branch(node, depth)
}

func (r *layoutReader) branch(node model.Layout, depth int) (model.Layout, error) {
	closeChar := byte('}')
	node.Axis = "x"

	if !r.consume('{') {
		if !r.consume('[') {
			return node, model.ErrInvalid
		}

		closeChar, node.Axis = ']', "y"
	}

	for {
		child, err := r.node(depth + 1)
		if err != nil {
			return node, err
		}

		node.Children = append(node.Children, child)
		if r.consume(closeChar) {
			return node, nil
		}

		if !r.consume(',') {
			return node, model.ErrInvalid
		}
	}
}

func (r *layoutReader) floating() ([]string, error) {
	// tmux lists floating panes in stacking order in an optional "<cell,...>" suffix; the same
	// cells also appear inside the tiled tree.
	if !r.consume('<') {
		return nil, nil
	}

	var panes []string

	for {
		cell, err := r.node(0)
		if err != nil || cell.Pane == "" {
			return nil, model.ErrInvalid
		}

		panes = append(panes, cell.Pane)

		if r.consume('>') {
			return panes, nil
		}

		if !r.consume(',') {
			return nil, model.ErrInvalid
		}
	}
}

func appendLayout(out *strings.Builder, node model.Layout, panes map[string]gotmux.PaneID, depth int) error {
	if depth > maxLayoutDepth {
		return model.ErrInvalid
	}

	fmt.Fprintf(out, "%dx%d,%d,%d", node.Width, node.Height, node.Left, node.Top)

	if node.Pane != "" {
		id, ok := strings.CutPrefix(string(panes[node.Pane]), "%")
		if !ok {
			return model.ErrInvalid
		}

		if _, err := strconv.ParseUint(id, 10, 32); err != nil {
			return model.ErrInvalid
		}

		out.WriteByte(',')
		out.WriteString(id)

		return nil
	}

	openChar, closeChar := byte('{'), byte('}')
	if node.Axis == "y" {
		openChar, closeChar = '[', ']'
	} else if node.Axis != "x" {
		return model.ErrInvalid
	}

	out.WriteByte(openChar)

	for index, child := range node.Children {
		if index > 0 {
			out.WriteByte(',')
		}

		if err := appendLayout(out, child, panes, depth+1); err != nil {
			return err
		}
	}

	out.WriteByte(closeChar)

	return nil
}

func withoutPanes(node model.Layout, removed []string) (model.Layout, bool) {
	if node.Pane != "" {
		return node, !slices.Contains(removed, node.Pane)
	}

	children := make([]model.Layout, 0, len(node.Children))

	for _, child := range node.Children {
		if kept, present := withoutPanes(child, removed); present {
			children = append(children, kept)
		}
	}

	switch len(children) {
	case 0:
		var none model.Layout

		return none, false
	case 1:
		return children[0], true
	default:
		node.Children = children

		return node, true
	}
}

func layoutChecksum(value string) string {
	var checksum uint16
	for index := range len(value) {
		checksum = bits.RotateLeft16(checksum, -1)
		checksum += uint16(value[index])
	}

	return fmt.Sprintf("%04x", checksum)
}

func decodeTypedLayout(text string, panes map[string]string) (WindowLayout, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		return WindowLayout{}, model.ErrInvalid
	}

	version, ok := typedInt(document, "V")
	if !ok || version != typedLayoutVersion {
		return WindowLayout{}, model.ErrInvalid
	}

	root, ok := document["L"]
	if !ok {
		return WindowLayout{}, model.ErrInvalid
	}

	decoder := typedDecoder{panes: panes, floating: nil, recent: "", recentRank: 0, last: "", lastRank: 0}

	tiled, present, err := decoder.node(root, 0)
	if err != nil {
		return WindowLayout{}, err
	}

	if !present {
		return WindowLayout{}, model.ErrInvalid
	}

	return WindowLayout{Tiled: tiled, Floating: decoder.floating, Recent: decoder.recent, Last: decoder.last}, nil
}

func (d *typedDecoder) node(raw json.RawMessage, depth int) (model.Layout, bool, error) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return model.Layout{}, false, model.ErrInvalid
	}

	kind, ok := typedString(node, "t")
	if !ok {
		return model.Layout{}, false, model.ErrInvalid
	}

	geometry, err := decodeTypedGeometry(node, depth)
	if err != nil {
		return model.Layout{}, false, err
	}

	switch kind {
	case "h", "v":
		geometry.Axis = "x"
		if kind == "v" {
			geometry.Axis = "y"
		}

		return d.branch(node, geometry, depth)
	case "p":
		return d.leaf(node, geometry)
	default:
		return model.Layout{}, false, model.ErrInvalid
	}
}

func decodeTypedGeometry(node map[string]json.RawMessage, depth int) (model.Layout, error) {
	width, okWidth := typedInt(node, "w")
	height, okHeight := typedInt(node, "h")
	left, okLeft := typedInt(node, "x")
	top, okTop := typedInt(node, "y")

	if depth > maxLayoutDepth || !okWidth || !okHeight || !okLeft || !okTop {
		return model.Layout{}, model.ErrInvalid
	}

	if width < 0 || height < 0 || left < 0 || top < 0 {
		return model.Layout{}, model.ErrInvalid
	}

	return model.Layout{
		Pane:     "",
		Axis:     "",
		Children: nil,
		Width:    width,
		Height:   height,
		Left:     left,
		Top:      top,
	}, nil
}

func (d *typedDecoder) branch(node map[string]json.RawMessage, result model.Layout, depth int) (model.Layout, bool, error) {
	var children []json.RawMessage
	if err := json.Unmarshal(node["c"], &children); err != nil || len(children) < minBranchChildren {
		return model.Layout{}, false, model.ErrInvalid
	}

	for _, child := range children {
		decoded, present, err := d.node(child, depth+1)
		if err != nil {
			return model.Layout{}, false, err
		}

		if present {
			result.Children = append(result.Children, decoded)
		}
	}

	switch len(result.Children) {
	case 0:
		var none model.Layout

		return none, false, nil
	case 1:
		return result.Children[0], true, nil
	default:
		return result, true, nil
	}
}

func (d *typedDecoder) leaf(node map[string]json.RawMessage, result model.Layout) (model.Layout, bool, error) {
	if len(node["c"]) != 0 {
		return model.Layout{}, false, model.ErrInvalid
	}

	paneID, ok := typedString(node, "I")
	if !ok {
		return model.Layout{}, false, model.ErrInvalid
	}

	reference := d.panes[paneID]
	if reference == "" {
		return model.Layout{}, false, model.ErrInvalid
	}

	if _, floating := node["z"]; floating {
		d.floating = append(d.floating, reference)

		var none model.Layout

		return none, false, nil
	}

	d.noteUse(node, reference)

	result.Pane = reference

	return result, true, nil
}

func (d *typedDecoder) noteUse(node map[string]json.RawMessage, reference string) {
	rank, used := typedInt(node, "l")

	var active bool
	if raw, ok := node["a"]; ok && json.Unmarshal(raw, &active) == nil && active {
		rank, used = activeRank, true
	}

	if !used {
		return
	}

	switch {
	case d.recent == "" || rank < d.recentRank:
		d.last, d.lastRank = d.recent, d.recentRank
		d.recent, d.recentRank = reference, rank
	case d.last == "" || rank < d.lastRank:
		d.last, d.lastRank = reference, rank
	}
}

func typedInt(node map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := node[key]
	if !ok {
		return 0, false
	}

	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}

	return value, true
}

func typedString(node map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := node[key]
	if !ok {
		return "", false
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}

	return value, true
}
