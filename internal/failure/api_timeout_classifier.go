package failure

import (
	"strings"
	"time"
)

// APIFailureClass distinguishes transient provider/network faults from permanent request failures.
type APIFailureClass string

const (
	APIFailureUnknown          APIFailureClass = "unknown"
	APIFailureTransientTimeout APIFailureClass = "transient_timeout"
	APIFailureTransientNetwork APIFailureClass = "transient_network"
	APIFailureTransientRate    APIFailureClass = "transient_rate_limit"
	APIFailurePermanentAuth    APIFailureClass = "permanent_auth"
	APIFailurePermanentRequest APIFailureClass = "permanent_request"
)

// APIFailureDecision is the retry/backoff decision for a provider error.
type APIFailureDecision struct {
	Class     APIFailureClass
	Retryable bool
	Backoff   time.Duration
	Reason    string
}

// ClassifyAPIFailure classifies common Codex/OpenAI timeout and transport errors.
func ClassifyAPIFailure(err error, attempt int) APIFailureDecision {
	if err == nil {
		return APIFailureDecision{}
	}
	if attempt < 1 {
		attempt = 1
	}

	msg := strings.ToLower(err.Error())
	switch {
	case containsAny(msg,
		"invalid api key",
		"incorrect api key",
		"unauthorized",
		"permission denied",
		"forbidden",
		"insufficient permissions"):
		return APIFailureDecision{
			Class:     APIFailurePermanentAuth,
			Retryable: false,
			Reason:    "provider authentication/authorization failure",
		}
	case containsAny(msg,
		"context length exceeded",
		"maximum context length",
		"model not found",
		"unsupported value",
		"invalid_request_error",
		"malformed input",
		"prompt is too long"):
		return APIFailureDecision{
			Class:     APIFailurePermanentRequest,
			Retryable: false,
			Reason:    "provider rejected the request permanently",
		}
	case containsAny(msg,
		"rate limit",
		"too many requests",
		"429",
		"retry-after",
		"server is overloaded"):
		return APIFailureDecision{
			Class:     APIFailureTransientRate,
			Retryable: true,
			Backoff:   steppedBackoff(attempt, 5*time.Second, 45*time.Second),
			Reason:    "provider rate limit or overload",
		}
	case containsAny(msg,
		"context deadline exceeded",
		"deadline exceeded",
		"request timed out",
		"timed out",
		"timeout awaiting response headers",
		"504 gateway timeout"):
		return APIFailureDecision{
			Class:     APIFailureTransientTimeout,
			Retryable: true,
			Backoff:   steppedBackoff(attempt, 2*time.Second, 30*time.Second),
			Reason:    "provider timeout",
		}
	case containsAny(msg,
		"connection reset by peer",
		"connection refused",
		"eof",
		"transport is closing",
		"tls handshake timeout",
		"temporary network failure",
		"502 bad gateway",
		"503 service unavailable"):
		return APIFailureDecision{
			Class:     APIFailureTransientNetwork,
			Retryable: true,
			Backoff:   steppedBackoff(attempt, 3*time.Second, 30*time.Second),
			Reason:    "transient network/provider transport failure",
		}
	default:
		return APIFailureDecision{
			Class:     APIFailureUnknown,
			Retryable: false,
			Reason:    "error does not match provider timeout classifier",
		}
	}
}

func steppedBackoff(attempt int, base, max time.Duration) time.Duration {
	delays := []time.Duration{base, base * 2, base * 4}
	if attempt <= len(delays) {
		if delays[attempt-1] > max {
			return max
		}
		return delays[attempt-1]
	}
	return max
}

func containsAny(msg string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
