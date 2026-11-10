package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// varNode is a scope, variable or watch result in an expandable tree.
type varNode struct {
	key   string
	name  string
	value string
	typ   string
	ref   int
	// memRef is a memory reference for the variable's value, if any.
	memRef string
	// evalName is an expression for this variable, when the adapter gives one.
	evalName string
	// parentRef is the variablesReference of the container holding this variable.
	parentRef int
	depth     int
	expanded  bool
	loading   bool
	err       string
	children  []*varNode
	// watch marks a top-level watch expression row.
	watch bool
}

func (n *varNode) childKey(name string) string {
	return n.key + "\x00" + name
}

// flattenVars lists the visible nodes of a forest in display order.
func flattenVars(roots []*varNode) []*varNode {
	var out []*varNode
	var walk func(nodes []*varNode)
	walk = func(nodes []*varNode) {
		for _, n := range nodes {
			out = append(out, n)
			if n.expanded {
				walk(n.children)
			}
		}
	}
	walk(roots)
	return out
}

func renderVarRows(st styles, nodes []*varNode) []string {
	rows := make([]string, len(nodes))
	for i, n := range nodes {
		icon := "  "
		if n.ref > 0 {
			icon = "▸ "
			if n.expanded {
				icon = "▾ "
			}
		}

		var b strings.Builder
		b.WriteString(" " + indent(n.depth) + st.dim.Render(icon))

		switch {
		case n.depth == 0 && !n.watch:
			b.WriteString(st.bold.Render(n.name))
		default:
			b.WriteString(st.varName.Render(sanitize(n.name)))
		}

		switch {
		case n.err != "":
			b.WriteString(st.dim.Render(": ") + st.stderr.Render(sanitize(n.err)))
		case n.loading && n.value == "":
			b.WriteString(st.dim.Render(" …"))
		case n.value != "" || n.watch:
			b.WriteString(st.dim.Render(": ") + valueStyle(st, n.value).Render(sanitize(n.value)))
		}
		rows[i] = b.String()
	}
	return rows
}

func valueStyle(st styles, value string) lipgloss.Style {
	switch {
	case strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'"):
		return st.varString
	case value != "" && (value[0] >= '0' && value[0] <= '9' || value[0] == '-' && len(value) > 1):
		return st.varNumber
	case value == "true" || value == "false" || value == "True" || value == "False" || value == "nil" || value == "None":
		return st.varOther
	}
	return st.base
}
