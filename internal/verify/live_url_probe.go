package verify

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Result captures the outcome of a live URL verification probe.
type Result struct {
	URL                string
	StatusCode         int
	Body               string
	Success            bool
	SideEffectVerified bool
}

// LiveURLProbe verifies a live deployment by hitting an HTTP endpoint with curl.
type LiveURLProbe struct {
	URL            string
	ExpectedStatus string
	BodyContains   string

	checkSideEffect func(context.Context) error
	curlBin         string
}

// SetSideEffectCheck installs an optional post-probe assertion such as a DB query.
func (p *LiveURLProbe) SetSideEffectCheck(fn func(context.Context) error) {
	p.checkSideEffect = fn
}

// Run performs the live probe and validates the response.
func (p LiveURLProbe) Run(ctx context.Context) (Result, error) {
	if strings.TrimSpace(p.URL) == "" {
		return Result{}, fmt.Errorf("verify.LiveURLProbe: url is required")
	}

	cmd := exec.CommandContext(ctx, p.curlPath(),
		"--silent",
		"--show-error",
		"--location",
		"--max-time", "30",
		"--write-out", "\n%{http_code}",
		p.URL,
	) // #nosec G204 -- binary name is fixed; URL is the configured probe target.

	out, err := cmd.CombinedOutput()
	if err != nil {
		return Result{}, fmt.Errorf("verify.LiveURLProbe: curl %s: %w", p.URL, err)
	}

	result, err := parseProbeOutput(p.URL, string(out))
	if err != nil {
		return Result{}, err
	}
	if !matchesExpectedStatus(result.StatusCode, p.ExpectedStatus) {
		return result, fmt.Errorf("verify.LiveURLProbe: status %d did not match %q", result.StatusCode, defaultExpectedStatus(p.ExpectedStatus))
	}
	if p.BodyContains != "" && !strings.Contains(result.Body, p.BodyContains) {
		return result, fmt.Errorf("verify.LiveURLProbe: response body missing %q", p.BodyContains)
	}
	if p.checkSideEffect != nil {
		if err := p.checkSideEffect(ctx); err != nil {
			return result, fmt.Errorf("verify.LiveURLProbe: side-effect check failed: %w", err)
		}
		result.SideEffectVerified = true
	}
	result.Success = true
	return result, nil
}

func (p LiveURLProbe) curlPath() string {
	if strings.TrimSpace(p.curlBin) != "" {
		return p.curlBin
	}
	return "curl"
}

func parseProbeOutput(rawURL, output string) (Result, error) {
	trimmed := strings.TrimRight(output, "\n")
	idx := strings.LastIndex(trimmed, "\n")
	if idx == -1 {
		return Result{}, fmt.Errorf("verify.LiveURLProbe: malformed curl output for %s", rawURL)
	}
	code, err := strconv.Atoi(strings.TrimSpace(trimmed[idx+1:]))
	if err != nil {
		return Result{}, fmt.Errorf("verify.LiveURLProbe: parse status code: %w", err)
	}
	return Result{
		URL:        rawURL,
		StatusCode: code,
		Body:       trimmed[:idx],
	}, nil
}

func matchesExpectedStatus(status int, expected string) bool {
	expected = defaultExpectedStatus(expected)
	if strings.HasSuffix(expected, "xx") && len(expected) == 3 {
		hundreds, err := strconv.Atoi(expected[:1])
		return err == nil && status/100 == hundreds
	}
	want, err := strconv.Atoi(expected)
	return err == nil && status == want
}

func defaultExpectedStatus(expected string) string {
	if strings.TrimSpace(expected) == "" {
		return "2xx"
	}
	return strings.TrimSpace(strings.ToLower(expected))
}
