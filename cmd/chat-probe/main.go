// chat-probe is a small integration harness that drives stoke's chat
// session against the live LiteLLM proxy and asserts that a "build the
// SOW at <path>" message produces a dispatch_sow tool call. It is NOT
// part of the regular CI gate — it makes real LLM calls, costs money,
// and depends on a running LiteLLM. Run it manually with:
//
//	go run ./cmd/chat-probe -file /tmp/sentinel-clients/SOW_WEB_MOBILE.md
//
// Exit codes:
//   0  — chat dispatched dispatch_sow with the expected file path
//   1  — wrong tool dispatched, no dispatch, or provider error
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/RelayOne/r1/internal/chat"
	"github.com/RelayOne/r1/internal/litellm"
)

// recordingDispatcher captures which dispatcher method the chat session
// invoked. SOW is the only one we care about for this probe; the rest
// satisfy the interface and emit a clear failure if hit.
type recordingDispatcher struct {
	calledMethod string
	calledFile   string
	calledDesc   string
}

func (d *recordingDispatcher) Scope(desc string) (string, error) {
	d.calledMethod = "Scope"
	d.calledDesc = desc
	return "scope dispatched (probe)", nil
}
func (d *recordingDispatcher) Build(desc string) (string, error) {
	d.calledMethod = "Build"
	d.calledDesc = desc
	return "build dispatched (probe)", nil
}
func (d *recordingDispatcher) Ship(desc string) (string, error) {
	d.calledMethod = "Ship"
	d.calledDesc = desc
	return "ship dispatched (probe)", nil
}
func (d *recordingDispatcher) Plan(desc string) (string, error) {
	d.calledMethod = "Plan"
	d.calledDesc = desc
	return "plan dispatched (probe)", nil
}
func (d *recordingDispatcher) Audit() (string, error) {
	d.calledMethod = "Audit"
	return "audit dispatched (probe)", nil
}
func (d *recordingDispatcher) Scan(_ bool) (string, error) {
	d.calledMethod = "Scan"
	return "scan dispatched (probe)", nil
}
func (d *recordingDispatcher) Status() (string, error) {
	d.calledMethod = "Status"
	return "status dispatched (probe)", nil
}
func (d *recordingDispatcher) SOW(filePath string) (string, error) {
	d.calledMethod = "SOW"
	d.calledFile = filePath
	return "sow dispatched (probe) — pipeline NOT actually run", nil
}

func main() {
	sowPath := flag.String("file", "/tmp/sentinel-clients/SOW_WEB_MOBILE.md", "SOW file path the model should dispatch")
	model := flag.String("model", "claude-sonnet-4-6", "model name")
	flag.Parse()

	disc := litellm.Discover()
	if disc == nil {
		fmt.Fprintln(os.Stderr, "FAIL: no LiteLLM proxy auto-discovered")
		os.Exit(1)
	}
	fmt.Printf("LiteLLM: %s (%d models)\n", disc.BaseURL, len(disc.Models))

	prov, err := chat.NewProviderFromOptions(chat.ProviderOptions{
		BaseURL: disc.BaseURL,
		APIKey:  disc.APIKey,
		Model:   *model,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL: build provider:", err)
		os.Exit(1)
	}

	session, err := chat.NewSession(prov, chat.Config{
		Model:     *model,
		MaxTokens: 4096,
		Tools:     chat.DispatcherTools(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL: build session:", err)
		os.Exit(1)
	}

	disp := &recordingDispatcher{}
	dispatched := false

	onDelta := func(delta string) {
		fmt.Print(delta)
	}
	onDispatch := func(_ context.Context, name string, input json.RawMessage) (string, error) {
		fmt.Printf("\n\n[probe] tool=%s input=%s\n", name, string(input))
		dispatched = true
		return chat.RunToolCall(disp, name, input)
	}

	// One-shot directive: tells the model the file path explicitly so
	// the only ambiguity left is which tool to pick.
	prompt := fmt.Sprintf("Build the Statement of Work at %s through the SOW pipeline. Just dispatch it — no clarifying questions, the file is ready.", *sowPath)
	fmt.Printf("\n[probe] sending: %q\n\n", prompt)

	ctx := context.Background()
	result, err := session.Send(ctx, prompt, onDelta, onDispatch)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nFAIL: session.Send:", err)
		os.Exit(1)
	}

	fmt.Printf("\n\n[probe] reply text length: %d\n", len(result.Text))

	if !dispatched {
		fmt.Fprintln(os.Stderr, "FAIL: model did not dispatch any tool")
		os.Exit(1)
	}
	if disp.calledMethod != "SOW" {
		fmt.Fprintf(os.Stderr, "FAIL: model dispatched %s, expected SOW\n", disp.calledMethod)
		os.Exit(1)
	}
	if disp.calledFile != *sowPath {
		fmt.Fprintf(os.Stderr, "FAIL: dispatched with file=%q, expected %q\n", disp.calledFile, *sowPath)
		os.Exit(1)
	}
	fmt.Println("\nPASS: chat → dispatch_sow with expected file path")
}
