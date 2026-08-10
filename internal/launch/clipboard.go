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

// CopyToClipboardSensitive is CopyToClipboard for secret material (TOTP codes,
// re-copied masked history entries). When the resolved tool is wl-copy it adds
// --sensitive, which offers the x-kde-passwordManagerHint type alongside the
// text so clipboard managers (including banshee's own history watcher) mask or
// skip the capture. xclip/xsel have no equivalent, so there the call degrades
// to a plain copy — banshee's watcher only runs under Wayland anyway.
func CopyToClipboardSensitive(opts Options, text string) error {
	argv, err := ResolveClipboard(opts)
	if err != nil {
		return err
	}
	if argv[0] == "wl-copy" {
		argv = append(argv[:len(argv):len(argv)], "--sensitive")
	}
	if err := opts.runStdin(argv, strings.NewReader(text)); err != nil {
		return fmt.Errorf("clipboard: %s: %w", argv[0], err)
	}
	return nil
}

// CopyToClipboardMIME writes r to the clipboard under an explicit MIME type —
// the re-copy path for clipboard-history images and text/uri-list file lists.
// Only wl-copy (-t/--sensitive) and xclip (-t) can offer a declared type; xsel
// cannot, and returns an error naming the tool rather than silently pasting
// image bytes as text. The payload rides on stdin, never argv.
func CopyToClipboardMIME(opts Options, mime string, r io.Reader, sensitive bool) error {
	argv, err := ResolveClipboard(opts)
	if err != nil {
		return err
	}
	switch argv[0] {
	case "wl-copy":
		argv = append(argv[:len(argv):len(argv)], "--type", mime)
		if sensitive {
			argv = append(argv, "--sensitive")
		}
	case "xclip":
		argv = append(argv[:len(argv):len(argv)], "-t", mime)
	default:
		return fmt.Errorf("clipboard: %s cannot offer MIME type %s", argv[0], mime)
	}
	if err := opts.runStdin(argv, r); err != nil {
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
