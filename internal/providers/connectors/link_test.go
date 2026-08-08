package connectors

import (
	"testing"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

func TestRegisterLinkHandler(t *testing.T) {
	tests := []struct {
		name    string
		argv    func(repo string) []string
		wantErr bool
		wantVal string
	}{
		{
			name:    "writes the binding",
			argv:    func(repo string) []string { return []string{"railway", repo, "proj-1"} },
			wantVal: "proj-1",
		},
		{
			name:    "wrong argv length errors",
			argv:    func(repo string) []string { return []string{"railway", repo} },
			wantErr: true,
		},
		{
			name:    "empty binding errors",
			argv:    func(repo string) []string { return []string{"railway", repo, ""} },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			d := launch.NewDispatcher()
			RegisterLinkHandler(d)

			err := d.Dispatch(providers.Action{Kind: ActConnectorLink, Argv: tt.argv(repo)})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Dispatch error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantVal == "" {
				return
			}
			rc, err := LoadRepoConfig(repo)
			if err != nil {
				t.Fatal(err)
			}
			if rc.Connectors["railway"] != tt.wantVal {
				t.Errorf("binding = %q, want %q", rc.Connectors["railway"], tt.wantVal)
			}
		})
	}
}
