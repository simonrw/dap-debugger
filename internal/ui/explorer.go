package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var hiddenNames = []string{".git", ".DS_Store", "__pycache__", ".jj"}

type fileNode struct {
	name     string
	path     string
	dir      bool
	depth    int
	expanded bool
	loaded   bool
	children []*fileNode
}

// explorer is a lazily loaded tree of the workspace folder.
type explorer struct {
	root *fileNode
	rows []*fileNode
	list list
}

func newExplorer(root string) explorer {
	e := explorer{}
	e.setRoot(root)
	return e
}

// setRoot shows the tree from another folder, keeping the selected entry
// selected when it is inside that folder.
func (e *explorer) setRoot(path string) {
	from := e.selected()
	e.root = &fileNode{name: filepath.Base(path), path: path, dir: true, depth: -1}
	e.list = list{}
	e.expand(e.root)
	if from != nil {
		e.reveal(from.path, 0)
	}
}

func (e *explorer) expand(n *fileNode) {
	if !n.dir {
		return
	}
	if !n.loaded {
		n.children = readDir(n.path, n.depth+1)
		n.loaded = true
	}
	n.expanded = true
	e.flatten()
}

func (e *explorer) collapse(n *fileNode) {
	n.expanded = false
	e.flatten()
}

func readDir(path string, depth int) []*fileNode {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}

	nodes := make([]*fileNode, 0, len(entries))
	for _, entry := range entries {
		if slices.Contains(hiddenNames, entry.Name()) {
			continue
		}
		full := filepath.Join(path, entry.Name())
		dir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(full); err == nil {
				dir = info.IsDir()
			}
		}
		nodes = append(nodes, &fileNode{name: entry.Name(), path: full, dir: dir, depth: depth})
	}

	slices.SortFunc(nodes, func(a, b *fileNode) int {
		if a.dir != b.dir {
			if a.dir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
	})
	return nodes
}

func (e *explorer) flatten() {
	e.rows = e.rows[:0]
	var walk func(n *fileNode)
	walk = func(n *fileNode) {
		for _, c := range n.children {
			e.rows = append(e.rows, c)
			if c.dir && c.expanded {
				walk(c)
			}
		}
	}
	walk(e.root)
}

func (e *explorer) selected() *fileNode {
	if e.list.cursor < 0 || e.list.cursor >= len(e.rows) {
		return nil
	}
	return e.rows[e.list.cursor]
}

// reveal expands the folders leading to path and selects it.
func (e *explorer) reveal(path string, height int) {
	rel, err := filepath.Rel(e.root.path, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return
	}

	node := e.root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		e.expand(node)
		var next *fileNode
		for _, c := range node.children {
			if c.name == part {
				next = c
				break
			}
		}
		if next == nil {
			return
		}
		node = next
	}

	if i := slices.Index(e.rows, node); i >= 0 {
		e.list.cursor = i
		if height > 0 {
			e.list.clamp(len(e.rows), height)
		}
	}
}

// parent returns the row index of a node's parent folder, or -1.
func (e *explorer) parent(n *fileNode) int {
	for i := slices.Index(e.rows, n) - 1; i >= 0; i-- {
		if e.rows[i].depth < n.depth {
			return i
		}
	}
	return -1
}

func (e *explorer) refresh() {
	var reload func(n *fileNode)
	reload = func(n *fileNode) {
		if !n.loaded {
			return
		}
		old := map[string]*fileNode{}
		for _, c := range n.children {
			old[c.path] = c
		}
		n.children = readDir(n.path, n.depth+1)
		for i, c := range n.children {
			if prev, ok := old[c.path]; ok && prev.dir == c.dir {
				n.children[i] = prev
				reload(prev)
			}
		}
	}
	reload(e.root)
	e.flatten()
}

func (e *explorer) renderRows(st styles, bpFiles map[string]bool) []string {
	rows := make([]string, len(e.rows))
	for i, n := range e.rows {
		var icon, name string
		switch {
		case n.dir && n.expanded:
			icon, name = "▾ ", st.dir.Render(n.name)
		case n.dir:
			icon, name = "▸ ", st.dir.Render(n.name)
		default:
			icon, name = "  ", st.base.Render(n.name)
		}
		row := " " + indent(n.depth) + st.dim.Render(icon) + name
		if bpFiles[n.path] {
			row += " " + st.breakpoint.Render("●")
		}
		rows[i] = row
	}
	return rows
}
