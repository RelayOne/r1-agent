# Task 02 — File edit by str_replace + diff verification

**Ability under test:** Edit tool (row #4 in parity matrix)  
**Reference product:** Claude Code (`tools.Edit`)  
**R1 equivalent:** `internal/tools/tools.go` `edit_file` + `internal/tools/str_replace.go`

## Task description

1. Create a temp file at `/tmp/r1-eval-task-02.txt` with this content:
   ```
   Hello world
   This is line 2
   This is line 3
   ```

2. Edit the file using str_replace to change "Hello world" to "Hello R1".

3. Verify the diff shows exactly one changed line.

4. Test R1's cascading fuzzy-match by making a second edit that has
   leading whitespace variation:
   - old_string: `  This is line 2` (with 2 spaces prepended — intentional mismatch)
   - new_string: `This is line TWO`
   
   R1 should still match via whitespace-normalization fallback.

5. Read the file back and report final contents.

## Acceptance criteria

- [ ] str_replace edit succeeds for exact match (step 2)
- [ ] Diff shows exactly "Hello world" → "Hello R1" on line 1
- [ ] Whitespace-normalized fallback edit succeeds (step 4)
- [ ] Final file contains "Hello R1", "This is line TWO", "This is line 3"

## Evaluation scoring

- PASS: all 4 ACs met
- PARTIAL: exact match works but fuzzy fallback fails
- FAIL: edit tool fails entirely
