package sandbox

import (
	"testing"
)

// Pure argv-construction tests: no docker daemon is touched.
func TestDockerArgs(t *testing.T) {
	work := t.TempDir()
	roDir := t.TempDir()

	cases := []struct {
		name    string
		policy  Policy
		wantSeq [][]string
		banSeq  [][]string
	}{
		{
			name:   "hardened baseline with egress denied",
			policy: Policy{DockerImage: "golang:1.24", AllowEgress: false, AllowWrite: []string{work}},
			wantSeq: [][]string{
				{"run", "--rm"},
				{"--security-opt=no-new-privileges"},
				{"--cap-drop=ALL"},
				{"--network=none"},
				{"-v", work + ":" + work},
				{"-w", work},
				{"golang:1.24", "bash", "-c", "go test ./..."},
			},
		},
		{
			name:    "egress allowed uses bridge",
			policy:  Policy{DockerImage: "golang:1.24", AllowEgress: true},
			wantSeq: [][]string{{"--network=bridge"}},
			banSeq:  [][]string{{"--network=none"}},
		},
		{
			name:    "allow-read mounted ro",
			policy:  Policy{DockerImage: "img", AllowRead: []string{roDir}},
			wantSeq: [][]string{{"-v", roDir + ":" + roDir + ":ro"}},
		},
		{
			name:   "nonexistent mounts skipped",
			policy: Policy{DockerImage: "img", AllowWrite: []string{"/no/such/dir"}},
			banSeq: [][]string{{"-v", "/no/such/dir:/no/such/dir"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := dockerArgs("go test ./...", work, tc.policy)
			for _, seq := range tc.wantSeq {
				if !hasSeq(args, seq...) {
					t.Errorf("argv missing %v\nargs: %v", seq, args)
				}
			}
			for _, seq := range tc.banSeq {
				if hasSeq(args, seq...) {
					t.Errorf("argv must not contain %v\nargs: %v", seq, args)
				}
			}
		})
	}
}
