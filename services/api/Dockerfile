FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gamics-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gamics-api /gamics-api
EXPOSE 8080
ENTRYPOINT ["/gamics-api"]
