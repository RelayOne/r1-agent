# Task 05 — Code refactor: symbol rename + verify

**Ability under test:** LSP rename (row #64) + Edit tool (row #4)  
**Reference product:** Claude Code (`LSP` tool + `Edit`)  
**R1 equivalent:** `internal/goast/` + `internal/tools/str_replace.go` + `internal/symindex/`

## Task description

Create a temporary Go file, perform a variable rename using R1's
tools, and verify the rename is clean.

1. Write this file to `/tmp/r1-eval-rename-test.go`:
   ```go
   package main

   import "fmt"

   func greet(name string) string {
       msg := "Hello, " + name
       return msg
   }

   func main() {
       result := greet("world")
       fmt.Println(result)
   }
   ```

2. Rename the local variable `msg` → `greeting` in the `greet` function
   using R1's edit_file tool (str_replace).

3. Read the file back and verify:
   - No remaining occurrences of `msg` as a variable name
   - `greeting` appears in its place
   - The file still compiles: `go vet /tmp/r1-eval-rename-test.go`
     (or `go build` if vet isn't available standalone)

4. **Assess the LSP gap:** R1 uses AST-based rename (`internal/goast/`)
   rather than a running LSP server. Document whether this approach
   handles the rename correctly for this case, and note that multi-file
   renames across package boundaries require the full `internal/goast/`
   + Bash-based refactoring pipeline (not a live LSP session).

## Acceptance criteria

- [ ] `/tmp/r1-eval-rename-test.go` created successfully
- [ ] `msg` renamed to `greeting` in all occurrences
- [ ] No `msg` variable references remain
- [ ] File compiles cleanly (go vet or go build passes)

## Evaluation scoring

- PASS: all 4 ACs met
- PARTIAL: rename done but compile step skipped
- FAIL: edit tool fails or rename is incomplete
