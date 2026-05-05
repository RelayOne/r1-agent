# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go main_test.go ./
COPY docs ./docs
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X main.versionStr=${VERSION}" \
    -o /out/r1-docs .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/r1-docs /usr/local/bin/r1-docs
USER nonroot:nonroot
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/r1-docs"]
