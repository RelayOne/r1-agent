// Spec 5 §6 + §10 T3: separate Go module so default `go test ./...`
// in the parent module doesn't try to compile this package and pull
// in Playwright's transitive deps. Run from this directory:
//
//   cd cmd/r1-server/e2e && go test -tags=e2e ./...
//
// Lives in a release-rehearsal CI lane only — see services/cloudbuild-e2e.yaml.

module github.com/RelayOne/r1/cmd/r1-server/e2e

go 1.23
