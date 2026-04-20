# R01-go — Hello world module + test

The smallest possible SOW in Go. One package, one function, one test.

## Scope

Create `go.mod` with `module github.com/example/greet` and `go 1.22+`.

Create `greet.go`:

```go
package greet

import "fmt"

func Greet(name string) string {
    return fmt.Sprintf("Hello, %s!", name)
}
```

Create `greet_test.go`:

```go
package greet

import (
    "strings"
    "testing"
)

func TestGreet(t *testing.T) {
    got := Greet("world")
    if !strings.Contains(got, "world") {
        t.Fatalf("expected %q to contain 'world'", got)
    }
}
```

## Acceptance

- `go.mod` with module path and go directive.
- `greet.go` exports `Greet`.
- `greet_test.go` exercises it.
- `go build ./...` exits 0.
- `go test ./...` exits 0 with the greet test passing.
- `go vet ./...` exits 0.

## What NOT to do

- No dependencies (empty `require` block).
- No `cmd/` binary.
- No goroutines, channels, or concurrency.
- No custom linter config.
