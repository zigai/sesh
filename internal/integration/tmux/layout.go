package tmux

import (
	"encoding/json"
	"fmt"
	"math/bits"
	"strconv"
	"strings"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	maxLayoutDepth     = 64
	typedLayoutVersion = 2
	minBranchChildren  = 2
)

type layoutReader struct {
	text     string
	position int
	panes    map[string]string
}

func DecodeLayout(value gotmux.Layout, panes map[string]string) (model.Layout, error) {
	text := string(value)
	if strings.HasPrefix(strings.TrimSpace(text), "{") {
		return decodeTypedLayout(text, panes)
	}

	checksum, body, ok := strings.Cut(text, ",")
	if !ok || checksum != layoutChecksum(body) {
		return model.Layout{}, model.ErrInvalid
	}

	reader := layoutReader{text: body, position: 0, panes: panes}

	node, err := reader.node(0)
	if err != nil || reader.position != len(body) {
		return model.Layout{}, model.ErrInvalid
	}

	return node, nil
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

func layoutChecksum(value string) string {
	var checksum uint16
	for index := range len(value) {
		checksum = bits.RotateLeft16(checksum, -1)
		checksum += uint16(value[index])
	}

	return fmt.Sprintf("%04x", checksum)
}

func decodeTypedLayout(text string, panes map[string]string) (model.Layout, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		return model.Layout{}, model.ErrInvalid
	}

	version, ok := typedInt(document, "V")
	if !ok || version != typedLayoutVersion {
		return model.Layout{}, model.ErrInvalid
	}

	root, ok := document["L"]
	if !ok {
		return model.Layout{}, model.ErrInvalid
	}

	return decodeTypedNode(root, panes, 0)
}

func decodeTypedNode(raw json.RawMessage, panes map[string]string, depth int) (model.Layout, error) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return model.Layout{}, model.ErrInvalid
	}

	kind, ok := typedString(node, "t")
	if !ok {
		return model.Layout{}, model.ErrInvalid
	}

	geometry, err := decodeTypedGeometry(node, depth)
	if err != nil {
		return model.Layout{}, err
	}

	switch kind {
	case "h", "v":
		geometry.Axis = "x"
		if kind == "v" {
			geometry.Axis = "y"
		}

		return decodeTypedBranch(node, geometry, panes, depth)
	case "p":
		return decodeTypedLeaf(node, geometry, panes)
	default:
		return model.Layout{}, model.ErrInvalid
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

func decodeTypedBranch(node map[string]json.RawMessage, result model.Layout, panes map[string]string, depth int) (model.Layout, error) {
	var children []json.RawMessage
	if err := json.Unmarshal(node["c"], &children); err != nil || len(children) < minBranchChildren {
		return model.Layout{}, model.ErrInvalid
	}

	for _, child := range children {
		decoded, err := decodeTypedNode(child, panes, depth+1)
		if err != nil {
			return model.Layout{}, err
		}

		result.Children = append(result.Children, decoded)
	}

	return result, nil
}

func decodeTypedLeaf(node map[string]json.RawMessage, result model.Layout, panes map[string]string) (model.Layout, error) {
	if len(node["c"]) != 0 {
		return model.Layout{}, model.ErrInvalid
	}

	paneID, ok := typedString(node, "I")
	if !ok {
		return model.Layout{}, model.ErrInvalid
	}

	reference := panes[paneID]
	if reference == "" {
		return model.Layout{}, model.ErrInvalid
	}

	result.Pane = reference

	return result, nil
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
