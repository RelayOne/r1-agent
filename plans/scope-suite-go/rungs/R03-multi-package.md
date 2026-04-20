# R03-go — Multi-package module

Go module split across two packages where `cmd/` imports from
`pkg/`. Exercises proper module + package layout.

## Scope

Module `github.com/example/mathlib`.

`pkg/math/math.go`:
```go
package math

func Add(a, b int) int { return a + b }
func Mul(a, b int) int { return a * b }
```

`pkg/math/math_test.go` with `TestAdd` and `TestMul`.

`cmd/mathcli/main.go` that parses two ints from `os.Args`, calls
`math.Add` and `math.Mul`, prints `add\nmul\n`.

`cmd/mathcli/main_test.go` (optional) exercises the CLI via
`go test` subprocess.

## Acceptance

- `go.mod` declares the module.
- `go build ./...` exits 0 — both `pkg/math` and `cmd/mathcli`
  compile.
- `go test ./...` exits 0 with tests in `pkg/math`.
- `go vet ./...` exits 0.

## What NOT to do

- No third-party deps.
- No more than these two packages.
- No goroutines.
