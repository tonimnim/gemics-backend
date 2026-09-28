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

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/gamics-evidence-worker ./cmd/evidence-worker

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/gamics-migrate ./cmd/migrate

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S gamics \
    && adduser -S -G gamics gamics

COPY --from=build /out/gamics-api /usr/local/bin/gamics-api
COPY --from=build /out/gamics-evidence-worker /usr/local/bin/gamics-evidence-worker
COPY --from=build /out/gamics-migrate /usr/local/bin/gamics-migrate

USER gamics
EXPOSE 8080
# One image runs every binary. The evidence worker serves /healthz on its own
# port, so each deployment sets HEALTHCHECK_PORT for the process it runs; the
# shell form expands it when the check runs.
ENV HEALTHCHECK_PORT=8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${HEALTHCHECK_PORT}/healthz" || exit 1

ENTRYPOINT ["/usr/local/bin/gamics-api"]
