// daemon_http.go — tiny HTTP client used by the `stoke daemon` client
// subcommands to talk to a running daemon. Kept separate from daemon_cmd.go
// so the dispatcher file stays readable.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func daemonHTTP(method, addr, token, path string, body any) (string, error) {
	url := fmt.Sprintf("http://%s%s", addr, path)
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return "", fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("daemon at %s unreachable: %w", addr, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("daemon %d: %s", resp.StatusCode, string(out))
	}
	// Pretty-print JSON if possible.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err == nil {
		return pretty.String(), nil
	}
	return string(out), nil
}
