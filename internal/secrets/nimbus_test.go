package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNimbusOperationsAreUnavailable(t *testing.T) {
	n := NewNimbus(NimbusConfig{URL: "https://nimbus.invalid", Token: "t", KeyPath: "/nonexistent/key"})
	ctx := context.Background()

	tests := []struct {
		name string
		op   func() error
	}{
		{"get", func() error { _, err := n.Get(ctx, "totp/x", Credential{Password: "hunter2"}); return err }},
		{"set", func() error { return n.Set(ctx, "totp/x", "SECRET", Credential{Password: "hunter2"}) }},
		{"delete", func() error { return n.Delete(ctx, "totp/x", Credential{Password: "hunter2"}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.op()
			if !errors.Is(err, ErrNotConfigured) {
				t.Fatalf("err = %v, want ErrNotConfigured", err)
			}
			if !strings.Contains(err.Error(), "not available yet") {
				t.Fatalf("err = %q, want the user-facing coming-soon wording", err)
			}
			for _, leak := range []string{"hunter2", "SECRET"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("err leaks %q: %s", leak, err)
				}
			}
		})
	}
}

func TestNimbusMetadata(t *testing.T) {
	n := NewNimbus(NimbusConfig{})
	if n.Name() != BackendNimbus {
		t.Fatalf("Name = %q, want %q", n.Name(), BackendNimbus)
	}
	if !n.AuthPerAccess() {
		t.Fatal("AuthPerAccess = false, want true: every Nimbus access needs a fresh password")
	}
}
