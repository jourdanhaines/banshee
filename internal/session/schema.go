// Package session loads, validates and writes banshee's session and group
// JSON configs (~/.config/banshee/sessions/<target>.json and
// groups/<name>.json, both schema v1 — unchanged from banshee v0.3).
//
// schema.go is a frozen Phase-0 contract.
package session

import "encoding/json"

// SchemaVersion is the only accepted "v" value for both file kinds.
const SchemaVersion = 1

// Session is sessions/<target>.json. The tmux session name always derives
// from the filename; Name is informational only.
type Session struct {
	V       int      `json:"v"`
	Name    string   `json:"name"`
	Cwd     string   `json:"cwd,omitempty"`
	Windows []Window `json:"windows"`
}

// Window is one tmux window.
type Window struct {
	Name  string `json:"name,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
	Panes []Pane `json:"panes"`
}

// Pane is one element of a window's panes array: either a leaf pane
// ({run, cwd}) or a nested array representing a perpendicular sub-split.
// The JSON shape is heterogeneous, so Pane keeps the raw form and exposes
// it via Leaf/Split.
type Pane struct {
	raw json.RawMessage
}

// Leaf pane payload.
type Leaf struct {
	Run string `json:"run,omitempty"`
	Cwd string `json:"cwd,omitempty"`
}

func (p *Pane) UnmarshalJSON(b []byte) error {
	p.raw = append(p.raw[:0], b...)
	return nil
}

func (p Pane) MarshalJSON() ([]byte, error) {
	if p.raw == nil {
		return []byte("{}"), nil
	}
	return p.raw, nil
}

// IsSplit reports whether the element is a nested sub-split array.
func (p Pane) IsSplit() bool {
	for _, c := range p.raw {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

// Split returns the nested panes of a sub-split element.
func (p Pane) Split() ([]Pane, error) {
	var panes []Pane
	err := json.Unmarshal(p.raw, &panes)
	return panes, err
}

// Leaf returns the leaf payload of a non-split element.
func (p Pane) Leaf() (Leaf, error) {
	var l Leaf
	err := json.Unmarshal(p.raw, &l)
	return l, err
}

// Group is groups/<name>.json.
type Group struct {
	V       int      `json:"v"`
	Name    string   `json:"name"`
	Targets []string `json:"targets"`
}
