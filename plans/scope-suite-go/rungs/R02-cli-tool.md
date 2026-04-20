# R02-go — CLI that deduplicates args

Go binary that reads `os.Args`, preserves first-seen order, drops
duplicates, prints one per line.

## Scope

Module `github.com/example/uniq-args`.

`main.go`:
```go
package main

import (
    "fmt"
    "os"
)

func main() {
    seen := map[string]bool{}
    for _, a := range os.Args[1:] {
        if seen[a] {
            continue
        }
        seen[a] = true
        fmt.Println(a)
    }
}
```

Integration test in `main_test.go` (or `cmd_test.go`) uses
`exec.Command(os.Args[0], ...)` to exec the built binary via
`go test -run TestCLI`, OR uses a separate `cli_test.go` in a
test package that does `exec.Command("go", "run", ".", ...)`.

## Acceptance

- `go.mod` with module path.
- `go build ./...` exits 0.
- `go test ./...` exits 0; at least two cases: no-args → empty;
  `a b a c b` → `a\nb\nc\n`.
- `go vet ./...` exits 0.

## What NOT to do

- No third-party deps.
- No flag parsing (`flag` package).
- No subcommands.
