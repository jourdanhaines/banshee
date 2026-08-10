package cliphist

import (
	"strings"
	"testing"
)

func TestLooksSecret(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		want       bool
		wantReason string
	}{
		{"PEM private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----", true, "private key"},
		{"AWS access key", "AKIAIOSFODNN7EXAMPLE", true, "AWS access key"},
		{"GitHub classic token", "ghp_16C7e42F292c6912E7710c838347Ae178B4a", true, "GitHub token"},
		{"GitHub fine-grained token", "github_pat_11ABCDEFG0_abcdefghijklmnopqrstuvwxyz", true, "GitHub token"},
		{"OpenAI-style key", "sk-proj-abcdefghijklmnopqrstuvwxyz123456", true, "API key"},
		{"JWT", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dQw4w9WgXcQdQw4w9WgXcQ", true, "JWT"},
		{"high-entropy token", "x7#Kp9$mQ2vL5@nR8wZ4", true, "looks like a secret"},
		{"prose", "the quick brown fox jumps over the lazy dog", false, ""},
		{"short token", "hunter2", false, ""},
		{"URL", "https://example.com/some/long/path?token=maybe#frag", false, ""},
		{"repeated low-entropy token", strings.Repeat("aB1", 10), false, ""},
		{"empty", "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := LooksSecret(tt.text)
			if got != tt.want {
				t.Fatalf("LooksSecret(%q) = %v, want %v", tt.text, got, tt.want)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestMaskTitle(t *testing.T) {
	tests := []struct {
		name string
		e    Entry
		want string
	}{
		{
			name: "hinted entry fully masked",
			e:    Entry{Text: "correct horse battery staple", MaskReason: MaskReasonHint},
			want: "••••••••",
		},
		{
			name: "heuristic entry keeps three-rune handle",
			e:    Entry{Text: "ghp_16C7e42F292c6912E7710c838347Ae178B4a", MaskReason: "GitHub token"},
			want: "ghp•••••",
		},
		{
			name: "tiny text fully masked",
			e:    Entry{Text: "ab", MaskReason: "looks like a secret"},
			want: "••••••••",
		},
		{
			name: "mask length is fixed regardless of content length",
			e:    Entry{Text: strings.Repeat("Z", 500) + "1$a", MaskReason: "looks like a secret"},
			want: "ZZZ•••••",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskTitle(tt.e); got != tt.want {
				t.Errorf("maskTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}
