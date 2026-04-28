# Expected Output — Task 05 (Code refactor rename)

## Pass criteria (pinned assertions)

### After rename:
- `/tmp/r1-eval-rename-test.go` exists
- Contains `greeting :=` (not `msg :=`)
- Does NOT contain `msg` as a variable name (local to greet function)
- go vet passes (exit 0) or go build passes

### Final file content (core lines only):
```go
func greet(name string) string {
    greeting := "Hello, " + name
    return greeting
}
```

## Allowed variance

- Whitespace around `:=` may vary
- go vet may warn on the `main` function style — non-fatal
- `msg` may legitimately appear in the function parameter name `name` — that's fine

## Failure indicators

- `msg` still present as variable name in greet function after edit
- `greeting` not present
- go vet/build fails (compile error introduced by bad rename)
