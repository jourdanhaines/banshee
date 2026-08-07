package launch

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ClipboardCandidates are probed, in order, when resolving the clipboard
// tool. wl-copy is only considered when $WAYLAND_DISPLAY is set.
var ClipboardCandidates = []struct {
	// Bin is the binary probed on PATH.
	Bin string
	// Argv is the full command line; text is written to its stdin.
	Argv []string
}{
	{"wl-copy", []string{"wl-copy"}},
	{"xclip", []string{"xclip", "-selection", "clipboard"}},
	{"xsel", []string{"xsel", "--clipboard", "--input"}},
}

// clipboardTimeout bounds how long a copy may take. The handler runs on the
// GTK main loop, so a clipboard tool that blocks (instead of forking like
// wl-copy/xclip do) must not hang the daemon.
const clipboardTimeout = 2 * time.Second

// ResolveClipboard picks the clipboard tool command line: the first of
// ClipboardCandidates found on PATH, skipping wl-copy outside Wayland.
func ResolveClipboard(opts Options) ([]string, error) {
	tried := make([]string, 0, len(ClipboardCandidates))
	for _, cand := range ClipboardCandidates {
		if cand.Bin == "wl-copy" && opts.getenv("WAYLAND_DISPLAY") == "" {
			continue
		}
		tried = append(tried, cand.Bin)
		if _, err := opts.lookPath(cand.Bin); err == nil {
			return cand.Argv, nil
		}
	}
	return nil, fmt.Errorf("no clipboard tool found (install wl-clipboard, xclip or xsel; tried %v)", tried)
}

// CopyToClipboard writes text to the resolved clipboard tool's stdin. The
// text never appears in the tool's argv, so it is not visible in the process
// list.
func CopyToClipboard(opts Options, text string) error {
	argv, err := ResolveClipboard(opts)
	if err != nil {
		return err
	}
	if err := opts.runStdin(argv, strings.NewReader(text)); err != nil {
		return fmt.Errorf("clipboard: %s: %w", argv[0], err)
	}
	return nil
}

// runStdinCmd runs argv to completion with stdin fed from r, bounded by
// clipboardTimeout.
func runStdinCmd(argv []string, stdin io.Reader) error {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	return cmd.Run()
}
