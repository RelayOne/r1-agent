# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.23-bookworm AS builder

ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/r1 \
    ./cmd/r1

# Final stage: distroless with glibc (needed for CGO/SQLite)
FROM gcr.io/distroless/base-debian12:nonroot

COPY --from=builder /out/r1 /usr/local/bin/r1

ENTRYPOINT ["r1"]
