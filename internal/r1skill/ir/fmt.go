package ir

import "fmt"

// fmtSprintf is the actual implementation of sprintf. Isolated in its
// own file so a future ir-light variant could swap to a hand-rolled
// formatter without touching ir.go.
func fmtSprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
