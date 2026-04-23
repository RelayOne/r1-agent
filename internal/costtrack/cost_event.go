// cost_event.go provides a CloudSwarm-compatible per-LLM-call cost event
// serializer. CloudSwarm's supervisor sidecar parses stdout lines of the
// shape:
//
//	{"event":"cost","model":"<name>","input_tokens":<int>,"output_tokens":<int>,"usd":<float>}
//
// with a trailing LF terminator (no CRLF). The helper below is the
// single blessed emitter: any provider response-handler that wants
// parity with CloudSwarm's llm_usage ingest must call it after usage
// is known.
//
// Spec reference: specs/work-stoke-alignment.md CS-1.

package costtrack

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
)

// costEventWriter is the sink for EmitCostEventToStdout. It defaults to
// os.Stdout but tests swap it via setCostEventWriter so stdout capture
// via a pipe isn't required (which is notoriously race-prone across
// goroutines). The mutex ensures concurrent emissions produce whole
// lines, never interleaved bytes.
var (
	costEventMu     sync.Mutex
	costEventWriter io.Writer = os.Stdout
)

// setCostEventWriter is test-only. Callers outside _test.go must not
// use it; the production sink is always os.Stdout.
func setCostEventWriter(w io.Writer) (restore func()) {
	costEventMu.Lock()
	defer costEventMu.Unlock()
	prev := costEventWriter
	costEventWriter = w
	return func() {
		costEventMu.Lock()
		costEventWriter = prev
		costEventMu.Unlock()
	}
}

// EmitCostEventToStdout writes the CloudSwarm-compatible cost event as a
// single NDJSON line to os.Stdout with an LF terminator. The output is
// byte-exact so CloudSwarm's parser regex matches without quirks:
//
//	^\{"event":"cost","model":"[^"]+","input_tokens":\d+,"output_tokens":\d+,"usd":[\d.]+\}$
//
// Negative token counts are clamped to 0 (the CloudSwarm regex is \d+).
// usd is rendered with strconv.FormatFloat 'f' / -1 precision so the
// shortest round-trippable form is used (e.g. "0.0123" not
// "1.23e-02"). A bare integer value of usd still renders as an integer
// literal (e.g. "0"), which the [\d.]+ regex accepts.
func EmitCostEventToStdout(model string, inputTokens, outputTokens int, usd float64) {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if usd < 0 {
		usd = 0
	}

	// JSON-escape the model string for the one field that can contain
	// user-controlled data. Everything else is numeric and safe.
	modelEsc := jsonEscapeString(model)

	line := fmt.Sprintf(
		`{"event":"cost","model":%s,"input_tokens":%d,"output_tokens":%d,"usd":%s}`+"\n",
		modelEsc,
		inputTokens,
		outputTokens,
		strconv.FormatFloat(usd, 'f', -1, 64),
	)

	costEventMu.Lock()
	defer costEventMu.Unlock()
	// Errors writing to stdout are intentionally dropped; cost
	// emission must never break an in-flight LLM call.
	_, _ = io.WriteString(costEventWriter, line)
}

// jsonEscapeString returns the JSON string literal for s (including the
// surrounding double quotes). We implement it inline to avoid an
// encoding/json allocation + map step on every LLM call.
func jsonEscapeString(s string) string {
	// Fast path: no characters that need escaping.
	needs := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' || c < 0x20 {
			needs = true
			break
		}
	}
	if !needs {
		return `"` + s + `"`
	}
	out := make([]byte, 0, len(s)+4)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if c < 0x20 {
				out = append(out, '\\', 'u', '0', '0',
					hexDigit(c>>4), hexDigit(c&0xF))
			} else {
				out = append(out, c)
			}
		}
	}
	out = append(out, '"')
	return string(out)
}

func hexDigit(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + (b - 10)
}
