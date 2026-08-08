package totp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// IndexVersion is the totp.json schema version this build writes. A file
// declaring a newer version is refused rather than silently downgraded, so a
// future banshee's index survives being opened by an older binary.
const IndexVersion = 1

// Entry is one TOTP account's non-secret metadata. The seed itself never
// appears here — it lives in an internal/secrets Store under totp/<Name> —
// which is what lets this file be a plain 0644 JSON blob and lets the user
// switch storage backends without rewriting their account list.
type Entry struct {
	// Name is the user-chosen label. It is the identity of the entry: the
	// launcher fuzzy-matches it and the secret key is derived from it.
	Name string `json:"name"`
	// Issuer is the service the code belongs to, shown as context. It comes
	// from an otpauth URI and is purely cosmetic.
	Issuer string `json:"issuer,omitempty"`
	// Digits is the code length; 0 means DefaultDigits.
	Digits int `json:"digits,omitempty"`
	// Period is the code lifetime in seconds; 0 means DefaultPeriod.
	Period int `json:"period,omitempty"`
	// Algorithm is the HMAC hash; empty means AlgSHA1.
	Algorithm string `json:"algorithm,omitempty"`
	// Created records when the entry was added, for display and ordering.
	Created time.Time `json:"created,omitempty"`
}

// Params resolves the entry's algorithm parameters, substituting the RFC
// defaults for unset fields. Every code path should go through this rather
// than reading Digits/Period/Algorithm directly, so an entry written by an
// older banshee (or hand-edited to omit a key) still produces codes.
func (e Entry) Params() Params {
	p := DefaultParams()
	if e.Digits != 0 {
		p.Digits = e.Digits
	}
	if e.Period != 0 {
		p.Period = e.Period
	}
	if strings.TrimSpace(e.Algorithm) != "" {
		p.Algorithm = e.Algorithm
	}
	return p
}

// Index is the on-disk totp.json: the chosen secrets backend plus the account
// metadata. Unknown JSON keys are ignored for forward compatibility.
type Index struct {
	// V is the schema version; see IndexVersion.
	V int `json:"v"`
	// Backend names the internal/secrets Store holding the seeds
	// ("plaintext", "keyring", …). It is persisted here rather than in
	// banshee.conf because banshee never writes the user's config file.
	Backend string `json:"backend,omitempty"`
	// Entries is the account list, in insertion order.
	Entries []Entry `json:"entries,omitempty"`
}

// LoadIndex reads the index at path. A missing file is not an error — it is
// the ordinary state before the first code is added — and yields an empty
// index at the current version, ready to Add to and Save.
func LoadIndex(path string) (Index, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Index{V: IndexVersion}, nil
	}
	if err != nil {
		return Index{}, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return Index{}, fmt.Errorf("totp: %s is not valid JSON: %w", path, err)
	}
	if idx.V > IndexVersion {
		return Index{}, fmt.Errorf("totp: %s declares version %d, this banshee understands %d", path, idx.V, IndexVersion)
	}
	// A missing "v" (or an explicit 0) is treated as the original schema so
	// a hand-written file still loads; anything newer is refused above.
	if idx.V == 0 {
		idx.V = IndexVersion
	}
	return idx, nil
}

// SaveIndex writes idx to path atomically (tmp + rename), so a crash or a
// concurrent read never observes a truncated account list. The file is 0644:
// it holds no secret material by construction, and keeping it world-readable
// makes it obvious that the seeds are somewhere else.
//
// The write merges onto whatever is already on disk, through
// map[string]json.RawMessage at the top level and again inside each entry
// (matched by name), so keys this build does not understand survive byte-exact.
// The forward-compatibility rule forbids a load-modify-save through Index,
// which would drop them — the same reasoning, and the same shape, as
// connectors.SaveRepoBinding. The version guard in LoadIndex does not cover
// this: an additive field keeps v: 1 by this repo's convention, so it is
// exactly the case that never trips the guard that would otherwise lose data.
func SaveIndex(path string, idx Index) error {
	if path == "" {
		return errors.New("totp: no index path")
	}
	if idx.V == 0 {
		idx.V = IndexVersion
	}

	top, priorEntries, err := readRawIndex(path)
	if err != nil {
		return err
	}
	// The entry list is dropped from the value handed to mergeKnown and rebuilt
	// element by element below, so each entry gets its own merge.
	if top, err = mergeKnown(top, Index{V: idx.V, Backend: idx.Backend}, indexKeys); err != nil {
		return err
	}
	if len(idx.Entries) > 0 {
		entries := make([]map[string]json.RawMessage, 0, len(idx.Entries))
		for _, e := range idx.Entries {
			merged, err := mergeKnown(priorEntries[foldName(e.Name)], e, entryKeys)
			if err != nil {
				return err
			}
			entries = append(entries, merged)
		}
		raw, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		top["entries"] = raw
	}

	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// indexKeys and entryKeys are the JSON object keys Index and Entry own.
// Anything else found on disk was written by a different banshee, and
// mergeKnown carries it through untouched. Both sets are derived from the
// struct tags rather than listed by hand, so adding a field cannot leave them
// stale — a stale set would silently duplicate or drop that field on rewrite.
var (
	indexKeys = jsonKeys(reflect.TypeOf(Index{}))
	entryKeys = jsonKeys(reflect.TypeOf(Entry{}))
)

// jsonKeys returns the set of JSON object keys the struct type t marshals to.
func jsonKeys(t reflect.Type) map[string]bool {
	keys := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// mergeKnown overlays typed's own JSON keys onto raw and returns the result.
// A key in own that typed did not emit (an omitempty field sitting at its zero
// value) is deleted, so typed stays authoritative for everything it can
// express, while every key raw carries that typed knows nothing about survives
// verbatim.
func mergeKnown(raw map[string]json.RawMessage, typed any, own map[string]bool) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	fresh := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &fresh); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage, len(fresh))
	}
	for k := range own {
		if v, ok := fresh[k]; ok {
			raw[k] = v
		} else {
			delete(raw, k)
		}
	}
	return raw, nil
}

// readRawIndex reads path as raw JSON for SaveIndex to merge onto: the
// top-level object, plus its entries keyed by folded name. A missing file
// yields empty maps. A file that is not an object, or whose "entries" is not an
// array of objects, is an error rather than something to overwrite — it is not
// an index, and clobbering it would destroy whatever it actually is.
func readRawIndex(path string) (map[string]json.RawMessage, map[string]map[string]json.RawMessage, error) {
	top := map[string]json.RawMessage{}
	entries := map[string]map[string]json.RawMessage{}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return top, entries, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, nil, fmt.Errorf("totp: %s is not a JSON object; refusing to overwrite: %w", path, err)
	}
	raw, ok := top["entries"]
	if !ok {
		return top, entries, nil
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, nil, fmt.Errorf("totp: %s has a malformed \"entries\" array; refusing to overwrite: %w", path, err)
	}
	for _, e := range list {
		var name string
		if err := json.Unmarshal(e["name"], &name); err != nil {
			// An entry with no usable name cannot be matched to anything in
			// idx.Entries, so there is nothing to merge it onto.
			continue
		}
		entries[foldName(name)] = e
	}
	return top, entries, nil
}

// foldName reduces an entry name to the identity Find and Add compare by:
// trimmed and case-folded.
func foldName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// Find returns the entry called name. The match is case-insensitive because
// the name is a human label the user retypes, not an identifier they copy;
// the returned Entry carries the exact stored spelling, which is what the
// secret key must be derived from.
func (idx Index) Find(name string) (Entry, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Entry{}, false
	}
	for _, e := range idx.Entries {
		if strings.EqualFold(e.Name, name) {
			return e, true
		}
	}
	return Entry{}, false
}

// Add appends e, refusing a name that already exists. The duplicate check is
// case-insensitive: two entries differing only in case would be
// indistinguishable in the launcher list while pointing at different secret
// keys, which is the worst of both worlds.
//
// The receiver is a pointer because Add mutates the entry list; callers hold
// the Index returned by LoadIndex and pass it to SaveIndex afterwards.
func (idx *Index) Add(e Entry) error {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		return errors.New("totp: entry needs a name")
	}
	if _, ok := idx.Find(e.Name); ok {
		return fmt.Errorf("totp: an entry named %q already exists", e.Name)
	}
	if idx.V == 0 {
		idx.V = IndexVersion
	}
	idx.Entries = append(idx.Entries, e)
	return nil
}
