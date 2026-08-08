package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	gokeyring "github.com/zalando/go-keyring"
)

// fakeKeyring is an in-memory keyringClient. It exists so these tests never
// touch DBus: a real Secret Service is not available in CI and a locked one
// would block.
type fakeKeyring struct {
	items map[string]string
	// err, when non-nil, is returned by every call instead of operating.
	err error
	// block, when non-nil, is waited on before each call returns, which is
	// how the cancellation test simulates a hung DBus round trip.
	block chan struct{}
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{items: map[string]string{}} }

func (f *fakeKeyring) wait() {
	if f.block != nil {
		<-f.block
	}
}

func (f *fakeKeyring) Set(service, user, secret string) error {
	f.wait()
	if f.err != nil {
		return f.err
	}
	f.items[service+"\x00"+user] = secret
	return nil
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	f.wait()
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.items[service+"\x00"+user]
	if !ok {
		return "", gokeyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	f.wait()
	if f.err != nil {
		return f.err
	}
	key := service + "\x00" + user
	if _, ok := f.items[key]; !ok {
		return gokeyring.ErrNotFound
	}
	delete(f.items, key)
	return nil
}

func TestKeyringRoundtrip(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"simple", "totp/github", "JBSWY3DPEHPK3PXP"},
		{"nested name", "totp/work/vpn", "GEZDGNBVGY3TQOJQ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeKeyring()
			k := newKeyringWith(fake)
			ctx := context.Background()

			if err := k.Set(ctx, tt.key, tt.value, Credential{}); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if got := fake.items[keyringService+"\x00"+tt.key]; got != tt.value {
				t.Fatalf("stored under service %q/user %q = %q, want %q", keyringService, tt.key, got, tt.value)
			}
			got, err := k.Get(ctx, tt.key, Credential{})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != tt.value {
				t.Fatalf("Get = %q, want %q", got, tt.value)
			}
			if err := k.Delete(ctx, tt.key, Credential{}); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := k.Get(ctx, tt.key, Credential{}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestKeyringErrorMapping(t *testing.T) {
	daemonDown := errors.New("dbus: couldn't determine address of session bus")

	tests := []struct {
		name      string
		backend   error
		op        string
		wantIs    error
		wantMatch string
	}{
		{"not found maps to sentinel", gokeyring.ErrNotFound, "get", ErrNotFound, "totp/x"},
		{"unsupported platform keeps cause", gokeyring.ErrUnsupportedPlatform, "get", gokeyring.ErrUnsupportedPlatform, "Secret Service"},
		{"no daemon gets a hint", daemonDown, "set", daemonDown, "gnome-keyring/KeePassXC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeKeyring()
			fake.err = tt.backend
			k := newKeyringWith(fake)
			ctx := context.Background()

			var err error
			switch tt.op {
			case "get":
				_, err = k.Get(ctx, "totp/x", Credential{})
			case "set":
				err = k.Set(ctx, "totp/x", "SECRET", Credential{})
			default:
				err = k.Delete(ctx, "totp/x", Credential{})
			}
			if !errors.Is(err, tt.wantIs) {
				t.Fatalf("err = %v, want errors.Is %v", err, tt.wantIs)
			}
			if !strings.Contains(err.Error(), tt.wantMatch) {
				t.Fatalf("err = %q, want it to contain %q", err, tt.wantMatch)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("err leaks the secret value: %q", err)
			}
		})
	}
}

func TestKeyringContextEndsHungCall(t *testing.T) {
	tests := []struct {
		name   string
		ctx    func() (context.Context, context.CancelFunc)
		wantIs error
	}{
		{
			name: "cancel",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantIs: context.Canceled,
		},
		{
			name: "deadline",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 20*time.Millisecond)
			},
			wantIs: context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeKeyring()
			fake.block = make(chan struct{})
			// Release the blocked goroutine at test end so the fake's
			// state is not mutated while a later assertion reads it.
			defer close(fake.block)

			k := newKeyringWith(fake)
			ctx, cancel := tt.ctx()
			defer cancel()

			done := make(chan error, 1)
			go func() {
				_, err := k.Get(ctx, "totp/x", Credential{})
				done <- err
			}()

			select {
			case err := <-done:
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("err = %v, want %v", err, tt.wantIs)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Get did not return while the backend was hung")
			}
		})
	}
}

func TestKeyringMetadata(t *testing.T) {
	k := NewKeyring()
	if k.Name() != BackendKeyring {
		t.Fatalf("Name = %q, want %q", k.Name(), BackendKeyring)
	}
	if k.AuthPerAccess() {
		t.Fatal("AuthPerAccess = true, want false: the session owns unlock policy")
	}
}
