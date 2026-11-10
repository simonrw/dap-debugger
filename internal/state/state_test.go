package state

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestBreakpointsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")

	s, err := Open(path, "/ws")
	if err != nil {
		t.Fatal(err)
	}
	s.Toggle("/ws/b.go", 20)
	s.Toggle("/ws/a.go", 5)
	s.Toggle("/ws/b.go", 3)
	s.Breakpoints[s.Find("/ws/b.go", 3)].Condition = "x > 1"
	s.Watch = []string{"x"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path, "/ws")
	if err != nil {
		t.Fatal(err)
	}
	want := []Breakpoint{
		{Path: "/ws/a.go", Line: 5, Enabled: true},
		{Path: "/ws/b.go", Line: 3, Enabled: true, Condition: "x > 1"},
		{Path: "/ws/b.go", Line: 20, Enabled: true},
	}
	if !reflect.DeepEqual(again.Breakpoints, want) {
		t.Fatalf("got %+v", again.Breakpoints)
	}
	if !reflect.DeepEqual(again.Watch, []string{"x"}) {
		t.Fatalf("watch %v", again.Watch)
	}

	if files := again.Files(); !reflect.DeepEqual(files, []string{"/ws/a.go", "/ws/b.go"}) {
		t.Fatalf("files %v", files)
	}

	again.Toggle("/ws/b.go", 20)
	if got := again.BreakpointsIn("/ws/b.go"); len(got) != 1 || got[0].Line != 3 {
		t.Fatalf("after removal %+v", got)
	}
}

func TestWorkspacesAreIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	a, _ := Open(path, "/a")
	b, _ := Open(path, "/b")
	a.Toggle("/a/x.py", 1)
	b.Toggle("/b/y.py", 2)
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}

	a, _ = Open(path, "/a")
	if len(a.Breakpoints) != 1 || a.Breakpoints[0].Path != "/a/x.py" {
		t.Fatalf("workspace a lost its breakpoints: %+v", a.Breakpoints)
	}
}
