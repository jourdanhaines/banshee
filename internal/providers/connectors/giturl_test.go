package connectors

import "testing"

func TestNormalizeGitURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"scp form with .git", "git@github.com:jourdanhaines/banshee.git", "https://github.com/jourdanhaines/banshee", true},
		{"scp form without .git", "git@github.com:jourdanhaines/banshee", "https://github.com/jourdanhaines/banshee", true},
		{"scp form no user", "github.com:jourdanhaines/banshee.git", "https://github.com/jourdanhaines/banshee", true},
		{"scp form trailing slash", "git@github.com:jourdanhaines/banshee.git/", "https://github.com/jourdanhaines/banshee", true},
		{"ssh scheme", "ssh://git@github.com/jourdanhaines/banshee.git", "https://github.com/jourdanhaines/banshee", true},
		{"ssh scheme with port", "ssh://git@github.com:22/jourdanhaines/banshee.git", "https://github.com/jourdanhaines/banshee", true},
		{"git scheme", "git://github.com/jourdanhaines/banshee.git", "https://github.com/jourdanhaines/banshee", true},
		{"git+ssh scheme", "git+ssh://git@github.com/u/r.git", "https://github.com/u/r", true},
		{"https passthrough", "https://github.com/jourdanhaines/banshee.git", "https://github.com/jourdanhaines/banshee", true},
		{"https without .git", "https://github.com/jourdanhaines/banshee", "https://github.com/jourdanhaines/banshee", true},
		{"https strips credentials", "https://user:token@github.com/u/r.git", "https://github.com/u/r", true},
		{"https keeps explicit port", "https://git.example.com:8443/u/r.git", "https://git.example.com:8443/u/r", true},
		{"gitlab subgroup", "git@gitlab.com:group/sub/repo.git", "https://gitlab.com/group/sub/repo", true},
		{"whitespace and newline", "  git@github.com:u/r.git\n", "https://github.com/u/r", true},
		{"local absolute path", "/home/alpinedev/dev/banshee", "", false},
		{"local relative path", "../other/repo", "", false},
		{"file scheme", "file:///home/alpinedev/dev/banshee", "", false},
		{"empty", "", "", false},
		{"host only", "git@github.com:", "", false},
		{"scheme without host", "https:///u/r", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NormalizeGitURL(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("NormalizeGitURL(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}
