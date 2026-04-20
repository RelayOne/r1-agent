# R04-go — chi router hello endpoint

Small Go HTTP service with `GET /hello/{name}` using chi router and
`httptest` for integration testing.

## Scope

Module `github.com/example/hello-api`.

`main.go`:
```go
package main

import (
    "encoding/json"
    "net/http"
    "github.com/go-chi/chi/v5"
)

func NewRouter() http.Handler {
    r := chi.NewRouter()
    r.Get("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
        name := chi.URLParam(req, "name")
        json.NewEncoder(w).Encode(map[string]string{"greeting": "Hello, " + name + "!"})
    })
    return r
}

func main() {
    http.ListenAndServe(":8080", NewRouter())
}
```

`main_test.go` uses `httptest.NewRecorder()` + `NewRouter()` to
assert the JSON response for `/hello/World`.

## Acceptance

- `go.mod` lists `github.com/go-chi/chi/v5`.
- `go build ./...` exits 0.
- `go test ./...` exits 0; at least one test asserts
  `greeting == "Hello, World!"`.
- `go vet ./...` exits 0.

## What NOT to do

- No database, no auth.
- No middleware beyond chi defaults.
- No more routes than `/hello/{name}`.
