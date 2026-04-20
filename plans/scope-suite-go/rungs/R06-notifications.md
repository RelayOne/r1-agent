# R06-go — Notification preferences API

chi router with GET/PUT /prefs/{userID} backed by in-memory store with
validation. Exercises request body parsing, state, and json response
shapes.

## Scope

Module `github.com/example/notify-api`. In-memory store via
`sync.RWMutex`-guarded `map[string]NotificationPrefs`.

Model:
```go
type NotificationPrefs struct {
    Email       bool            `json:"email"`
    SMS         bool            `json:"sms"`
    Push        bool            `json:"push"`
    Digest      string          `json:"digest"`       // "off" | "daily" | "weekly"
    QuietHours  *QuietHours     `json:"quiet_hours,omitempty"`
}
type QuietHours struct { Start, End string }  // "HH:MM"
```

Endpoints:
- `GET /prefs/{userID}` → 200 prefs or 404 if unknown.
- `PUT /prefs/{userID}` → 200 echo merged prefs, 400 on invalid.

Validation: digest must be one of "off"/"daily"/"weekly"; quiet_hours
entries match `^\d\d:\d\d$`. Invalid → 400 with `{"error": "<reason>"}`.

## Acceptance

- `go.mod` lists `github.com/go-chi/chi/v5`.
- At least 4 tests using `httptest` cover: GET unknown → 404, PUT
  valid → echoed prefs, PUT invalid digest → 400, GET after PUT
  returns stored value.
- `go build ./...` + `go test ./...` + `go vet ./...` all exit 0.

## What NOT to do

- No database, no auth, no listing endpoints.
- No concurrent-safety tests beyond the mutex being correctly used.
