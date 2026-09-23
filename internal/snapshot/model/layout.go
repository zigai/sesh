package model

const maxLayoutDepth = 64

func validateLayout(node Layout, depth int, leaves map[string]Layout) error {
	if depth > maxLayoutDepth || !validSize(node.Width, node.Height) || node.Left < 0 || node.Top < 0 {
		return ErrInvalid
	}

	if node.Pane != "" {
		return validateLeaf(node, leaves)
	}

	if len(node.Children) < 2 || (node.Axis != "x" && node.Axis != "y") {
		return ErrInvalid
	}

	if err := validatePartition(node); err != nil {
		return err
	}

	for _, child := range node.Children {
		if err := validateLayout(child, depth+1, leaves); err != nil {
			return err
		}
	}

	return nil
}

func validateLeaf(node Layout, leaves map[string]Layout) error {
	if !ValidID(node.Pane) || len(node.Children) != 0 || node.Axis != "" {
		return ErrInvalid
	}

	if _, exists := leaves[node.Pane]; exists {
		return ErrInvalid
	}

	leaves[node.Pane] = node

	return nil
}

func validatePartition(node Layout) error {
	position := node.Left

	end := node.Left + node.Width
	if node.Axis == "y" {
		position, end = node.Top, node.Top+node.Height
	}

	for _, child := range node.Children {
		if node.Axis == "x" {
			if child.Left != position || child.Top != node.Top || child.Height != node.Height {
				return ErrInvalid
			}

			position += child.Width + 1
		} else {
			if child.Top != position || child.Left != node.Left || child.Width != node.Width {
				return ErrInvalid
			}

			position += child.Height + 1
		}
	}

	if position-1 != end {
		return ErrInvalid
	}

	return nil
}
