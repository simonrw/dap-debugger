package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/simonrw/dap-debugger/internal/launch"
)

// taskRun runs the commands of a preLaunchTask or postDebugTask in order.
// Background commands keep running after the run is done; their output keeps
// arriving until they exit.
type taskRun struct {
	events chan taskEvent
	cancel context.CancelFunc
	// background labels the background commands this run started.
	background []string
}

type taskEvent struct {
	text  string
	done  bool
	ended bool
	err   error
}

type taskOutputMsg struct {
	run  *taskRun
	text string
}

type taskDoneMsg struct {
	run *taskRun
	err error
}

type taskEndedMsg struct{ run *taskRun }

// taskOutput forwards command output as task events and watches for a
// background command's ready pattern.
type taskOutput struct {
	events  chan taskEvent
	ready   *regexp.Regexp
	readyCh chan struct{}
	once    sync.Once
	mu      sync.Mutex
	line    []byte
}

func (w *taskOutput) Write(p []byte) (int, error) {
	w.events <- taskEvent{text: string(p)}
	if w.ready == nil {
		return len(p), nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.line = append(w.line, p...)
	for {
		i := bytes.IndexByte(w.line, '\n')
		line := w.line
		if i >= 0 {
			line = w.line[:i]
		}
		if w.ready.Match(bytes.TrimRight(line, "\r")) {
			w.once.Do(func() { close(w.readyCh) })
		}
		if i < 0 {
			break
		}
		w.line = w.line[i+1:]
	}
	return len(p), nil
}

func startTasks(cmds []launch.TaskCommand) *taskRun {
	ctx, cancel := context.WithCancel(context.Background())
	run := &taskRun{events: make(chan taskEvent, 256), cancel: cancel}
	for _, c := range cmds {
		if c.Ready != nil {
			run.background = append(run.background, c.Label)
		}
	}

	go func() {
		var background sync.WaitGroup
		finish := func(err error) {
			run.events <- taskEvent{done: true, err: err}
			background.Wait()
			run.events <- taskEvent{ended: true}
		}

		for _, c := range cmds {
			run.events <- taskEvent{text: fmt.Sprintf("> Executing task: %s\n", c.Label)}
			cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
			cmd.Dir = c.Dir
			cmd.Env = append(os.Environ(), c.Env...)
			ownGroup(cmd)
			out := &taskOutput{events: run.events, ready: c.Ready, readyCh: make(chan struct{})}
			cmd.Stdout, cmd.Stderr = out, out

			if c.Ready == nil {
				if err := cmd.Run(); err != nil {
					finish(fmt.Errorf("task %q: %w", c.Label, err))
					return
				}
				continue
			}

			if err := cmd.Start(); err != nil {
				finish(fmt.Errorf("task %q: %w", c.Label, err))
				return
			}
			exited := make(chan error, 1)
			background.Add(1)
			go func() {
				defer background.Done()
				exited <- cmd.Wait()
			}()
			select {
			case <-out.readyCh:
			case err := <-exited:
				finish(fmt.Errorf("background task %q exited before it was ready: %v", c.Label, err))
				return
			}
		}
		finish(nil)
	}()
	return run
}

func waitTask(run *taskRun) tea.Cmd {
	return func() tea.Msg {
		e := <-run.events
		switch {
		case e.ended:
			return taskEndedMsg{run: run}
		case e.done:
			return taskDoneMsg{run: run, err: e.err}
		}
		return taskOutputMsg{run: run, text: e.text}
	}
}

func (m *Model) tasksPath() string {
	return filepath.Join(filepath.Dir(m.file.Path), "tasks.json")
}

// runTask resolves a task label and starts running it. Background tasks
// that are already running are reused, as VS Code does.
func (m *Model) runTask(label string, vars launch.Vars) tea.Cmd {
	cmds, err := launch.ResolveTask(m.tasksPath(), label, vars)
	if err != nil {
		m.console.println("stderr", err.Error())
		return nil
	}
	cmds = slices.DeleteFunc(cmds, func(c launch.TaskCommand) bool {
		return c.Ready != nil && m.background[c.Label] != nil
	})

	m.task = startTasks(cmds)
	for _, l := range m.task.background {
		m.background[l] = m.task
	}
	return waitTask(m.task)
}

// stopTasks stops running tasks, background ones included.
func (m *Model) stopTasks() {
	if m.task != nil {
		m.task.cancel()
	}
	for _, run := range m.background {
		run.cancel()
	}
}

func (m *Model) handleTaskMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case taskOutputMsg:
		m.console.write("stdout", msg.text)
		return waitTask(msg.run), true

	case taskEndedMsg:
		for label, run := range m.background {
			if run == msg.run {
				delete(m.background, label)
			}
		}
		return nil, true

	case taskDoneMsg:
		// Background commands keep producing output after the run is done.
		next := waitTask(msg.run)
		if msg.run != m.task {
			return next, true
		}
		m.task = nil
		plan := m.pending
		m.pending = nil
		if msg.err != nil {
			m.console.println("stderr", msg.err.Error())
			if plan != nil && m.sess == nil {
				m.status = statusInactive
			}
			m.compound, m.queue, m.noDebug = nil, nil, false
			return next, true
		}
		if plan != nil {
			return tea.Batch(next, m.launchPlan(plan)), true
		}
		if len(m.queue) > 0 {
			return tea.Batch(next, m.startNext()), true
		}
		return next, true
	}
	return nil, false
}
