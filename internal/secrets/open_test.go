package secrets

import (
	"strings"
	"testing"
)

func TestOpen(t *testing.T) {
	tests := []struct {
		name          string
		backend       string
		wantName      string
		wantAuth      bool
		wantErrSubstr string
	}{
		{name: "plaintext", backend: BackendPlaintext, wantName: BackendPlaintext},
		{name: "keyring", backend: BackendKeyring, wantName: BackendKeyring},
		{name: "nimbus", backend: BackendNimbus, wantName: BackendNimbus, wantAuth: true},
		{name: "unknown", backend: "vault", wantErrSubstr: `unknown secrets backend "vault"`},
		{name: "empty", backend: "", wantErrSubstr: "unknown secrets backend"},
		{name: "case sensitive", backend: "Keyring", wantErrSubstr: "unknown secrets backend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := Open(tt.backend)
			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("Open(%q) succeeded, want an error", tt.backend)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("err = %q, want it to contain %q", err, tt.wantErrSubstr)
				}
				if st != nil {
					t.Fatalf("Open(%q) returned a Store alongside its error", tt.backend)
				}
				return
			}
			if err != nil {
				t.Fatalf("Open(%q): %v", tt.backend, err)
			}
			if st.Name() != tt.wantName {
				t.Fatalf("Name = %q, want %q", st.Name(), tt.wantName)
			}
			if st.AuthPerAccess() != tt.wantAuth {
				t.Fatalf("AuthPerAccess = %v, want %v", st.AuthPerAccess(), tt.wantAuth)
			}
		})
	}
}
