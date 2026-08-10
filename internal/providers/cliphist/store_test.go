package cliphist

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedClock returns a controllable now func starting at a fixed instant.
func fixedClock() (func() time.Time, *time.Time) {
	t0 := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	now := &t0
	return func() time.Time { return *now }, now
}

func TestStoreAdd(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, s *Store, now *time.Time)
	}{
		{
			name: "eviction past cap drops oldest",
			run: func(t *testing.T, s *Store, _ *time.Time) {
				for _, txt := range []string{"a", "b", "c", "d"} {
					if _, ok := s.Add(KindText, "text/plain", []byte(txt), false, ""); !ok {
						t.Fatalf("Add(%q) dropped", txt)
					}
				}
				got := s.List()
				if len(got) != 3 {
					t.Fatalf("len(List()) = %d, want 3", len(got))
				}
				if got[0].Text != "d" || got[2].Text != "b" {
					t.Errorf("List() = [%s %s %s], want newest-first d c b", got[0].Text, got[1].Text, got[2].Text)
				}
			},
		},
		{
			name: "consecutive duplicate collapses and bumps",
			run: func(t *testing.T, s *Store, now *time.Time) {
				first, _ := s.Add(KindText, "text/plain", []byte("same"), false, "")
				*now = now.Add(2 * time.Second)
				second, ok := s.Add(KindText, "text/plain", []byte("same"), false, "")
				if !ok {
					t.Fatal("duplicate Add dropped")
				}
				if len(s.List()) != 1 {
					t.Fatalf("len = %d, want 1", len(s.List()))
				}
				if second.ID != first.ID || second.Copies != 2 {
					t.Errorf("ID = %d Copies = %d, want ID %d Copies 2", second.ID, second.Copies, first.ID)
				}
				if !second.Time.After(first.Time) {
					t.Error("Time not bumped")
				}
			},
		},
		{
			name: "non-consecutive duplicate appends",
			run: func(t *testing.T, s *Store, _ *time.Time) {
				s.Add(KindText, "text/plain", []byte("x"), false, "")
				s.Add(KindText, "text/plain", []byte("y"), false, "")
				s.Add(KindText, "text/plain", []byte("x"), false, "")
				if n := len(s.List()); n != 3 {
					t.Errorf("len = %d, want 3", n)
				}
			},
		},
		{
			name: "oversized text dropped",
			run: func(t *testing.T, s *Store, _ *time.Time) {
				if _, ok := s.Add(KindText, "text/plain", make([]byte, MaxTextBytes+1), false, ""); ok {
					t.Error("oversized text accepted")
				}
				if len(s.List()) != 0 {
					t.Error("entry retained")
				}
			},
		},
		{
			name: "image without image dir dropped",
			run: func(t *testing.T, s *Store, _ *time.Time) {
				if _, ok := s.Add(KindImage, "image/png", []byte("png"), false, ""); ok {
					t.Error("image accepted with no image dir")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nowFn, now := fixedClock()
			s := NewStore(WithCap(3), WithClock(nowFn))
			tt.run(t, s, now)
		})
	}
}

func TestStoreSuppressNext(t *testing.T) {
	nowFn, now := fixedClock()
	s := NewStore(WithClock(nowFn))

	hash := sha256.Sum256([]byte("123456"))
	s.SuppressNext(hash)
	if _, ok := s.Add(KindText, "text/plain", []byte("123456"), false, ""); ok {
		t.Fatal("suppressed capture was added")
	}
	// One-shot: the same content copied again is history-worthy.
	if _, ok := s.Add(KindText, "text/plain", []byte("123456"), false, ""); !ok {
		t.Fatal("second capture dropped; suppression not one-shot")
	}

	// An expired suppression must not swallow anything.
	s2 := NewStore(WithClock(nowFn))
	s2.SuppressNext(sha256.Sum256([]byte("later")))
	*now = now.Add(suppressTTL + time.Second)
	if _, ok := s2.Add(KindText, "text/plain", []byte("later"), false, ""); !ok {
		t.Fatal("expired suppression still active")
	}
}

func TestStoreImages(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(WithCap(2), WithImageDir(dir))

	img, ok := s.Add(KindImage, "image/png", []byte("fakepng"), false, "")
	if !ok {
		t.Fatal("image Add dropped")
	}
	if filepath.Dir(img.ImagePath) != dir || filepath.Ext(img.ImagePath) != ".png" {
		t.Fatalf("ImagePath = %q, want *.png under %q", img.ImagePath, dir)
	}
	if got, err := os.ReadFile(img.ImagePath); err != nil || string(got) != "fakepng" {
		t.Fatalf("image file = %q, %v", got, err)
	}

	// Delete removes the file.
	if !s.Delete(img.ID) {
		t.Fatal("Delete returned false")
	}
	if _, err := os.Stat(img.ImagePath); !os.IsNotExist(err) {
		t.Errorf("image file survives Delete: %v", err)
	}

	// Eviction removes the file.
	old, _ := s.Add(KindImage, "image/png", []byte("old"), false, "")
	s.Add(KindText, "text/plain", []byte("a"), false, "")
	s.Add(KindText, "text/plain", []byte("b"), false, "")
	if _, err := os.Stat(old.ImagePath); !os.IsNotExist(err) {
		t.Errorf("image file survives eviction: %v", err)
	}

	// Clear removes files and empties the ring.
	last, _ := s.Add(KindImage, "image/jpeg", []byte("jpg"), false, "")
	if filepath.Ext(last.ImagePath) != ".jpg" {
		t.Errorf("jpeg extension = %q", filepath.Ext(last.ImagePath))
	}
	s.Clear()
	if len(s.List()) != 0 {
		t.Error("List() not empty after Clear")
	}
	if _, err := os.Stat(last.ImagePath); !os.IsNotExist(err) {
		t.Errorf("image file survives Clear: %v", err)
	}
}

func TestStoreGetDelete(t *testing.T) {
	s := NewStore()
	e, _ := s.Add(KindText, "text/plain", []byte("keep"), false, "")

	if got, ok := s.Get(e.ID); !ok || got.Text != "keep" {
		t.Fatalf("Get(%d) = %+v, %v", e.ID, got, ok)
	}
	if _, ok := s.Get(999); ok {
		t.Error("Get(999) found a ghost")
	}
	if s.Delete(999) {
		t.Error("Delete(999) returned true")
	}
	if !s.Delete(e.ID) || len(s.List()) != 0 {
		t.Error("Delete did not remove the entry")
	}
}
