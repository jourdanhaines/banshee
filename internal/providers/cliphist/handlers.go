package cliphist

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// Action kinds emitted by this provider. Both carry the entry ID as Argv[0] —
// a lookup key into the Store, never content, so clipboard payloads (which
// may be secrets) stay out of Action values entirely.
const (
	// ActClipCopy re-copies the entry to the system clipboard.
	ActClipCopy = "clip-copy"
	// ActClipDelete removes the entry from the history.
	ActClipDelete = "clip-delete"
)

// Deps are the handlers' collaborators, all injectable for tests (the totp
// Deps pattern).
type Deps struct {
	// Store is the shared history; required.
	Store *Store
	// CopyText puts text on the clipboard; sensitive re-copies carry the
	// password-manager hint so downstream managers (including banshee's own
	// watcher) mask the recapture. Defaults to the launch helpers.
	CopyText func(text string, sensitive bool) error
	// CopyStream puts a typed payload (image bytes, a uri-list) on the
	// clipboard. Defaults to launch.CopyToClipboardMIME.
	CopyStream func(mime string, r io.Reader, sensitive bool) error
	// OpenFile opens an image entry's runtime file for re-copy. Defaults to
	// os.Open.
	OpenFile func(path string) (io.ReadCloser, error)
	// Notify reports a failure after the window has hidden — a returned error
	// would go nowhere by then. Defaults to the log.
	Notify func(msg string)
	// Reopen shows the launcher again on the given query; the delete handler
	// uses it so the user sees the list without the removed row. Nil means
	// the window simply stays hidden after a delete.
	//
	// Called from an action handler on the GTK main loop; boot's
	// implementation hops through glib.IdleAdd, so this package never assumes
	// which thread it runs on.
	Reopen func(query string)
}

// withDefaults fills the nil collaborators. Reopen is deliberately not
// defaulted, exactly like totp's: there is no stand-in for "show the launcher
// again".
func (d Deps) withDefaults() Deps {
	if d.CopyText == nil {
		d.CopyText = func(string, bool) error { return errors.New("cliphist: no clipboard configured") }
	}
	if d.CopyStream == nil {
		d.CopyStream = func(string, io.Reader, bool) error { return errors.New("cliphist: no clipboard configured") }
	}
	if d.OpenFile == nil {
		d.OpenFile = func(path string) (io.ReadCloser, error) { return os.Open(path) }
	}
	if d.Notify == nil {
		d.Notify = func(msg string) { log.Printf("cliphist: %s", msg) }
	}
	return d
}

// RegisterHandlers binds the clip action kinds wired to the real clipboard.
func RegisterHandlers(d *launch.Dispatcher, opts launch.Options, store *Store) {
	RegisterHandlersWith(d, Deps{
		Store: store,
		CopyText: func(text string, sensitive bool) error {
			if sensitive {
				return launch.CopyToClipboardSensitive(opts, text)
			}
			return launch.CopyToClipboard(opts, text)
		},
		CopyStream: func(mime string, r io.Reader, sensitive bool) error {
			return launch.CopyToClipboardMIME(opts, mime, r, sensitive)
		},
	})
}

// RegisterHandlersWith is RegisterHandlers with injectable collaborators.
func RegisterHandlersWith(dispatch *launch.Dispatcher, deps Deps) {
	deps = deps.withDefaults()
	dispatch.Register(ActClipCopy, deps.handleCopy)
	dispatch.Register(ActClipDelete, deps.handleDelete)
}

// entryFromAction resolves an action's Argv[0] back to a live entry. Cheap
// and synchronous: these are the failures worth returning as dispatcher
// errors while the caller can still see them.
func (d Deps) entryFromAction(a providers.Action, kind string) (Entry, error) {
	if d.Store == nil {
		return Entry{}, fmt.Errorf("%s: no store wired", kind)
	}
	if len(a.Argv) == 0 {
		return Entry{}, fmt.Errorf("%s: missing entry id", kind)
	}
	id, err := strconv.ParseUint(a.Argv[0], 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("%s: bad entry id %q", kind, a.Argv[0])
	}
	e, ok := d.Store.Get(id)
	if !ok {
		return Entry{}, fmt.Errorf("%s: entry %d no longer in history", kind, id)
	}
	return e, nil
}

// handleCopy re-copies an entry. Validation is synchronous; the clipboard
// write is detached because action handlers run on the GTK main loop and the
// clipboard tool round trip must not stall it.
func (d Deps) handleCopy(a providers.Action) error {
	e, err := d.entryFromAction(a, "clip-copy")
	if err != nil {
		return err
	}
	go func() {
		var err error
		switch e.Kind {
		case KindImage:
			var f io.ReadCloser
			if f, err = d.OpenFile(e.ImagePath); err == nil {
				err = d.CopyStream(e.MIME, f, e.Sensitive)
				f.Close()
			}
		case KindFiles:
			err = d.CopyStream(e.MIME, strings.NewReader(e.Text), e.Sensitive)
		default:
			err = d.CopyText(e.Text, e.Sensitive)
		}
		if err != nil {
			// Errors name the entry, never its content.
			d.Notify(fmt.Sprintf("could not copy entry %d: %v", e.ID, err))
		}
	}()
	return nil
}

// handleDelete drops an entry, then re-shows the history so the user sees it
// gone. Deleting is memory plus a tmpfs unlink — fast enough to stay on the
// main loop.
func (d Deps) handleDelete(a providers.Action) error {
	e, err := d.entryFromAction(a, "clip-delete")
	if err != nil {
		return err
	}
	d.Store.Delete(e.ID)
	if d.Reopen != nil {
		d.Reopen("clip")
	}
	return nil
}
