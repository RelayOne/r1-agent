// Panic-recovery and adapter helpers for popular Go entry points.

package coderadar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// WrapPanics returns an http.Handler that recovers from panics in next, ships
// them to CodeRadar as captured errors, and writes a 500 to the client. The
// original panic value is wrapped in an error if it isn't already one.
func (c *Client) WrapPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			err := panicToError(rec)
			// Ship to CodeRadar on a detached context so client-aborted requests
			// still get reported.
			ctx := context.WithoutCancel(r.Context())
			_ = c.CaptureError(ctx, err, ErrorOpts{
				Tags: map[string]string{
					"http.method": r.Method,
					"http.path":   r.URL.Path,
				},
			})
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

func panicToError(rec any) error {
	switch v := rec.(type) {
	case error:
		return v
	case string:
		return errors.New(v)
	default:
		return fmt.Errorf("panic: %v", v)
	}
}

// SpanLike is the minimum surface a captured OpenTelemetry span must expose
// for OTelExporter to convert it. It mirrors otel/sdk/trace.ReadOnlySpan
// without taking a hard dependency on the OTel SDK.
type SpanLike interface {
	Name() string
	Status() (code int, description string)
	Attributes() map[string]any
}

// OTelExporter returns a function that bridges OpenTelemetry spans into
// CodeRadar ingest. Spans whose status code is non-OK (>0) are converted to
// captured errors.
//
// Example wiring against go.opentelemetry.io/otel/sdk/trace.SpanExporter:
//
//	type cwAdapter struct{ exp func(SpanLike) error }
//	func (a *cwAdapter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
//	    for _, s := range spans {
//	        a.exp(otelSpanShim{s})
//	    }
//	    return nil
//	}
//
// The shim type translates from sdktrace.ReadOnlySpan to SpanLike.
func (c *Client) OTelExporter() func(span SpanLike) error {
	return func(span SpanLike) error {
		if span == nil {
			return nil
		}
		code, desc := span.Status()
		if code <= 0 {
			return nil
		}
		msg := desc
		if msg == "" {
			msg = span.Name()
		}
		err := errors.New(msg)
		extra := map[string]any{"otel.span.name": span.Name()}
		for k, v := range span.Attributes() {
			extra[k] = v
		}
		return c.CaptureError(context.Background(), err, ErrorOpts{
			Tags:  map[string]string{"source": "otel"},
			Extra: extra,
		})
	}
}
