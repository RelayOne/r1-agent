The file `main.go` contains a simple HTTP server with a `/api/users` endpoint. Add a new `/health` endpoint that:

1. Responds to GET requests with HTTP 200
2. Sets the Content-Type header to `application/json`
3. Returns the JSON body: `{"status":"ok"}`

Do not modify the existing `/api/users` endpoint or its behavior.
