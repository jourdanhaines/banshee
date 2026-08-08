package hypr

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

const clientsJSON = `[
  {"address": "0x55d2a0", "pid": 4200, "class": "com.mitchellh.ghostty"},
  {"address": "0x55d2b0", "pid": 4300, "class": "firefox"},
  {"address": "", "pid": 4400},
  {"address": "0x55d2c0", "pid": 0}
]`

func TestParseClientAddresses(t *testing.T) {
	got, err := parseClientAddresses(clientsJSON)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{4200: "0x55d2a0", 4300: "0x55d2b0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed %v, want %v", got, want)
	}
	if _, err := parseClientAddresses("not json"); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// fakeTree maps pid → ppid for the ancestry walk.
func fakeTree(tree map[int]int) func(int) ([]byte, error) {
	return func(pid int) ([]byte, error) {
		ppid, ok := tree[pid]
		if !ok {
			return nil, errors.New("no such process")
		}
		return []byte(fmt.Sprintf("Name:\tx\nPid:\t%d\nPPid:\t%d\n", pid, ppid)), nil
	}
}

func TestAncestors(t *testing.T) {
	// tmux client 5000 → shell 4500 → terminal 4200 → init 1.
	tree := map[int]int{5000: 4500, 4500: 4200, 4200: 1}
	got := ancestors(5000, fakeTree(tree))
	if want := []int{5000, 4500, 4200}; !reflect.DeepEqual(got, want) {
		t.Errorf("ancestors = %v, want %v", got, want)
	}
}

func TestAncestorsUnreadableStops(t *testing.T) {
	got := ancestors(5000, fakeTree(map[int]int{}))
	if want := []int{5000}; !reflect.DeepEqual(got, want) {
		t.Errorf("ancestors = %v, want %v", got, want)
	}
}

func TestAncestorsSelfParentStops(t *testing.T) {
	got := ancestors(7, fakeTree(map[int]int{7: 7}))
	if want := []int{7}; !reflect.DeepEqual(got, want) {
		t.Errorf("ancestors = %v, want %v", got, want)
	}
}

func TestFocusTerminalOf(t *testing.T) {
	var dispatched []string
	c := &Ctl{
		Getenv: func(string) string { return "sig" },
		Run: func(args ...string) (string, error) {
			if args[0] == "-j" {
				return clientsJSON, nil
			}
			dispatched = args
			return "ok", nil
		},
		ReadStatus: fakeTree(map[int]int{5000: 4500, 4500: 4200, 4200: 1}),
	}
	if err := c.FocusTerminalOf(5000); err != nil {
		t.Fatal(err)
	}
	want := []string{"dispatch", "focuswindow", "address:0x55d2a0"}
	if !reflect.DeepEqual(dispatched, want) {
		t.Errorf("dispatched %v, want %v", dispatched, want)
	}
}

func TestFocusTerminalOfNoWindow(t *testing.T) {
	c := &Ctl{
		Getenv:     func(string) string { return "sig" },
		Run:        func(args ...string) (string, error) { return clientsJSON, nil },
		ReadStatus: fakeTree(map[int]int{9000: 1}),
	}
	if err := c.FocusTerminalOf(9000); err == nil {
		t.Error("expected error when no ancestor owns a window")
	}
}

func TestFocusTerminalOfOutsideHyprlandIsNoop(t *testing.T) {
	c := &Ctl{
		Getenv: func(string) string { return "" },
		Run: func(args ...string) (string, error) {
			t.Error("must not shell out outside Hyprland")
			return "", nil
		},
	}
	if err := c.FocusTerminalOf(5000); err != nil {
		t.Errorf("expected silent no-op, got %v", err)
	}
}
