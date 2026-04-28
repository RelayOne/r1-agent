# Expected Output — Task 02 (File edit)

## Pass criteria (pinned assertions)

### After exact str_replace:
- File contains: `Hello R1`
- File does NOT contain: `Hello world`
- Line 1 only changed; lines 2 and 3 unchanged

### After fuzzy (whitespace-normalized) str_replace:
- File contains: `This is line TWO`
- File does NOT contain: `This is line 2`
- R1 matched despite leading-space mismatch

### Final file contents (exact):
```
Hello R1
This is line TWO
This is line 3
```

## Allowed variance

- R1 may report the whitespace fallback in its tool result ("matched via whitespace normalization" or similar) — this is informational and counts as PASS.

## Failure indicators

- `Hello world` still present after edit
- Fuzzy edit fails with "no unique match" (exact match shouldn't be required)
- File contents contain extra lines or missing lines
