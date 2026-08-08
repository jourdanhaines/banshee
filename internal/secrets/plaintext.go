package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jourdanhaines/banshee/internal/config"
)

// plaintextVersion is the schema version this build writes. A file carrying a
// higher version was written by a newer banshee, so this build reads what it
// can but refuses to write it back — rewriting would drop fields it does not
// understand and silently corrupt the newer install's secrets.
const plaintextVersion = 1

// plaintextFile is the on-disk shape of the plaintext store. Unknown keys are
// ignored on read, per the repo-wide forward-compatibility rule.
type plaintextFile struct {
	V int `json:"v"`
	// Secrets maps key ("totp/<name>") to the raw secret value.
	Secrets map[string]string `json:"secrets"`
	// raw is the file exactly as it was read, keyed by top-level JSON key. save
	// writes it back with only "v" and "secrets" replaced, so a key written by
	// a newer banshee survives a rewrite by this one. The version guard in
	// writable does not cover that case: an additive field keeps v: 1 by this
	// repo's convention, so it never trips the guard.
	raw map[string]json.RawMessage
}

// Plaintext keeps secrets in an unencrypted JSON file owned by the user. It
// exists because a Secret Service daemon is not universally present (headless
// boxes, minimal window managers) and refusing to work there would push users
// to keep their keys somewhere worse. The file is 0600 inside a 0700 directory,
// which is the whole of its protection — see the package threat model.
type Plaintext struct {
	// path is the secrets file. Empty means config.PlaintextSecretsPath(),
	// resolved lazily so tests can construct a Plaintext without a $HOME.
	path string
}

var _ Store = (*Plaintext)(nil)

// NewPlaintext returns a Plaintext backed by path. An empty path selects the
// standard location; tests pass a t.TempDir() file so they never touch the
// developer's real secrets.
func NewPlaintext(path string) *Plaintext { return &Plaintext{path: path} }

// Name identifies the backend for persistence in the TOTP index.
func (p *Plaintext) Name() string { return BackendPlaintext }

// AuthPerAccess is false: the file is readable by the process that owns it, so
// there is nothing to authenticate against.
func (p *Plaintext) AuthPerAccess() bool { return false }

// Blocking is false: every operation is a read or an atomic rewrite of one
// small local file, with nothing to prompt on and nobody to wait for.
func (p *Plaintext) Blocking() bool { return false }

func (p *Plaintext) filePath() string {
	if p.path != "" {
		return p.path
	}
	return config.PlaintextSecretsPath()
}

// Get returns the stored value, or ErrNotFound. cred is ignored.
func (p *Plaintext) Get(ctx context.Context, key string, _ Credential) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := p.load()
	if err != nil {
		return "", err
	}
	v, ok := f.Secrets[key]
	if !ok {
		return "", fmt.Errorf("plaintext: %q: %w", key, ErrNotFound)
	}
	return v, nil
}

// Set stores value under key, creating the file on first use.
func (p *Plaintext) Set(ctx context.Context, key, value string, _ Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := p.load()
	if err != nil {
		return err
	}
	if err := p.writable(f); err != nil {
		return err
	}
	if f.Secrets == nil {
		f.Secrets = make(map[string]string)
	}
	f.Secrets[key] = value
	return p.save(f)
}

// Delete removes key, reporting ErrNotFound when it was never there so a
// caller can tell a real removal from a no-op.
func (p *Plaintext) Delete(ctx context.Context, key string, _ Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := p.load()
	if err != nil {
		return err
	}
	if _, ok := f.Secrets[key]; !ok {
		return fmt.Errorf("plaintext: %q: %w", key, ErrNotFound)
	}
	if err := p.writable(f); err != nil {
		return err
	}
	delete(f.Secrets, key)
	return p.save(f)
}

// writable rejects a rewrite of a file from a newer schema. The check lives on
// the write path only: reading a v2 file loses nothing, writing one would.
func (p *Plaintext) writable(f *plaintextFile) error {
	if f.V > plaintextVersion {
		return fmt.Errorf("plaintext: %s is schema v%d, this banshee writes v%d (upgrade banshee; refusing to overwrite)",
			p.filePath(), f.V, plaintextVersion)
	}
	return nil
}

// load reads the secrets file. A missing file is an empty store rather than an
// error, so the first Set does not need a separate "create" step.
func (p *Plaintext) load() (*plaintextFile, error) {
	b, err := os.ReadFile(p.filePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &plaintextFile{
				V:       plaintextVersion,
				Secrets: map[string]string{},
				raw:     map[string]json.RawMessage{},
			}, nil
		}
		return nil, fmt.Errorf("plaintext: read %s: %w", p.filePath(), err)
	}
	var f plaintextFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("plaintext: parse %s: %w", p.filePath(), err)
	}
	// Read a second time into a raw map so save can put back the keys this
	// build has no field for; see plaintextFile.raw.
	f.raw = map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &f.raw); err != nil {
		return nil, fmt.Errorf("plaintext: parse %s: %w", p.filePath(), err)
	}
	if f.Secrets == nil {
		f.Secrets = map[string]string{}
	}
	return &f, nil
}

// save writes the file atomically: a 0600 temp file in the same directory,
// then a rename, so a crash mid-write can never leave a truncated secrets file
// (the state.Store.Record idiom, with modes tightened for secret material).
//
// It writes the raw map read by load with only the fields this build owns
// replaced, so the rewrite is a merge rather than a load-modify-save through
// plaintextFile — the forward-compatibility rule forbids the latter, which
// would drop every key this build has no field for (see plaintextFile.raw and
// connectors.SaveRepoBinding, the same shape).
func (p *Plaintext) save(f *plaintextFile) error {
	f.V = plaintextVersion
	out := f.raw
	if out == nil {
		out = map[string]json.RawMessage{}
	}
	v, err := json.Marshal(f.V)
	if err != nil {
		return fmt.Errorf("plaintext: encode: %w", err)
	}
	vals, err := json.Marshal(f.Secrets)
	if err != nil {
		return fmt.Errorf("plaintext: encode: %w", err)
	}
	out["v"], out["secrets"] = v, vals

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("plaintext: encode: %w", err)
	}
	b = append(b, '\n')

	path := p.filePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("plaintext: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("plaintext: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("plaintext: rename %s: %w", path, err)
	}
	return nil
}
