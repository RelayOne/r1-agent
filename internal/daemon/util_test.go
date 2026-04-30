package daemon

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return v
}

func drainN(ch <-chan struct{}, n int) {
	for i := 0; i < n; i++ {
		<-ch
	}
}
