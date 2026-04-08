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
    -o /out/stoke \
    ./cmd/stoke

# Final stage: distroless with glibc (needed for CGO/SQLite)
FROM gcr.io/distroless/base-debian12:nonroot

COPY --from=builder /out/stoke /usr/local/bin/stoke

ENTRYPOINT ["stoke"]
