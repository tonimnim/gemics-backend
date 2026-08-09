# syntax=docker/dockerfile:1.7

FROM golang:1.26-alpine AS build
WORKDIR /src
ARG VERSION=dev

COPY services/api/go.mod services/api/go.sum ./
RUN go mod download
COPY services/api/ ./

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/gamics-api ./cmd/api

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S gamics \
    && adduser -S -G gamics gamics

COPY --from=build /out/gamics-api /usr/local/bin/gamics-api

USER gamics
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -q -O - http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/gamics-api"]
