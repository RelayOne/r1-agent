package sandbox

import (
	"os"
	"strconv"
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

// TestDockerArgsRunsAsHostUser pins that the container runs as the invoking
// host uid:gid, so files it creates in the bind-mounted worktree stay owned
// by the host user (root-owned files break the host-side shadow-checkpoint /
// commit / cleanup that runs after the sandboxed command returns).
func TestDockerArgsRunsAsHostUser(t *testing.T) {
	uid := os.Getuid()
	if uid < 0 {
		t.Skip("no numeric uid on this platform (Windows); --user flag intentionally omitted")
	}
	args := dockerArgs("true", t.TempDir(), Policy{DockerImage: "img"})
	want := strconv.Itoa(uid) + ":" + strconv.Itoa(os.Getgid())
	if !hasSeq(args, "--user", want) {
		t.Errorf("argv missing [--user %s]\nargs: %v", want, args)
	}
}
