# coderadar (Go SDK)

Go SDK for the [CodeRadar](https://coderadar.app) error monitoring service.

```bash
go get github.com/RelayOne/coderadar/sdks/go/coderadar
```

## Quick start

```go
package main

import (
    "context"
    "errors"
    "net/http"
    "os"

    "github.com/RelayOne/coderadar/sdks/go/coderadar"
)

func main() {
    client := coderadar.NewClient(
        os.Getenv("CODERADAR_API_KEY"),
        coderadar.DefaultEndpoint,
        coderadar.WithServiceName("payments-api"),
        coderadar.WithEnvironment("production"),
    )

    // Manual capture.
    if err := doWork(); err != nil {
        _ = client.CaptureError(context.Background(), err, coderadar.ErrorOpts{
            Tags:  map[string]string{"feature": "checkout"},
            User:  "user-123",
            Extra: map[string]any{"order_id": 42},
        })
    }

    // Auto-capture panics in any net/http handler chain.
    handler := client.WrapPanics(http.DefaultServeMux)
    _ = http.ListenAndServe(":8080", handler)
}

func doWork() error { return errors.New("boom") }
```

## OpenTelemetry bridge

`client.OTelExporter()` returns a `func(SpanLike) error` that converts any
finished span with a non-OK status into a CodeRadar error event. Wire it into
your existing OTel SDK with a thin shim that satisfies the `SpanLike`
interface — see the godoc on `OTelExporter`.

## Wire format

Events are POSTed as JSON to `${baseURL}/errors` with header
`x-coderadar-key: <api-key>`. The schema matches the canonical
`apps/ingest-api/src/schemas/error-event.ts`.

## Retries

Failed transports and 5xx / 429 responses are retried with exponential
backoff (default 3 retries, 200ms base). 4xx responses (other than 429) are
not retried. Context cancellation is honored at every wait point.
