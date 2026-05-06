# heroa Go SDK

Heroa managed-runtime SDK for Go. Idiomatic `context.Context`, typed
`*HeroaError` values, stable canonical JSON for idempotency-key derivation.

## Install

```sh
go get heroa.dev/sdk-go
```

## Usage

```go
package main

import (
    "context"
    "fmt"
    "os"

    heroa "heroa.dev/sdk-go"
)

func main() {
    c, err := heroa.New(heroa.Config{
        APIKey:  os.Getenv("HEROA_API_KEY"),
        BaseURL: os.Getenv("HEROA_BASE_URL"), // optional; defaults to https://api.heroa.dev
    })
    if err != nil {
        panic(err)
    }

    inst, err := c.Deploy(context.Background(), heroa.DeployRequest{
        Template: "next-ssr",
        Region:   "us-east",
        AppName:  "demo-preview",
        Size:     "small",
        TTL:      "1h",
        Metadata: map[string]string{"agent_id": "r1-abc123"},
        Lifecycle: heroa.Hooks{
            OnReady: func(i *heroa.Instance) { fmt.Println("live at", i.URL) },
            OnError: func(err error) { fmt.Println("deploy error:", err) },
        },
    })
    if err != nil {
        // Typed error branching:
        switch {
        case heroa.IsRegionNotAllowed(err):
            // retry with allowed region
        case heroa.IsAuth(err):
            // rotate key
        case heroa.IsQuotaExceeded(err):
            // upgrade plan
        }
        panic(err)
    }

    fmt.Println(inst.ID)        // m-h3roa-x4n7
    fmt.Println(inst.URL)       // https://m-h3roa-x4n7.heroa.dev
    fmt.Println(inst.ExpiresAt) // 2026-04-24T13:00:00Z
}
```

## Typed errors

`*HeroaError` values carry `Code` (one of the ten `ErrCode*` constants),
`Status`, `Message`, and `RequestID`. Helpers like `IsAuth(err)`,
`IsRegionNotAllowed(err)`, `IsQuotaExceeded(err)` provide a narrow
type-switch API for the common branches.

## Idempotency

`Deploy` auto-derives an `Idempotency-Key` header from
`sha256(canonicalized(request))`. Override via
`DeployRequest.IdempotencyKey`.

## Coming in H4-2..H4-4

- WebSocket back-channel for `Instance.Stream()` + `OnLog` hook.
- `Instance` methods: `Extend`, `Destroy`, `Status`, `Logs`, `Exec`.
