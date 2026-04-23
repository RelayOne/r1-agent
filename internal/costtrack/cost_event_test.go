package costtrack

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// cloudSwarmRegex mirrors CloudSwarm's sidecar parser. Any change to
// the emitted shape that breaks this regex is a wire-format break.
// Spec: specs/work-stoke-alignment.md CS-1.
var cloudSwarmRegex = regexp.MustCompile(
	`^\{"event":"cost","model":"[^"]+","input_tokens":\d+,"output_tokens":\d+,"usd":[\d.]+\}$`,
)

// captureStdout swaps the package-internal writer for a buffer and
// returns the bytes written during fn. It is safe under t.Parallel()
// only if callers don't mark themselves parallel; the emitter uses a
// single shared sink so we keep these tests serial.
func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	var buf bytes.Buffer
	restore := setCostEventWriter(&buf)
	defer restore()
	fn()
	return buf.Bytes()
}

func TestCostEvent_ExactShape(t *testing.T) {
	out := captureStdout(t, func() {
		EmitCostEventToStdout("claude-opus-4", 1234, 567, 0.0891)
	})
	line := strings.TrimRight(string(out), "\n")
	if !cloudSwarmRegex.MatchString(line) {
		t.Fatalf("cost event does not match CloudSwarm regex: %q", line)
	}

	// Round-trip through encoding/json to confirm it's valid JSON and
	// all five fields are present with the correct types.
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("emitted line is not valid JSON: %v (%q)", err, line)
	}
	if got["event"] != "cost" {
		t.Errorf(`event = %v, want "cost"`, got["event"])
	}
	if got["model"] != "claude-opus-4" {
		t.Errorf(`model = %v, want "claude-opus-4"`, got["model"])
	}
	if v, _ := got["input_tokens"].(float64); v != 1234 {
		t.Errorf("input_tokens = %v, want 1234", got["input_tokens"])
	}
	if v, _ := got["output_tokens"].(float64); v != 567 {
		t.Errorf("output_tokens = %v, want 567", got["output_tokens"])
	}
	if v, _ := got["usd"].(float64); v != 0.0891 {
		t.Errorf("usd = %v, want 0.0891", got["usd"])
	}
}

func TestCostEvent_TrailingLF(t *testing.T) {
	out := captureStdout(t, func() {
		EmitCostEventToStdout("gpt-4o", 10, 20, 0.5)
	})
	if len(out) == 0 {
		t.Fatal("no bytes written")
	}
	if out[len(out)-1] != '\n' {
		t.Fatalf("last byte = %q, want LF", out[len(out)-1])
	}
	// Explicitly reject CRLF — CloudSwarm parses NDJSON and a stray CR
	// would end up inside the last field value.
	if bytes.Contains(out, []byte("\r\n")) {
		t.Fatalf("output contains CRLF, want LF only: %q", out)
	}
	if bytes.Contains(out, []byte{'\r'}) {
		t.Fatalf("output contains CR, want LF only: %q", out)
	}
	// Exactly one newline per emission.
	if n := bytes.Count(out, []byte{'\n'}); n != 1 {
		t.Fatalf("newline count = %d, want 1", n)
	}
}

func TestCostEvent_ZeroValues(t *testing.T) {
	out := captureStdout(t, func() {
		EmitCostEventToStdout("m", 0, 0, 0)
	})
	line := strings.TrimRight(string(out), "\n")
	if !cloudSwarmRegex.MatchString(line) {
		t.Fatalf("zero-value cost event does not match regex: %q", line)
	}
}

func TestCostEvent_NegativeClampedToZero(t *testing.T) {
	// CloudSwarm regex is \d+ (unsigned); negatives must not leak.
	out := captureStdout(t, func() {
		EmitCostEventToStdout("m", -5, -7, -0.1)
	})
	line := strings.TrimRight(string(out), "\n")
	if !cloudSwarmRegex.MatchString(line) {
		t.Fatalf("negative values leaked past clamp: %q", line)
	}
}

func TestCostEvent_ModelWithQuotesEscaped(t *testing.T) {
	// Defensive: if a misconfigured model name contains a double
	// quote it must be escaped so the line remains valid JSON. The
	// CloudSwarm regex [^"]+ will fail, which is correct — but
	// downstream json.Unmarshal must still succeed.
	out := captureStdout(t, func() {
		EmitCostEventToStdout(`bad"name`, 1, 2, 3.0)
	})
	line := strings.TrimRight(string(out), "\n")
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("escaped model produced invalid JSON: %v (%q)", err, line)
	}
	if got["model"] != `bad"name` {
		t.Errorf("model round-trip mismatch: %v", got["model"])
	}
}

func TestCostEvent_ConcurrentEmitsAreLineAtomic(t *testing.T) {
	// Interleaved writes from multiple goroutines must never produce a
	// partial line; each emission is a whole NDJSON record.
	var buf bytes.Buffer
	restore := setCostEventWriter(&buf)
	defer restore()

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			EmitCostEventToStdout("m", i, i*2, float64(i)/10)
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("line count = %d, want %d", len(lines), n)
	}
	for _, line := range lines {
		if !cloudSwarmRegex.MatchString(line) {
			t.Fatalf("interleaved line broke regex: %q", line)
		}
	}
}

func TestCostEvent_JSONEscapeHelper(t *testing.T) {
	cases := map[string]string{
		"":       `""`,
		"plain":  `"plain"`,
		`a"b`:    `"a\"b"`,
		`a\b`:    `"a\\b"`,
		"a\nb":   `"a\nb"`,
		"a\tb":   `"a\tb"`,
		"a\x01b":   "\"a\\u0001b\"",
	}
	for in, want := range cases {
		if got := jsonEscapeString(in); got != want {
			t.Errorf("jsonEscapeString(%q) = %q, want %q", in, got, want)
		}
	}
}
