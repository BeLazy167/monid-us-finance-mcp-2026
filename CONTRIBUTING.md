# Contributing

The server is one Go binary with no third-party dependencies. There is no
`go.sum`, no lockfile, and nothing to install beyond Go 1.22 or newer.

```bash
make test     # full suite under the race detector
make vet      # gofmt check plus go vet
make run      # serve locally on :8080
make help     # every target
```

Send your own Monid key with each request; the server holds no secret of its own.

```bash
curl -H 'X-API-KEY: <your-monid-key>' \
  'http://localhost:8080/financials/income-statements?ticker=AAPL&period=annual&limit=1'
```

## The rule that shapes every change

This server exists so a Financial Datasets client can change only its base URL
and keep working. Two consequences:

- **Follow the live Financial Datasets API, not its published spec, when they
  disagree.** A switching client reads the live one. Where the two differ, say
  so in `docs/openapi-notes.md`.
- **Never fabricate a field.** If a provider cannot source it, omit the key and
  record the gap in `docs/compatibility.md` and `docs-site/overview/coverage.mdx`.
  A missing key is schema-legal. A guessed number is a lie a caller will trade on.

Put anything Financial Datasets does not have on its own route, with its own
documentation. Do not change a Financial Datasets response shape to fit it.

## Adding a route

1. Implement the tool in `go/service/`, and register it in the `toolHandlers`
   table in `go/service/tools.go`. A method that is not a Financial Datasets
   tool goes in `capabilities.go` and the `capabilityHandlers` table instead.
2. Register the REST path in `restRoutes` in `go/httpapi/rest.go`.
3. Raise the `wiredCapabilities` count in `go/cmd/server/main_test.go`. It is a
   ratchet: it fails when a capability is added without being wired.
4. Add the path to `docs/openapi.json` and copy it to
   `docs-site/api-reference/openapi.json`. The two must stay byte-identical.
5. Validate the docs with `mint broken-links` from `docs-site/`.

## Tests

Table-driven, with the race detector on. Every parser has fixtures captured from
the real upstream page or feed, in `go/service/testdata/`. When an upstream
changes shape, add its new output as a fixture rather than loosening an
assertion — the fixture is the evidence that the parser reads what the source
actually publishes.

## Style

`gofmt` decides formatting. Comments explain why a thing is the way it is,
especially where a provider is wrong or a shape is surprising; the code already
shows what it does.
