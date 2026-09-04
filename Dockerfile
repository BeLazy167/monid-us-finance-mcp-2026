# Build the single static server binary.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go/go.mod ./go/go.mod
WORKDIR /src/go
RUN go mod download
COPY go/ ./
# CGO off gives a fully static binary; trimpath and -s -w keep it small.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# Runtime image carries the binary, the static site and the endpoint allowlist.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
COPY website/ /app/website/
COPY docs/monid_finance_discovery.json /app/docs/monid_finance_discovery.json

ENV PORT=8080 \
    STATIC_DIR=/app/website \
    ALLOWLIST_PATH=/app/docs/monid_finance_discovery.json
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
