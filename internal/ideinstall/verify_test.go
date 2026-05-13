package ideinstall

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintVerifyTable_Format(t *testing.T) {
	rows := []VerifyResult{
		{IDE: "cursor", Path: "/home/u/.cursor/mcp.json", Status: VerifyStatusRegistered},
		{IDE: "windsurf", Path: "/home/u/.codeium/windsurf/mcp_config.json", Status: VerifyStatusNotFound},
		{IDE: "vscode", Path: "/home/u/.config/Code/User/mcp.json", Status: VerifyStatusUnregistered},
		{IDE: "jetbrains", Path: "/home/u/.local/share/JetBrains/IntelliJIdea2026.2/plugins/", Status: VerifyStatusRegistered},
	}
	var buf bytes.Buffer
	PrintVerifyTable(&buf, rows)

	// Per spec §T8.2 each line must use the exact padded shape so
	// pipes align across rows. Assert byte-for-byte against a
	// golden table.
	want := strings.Join([]string{
		"cursor    | registered   | /home/u/.cursor/mcp.json",
		"windsurf  | not-found    | /home/u/.codeium/windsurf/mcp_config.json",
		"vscode    | unregistered | /home/u/.config/Code/User/mcp.json",
		"jetbrains | registered   | /home/u/.local/share/JetBrains/IntelliJIdea2026.2/plugins/",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Fatalf("table mismatch:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		found, registered bool
		want              VerifyStatus
	}{
		{true, true, VerifyStatusRegistered},
		{true, false, VerifyStatusUnregistered},
		{false, false, VerifyStatusNotFound},
		{false, true, VerifyStatusRegistered}, // jar present even without IDE dir
	}
	for _, c := range cases {
		if got := classifyStatus(c.found, c.registered); got != c.want {
			t.Fatalf("classifyStatus(%v,%v) = %q, want %q",
				c.found, c.registered, got, c.want)
		}
	}
}
