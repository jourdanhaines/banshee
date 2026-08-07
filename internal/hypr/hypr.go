// Package hypr is banshee's minimal Hyprland IPC client: enough to focus the
// terminal window that holds a given process, nothing more.
//
// Everything here is best-effort by design. Banshee works on any wlroots
// compositor; only the "raise the terminal we just switched a tmux client in"
// nicety is Hyprland-specific, so a missing hyprctl or a non-Hyprland session
// degrades to doing nothing rather than failing the action.
//
// The exec and /proc reads sit behind function fields so the pid-ancestry and
// JSON-parsing logic is unit testable without a compositor.
package hypr

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
)

// maxAncestryHops bounds the /proc parent walk; a real terminal is a handful
// of hops away (tmux client → shell → terminal), 32 is paranoia.
const maxAncestryHops = 32

// Ctl talks to Hyprland through hyprctl. The zero value is production-ready;
// tests override the function fields.
type Ctl struct {
	// Run executes hyprctl with args and returns stdout. nil execs hyprctl.
	Run func(args ...string) (string, error)
	// ReadStatus returns /proc/<pid>/status. nil reads the real procfs.
	ReadStatus func(pid int) ([]byte, error)
	// Getenv looks up environment variables. nil uses os.Getenv.
	Getenv func(key string) string
}

// Available reports whether Hyprland IPC is worth attempting: banshee runs
// inside a Hyprland session and hyprctl is on PATH.
func (c *Ctl) Available() bool {
	if c.getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return false
	}
	if c.Run != nil {
		return true // test seam supplies its own transport
	}
	_, err := exec.LookPath("hyprctl")
	return err == nil
}

// FocusTerminalOf focuses the Hyprland window that (transitively) contains
// pid — for banshee, the terminal window a tmux client process runs inside.
// The window is found by walking pid's /proc ancestry and matching each
// ancestor against `hyprctl -j clients`; focuswindow by address also switches
// to the window's workspace, which is the point.
//
// Not running under Hyprland is a silent no-op, not an error: callers treat
// focusing as a nicety on top of an already-successful switch-client.
func (c *Ctl) FocusTerminalOf(pid int) error {
	if !c.Available() {
		return nil
	}
	out, err := c.run("-j", "clients")
	if err != nil {
		return fmt.Errorf("hyprctl clients: %w", err)
	}
	windows, err := parseClientAddresses(out)
	if err != nil {
		return fmt.Errorf("hyprctl clients: %w", err)
	}
	for _, p := range ancestors(pid, c.readStatus) {
		if addr, ok := windows[p]; ok {
			if _, err := c.run("dispatch", "focuswindow", "address:"+addr); err != nil {
				return fmt.Errorf("hyprctl focuswindow: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("no Hyprland window in the ancestry of pid %d", pid)
}

// parseClientAddresses maps window-owning pids to window addresses from
// `hyprctl -j clients` output.
func parseClientAddresses(jsonOut string) (map[int]string, error) {
	var clients []struct {
		Address string `json:"address"`
		Pid     int    `json:"pid"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &clients); err != nil {
		return nil, err
	}
	out := make(map[int]string, len(clients))
	for _, cl := range clients {
		if cl.Pid > 0 && cl.Address != "" {
			out[cl.Pid] = cl.Address
		}
	}
	return out, nil
}

var ppidRe = regexp.MustCompile(`(?m)^PPid:\s*(\d+)`)

// ancestors returns pid followed by its parents up to (excluding) pid 1,
// bounded by maxAncestryHops. A pid whose status cannot be read ends the walk.
func ancestors(pid int, readStatus func(int) ([]byte, error)) []int {
	var out []int
	for hops := 0; pid > 1 && hops < maxAncestryHops; hops++ {
		out = append(out, pid)
		status, err := readStatus(pid)
		if err != nil {
			break
		}
		m := ppidRe.FindSubmatch(status)
		if m == nil {
			break
		}
		next, err := strconv.Atoi(string(m[1]))
		if err != nil || next == pid {
			break
		}
		pid = next
	}
	return out
}

func (c *Ctl) run(args ...string) (string, error) {
	if c.Run != nil {
		return c.Run(args...)
	}
	out, err := exec.Command("hyprctl", args...).Output()
	return string(out), err
}

func (c *Ctl) readStatus(pid int) ([]byte, error) {
	if c.ReadStatus != nil {
		return c.ReadStatus(pid)
	}
	return os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
}

func (c *Ctl) getenv(key string) string {
	if c.Getenv != nil {
		return c.Getenv(key)
	}
	return os.Getenv(key)
}
