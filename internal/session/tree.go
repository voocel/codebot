package session

import "fmt"

// BuildTree constructs the full session tree from entries.
// Returns the root node (header).
func BuildTree(entries []Entry) (*TreeNode, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("no entries in session")
	}

	// Build ID → node map.
	nodes := make(map[string]*TreeNode, len(entries))
	for _, e := range entries {
		nodes[e.ID] = &TreeNode{Entry: e}
	}

	// Link children to parents.
	var root *TreeNode
	for _, e := range entries {
		node := nodes[e.ID]
		if e.ParentID == "" {
			root = node
		} else if parent, ok := nodes[e.ParentID]; ok {
			parent.Children = append(parent.Children, node)
		}
	}

	if root == nil && len(entries) > 0 {
		root = nodes[entries[0].ID]
	}
	return root, nil
}

