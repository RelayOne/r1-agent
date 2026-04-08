The file `util.go` contains a `ParseDuration` function that parses a human-readable duration string (e.g., "30s", "5m", "2h") and returns the equivalent `time.Duration`. The function currently has no tests.

Write comprehensive tests in a new file `util_test.go` that cover:

1. Valid inputs: seconds ("30s"), minutes ("5m"), hours ("2h")
2. Edge cases: empty string, zero values ("0s"), large values
3. Error cases: invalid suffix ("10x"), missing suffix ("10"), non-numeric prefix ("abcs"), negative values ("-5s")

The tests should be in the `main` package and use standard `testing` patterns. Do not modify `util.go`.
