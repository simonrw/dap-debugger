package ui

import (
	tea "charm.land/bubbletea/v2"
	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/session"
)

// dataBreakpoint stops when a variable's value changes.
type dataBreakpoint struct {
	id          string
	description string
	accessType  godap.DataBreakpointAccessType
	enabled     bool
	verified    bool
}

type dataInfoMsg struct {
	sess *session.Session
	body godap.DataBreakpointInfoResponseBody
	err  error
}

type dataBreakpointsMsg struct {
	sess    *session.Session
	sent    []string
	records []godap.Breakpoint
	err     error
}

// addDataBreakpoint asks the adapter whether a variable can be watched for changes.
func (m *Model) addDataBreakpoint(n *varNode) tea.Cmd {
	if n == nil || n.parentRef == 0 || m.sess == nil || m.status != statusStopped {
		return nil
	}
	if !m.caps.SupportsDataBreakpoints {
		m.console.println("stderr", "This debug adapter does not support breaking on value changes")
		return nil
	}
	s, ref, name := m.sess, n.parentRef, n.name
	return func() tea.Msg {
		var body godap.DataBreakpointInfoResponseBody
		err := s.Request("dataBreakpointInfo", map[string]any{"variablesReference": ref, "name": name}, &body)
		return dataInfoMsg{sess: s, body: body, err: err}
	}
}

func (m *Model) handleDataMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case dataInfoMsg:
		if msg.sess != m.sess {
			return nil, true
		}
		id, _ := msg.body.DataId.(string)
		if msg.err != nil || id == "" {
			reason := msg.body.Description
			if msg.err != nil {
				reason = shortError(msg.err)
			}
			m.console.println("stderr", "Cannot break on value change: "+reason)
			return nil, true
		}
		access := godap.DataBreakpointAccessType("write")
		if len(msg.body.AccessTypes) > 0 {
			access = msg.body.AccessTypes[0]
			for _, a := range msg.body.AccessTypes {
				if a == "write" {
					access = a
				}
			}
		}
		m.dataBps = append(m.dataBps, dataBreakpoint{id: id, description: msg.body.Description, accessType: access, enabled: true})
		return m.sendDataBreakpoints(), true

	case dataBreakpointsMsg:
		if msg.sess != m.sess {
			return nil, true
		}
		if msg.err != nil {
			m.console.println("stderr", msg.err.Error())
			return nil, true
		}
		verified := map[string]bool{}
		for i, id := range msg.sent {
			if i < len(msg.records) {
				verified[id] = msg.records[i].Verified
				if !msg.records[i].Verified && msg.records[i].Message != "" {
					m.console.println("stderr", msg.records[i].Message)
				}
			}
		}
		for i := range m.dataBps {
			m.dataBps[i].verified = verified[m.dataBps[i].id]
		}
		return nil, true
	}
	return nil, false
}

func (m *Model) sendDataBreakpoints() tea.Cmd {
	if m.sess == nil || !m.configured {
		return nil
	}
	var sent []string
	bps := []godap.DataBreakpoint{}
	for _, d := range m.dataBps {
		if d.enabled {
			sent = append(sent, d.id)
			bps = append(bps, godap.DataBreakpoint{DataId: d.id, AccessType: d.accessType})
		}
	}
	s := m.sess
	return func() tea.Msg {
		var body godap.SetDataBreakpointsResponseBody
		err := s.Request("setDataBreakpoints", map[string]any{"breakpoints": bps}, &body)
		return dataBreakpointsMsg{sess: s, sent: sent, records: body.Breakpoints, err: err}
	}
}
