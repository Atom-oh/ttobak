# Go backend

Follow the root guide. `cmd/` has eight zip Lambda entry points, a separate
`convert-doc` container, and maintenance commands; do not treat every directory
as a zip deployment. GatewayStack defines artifact packaging.

HTTP handlers call services, which call repositories. Model files define keys
and request/response contracts. Use Go's DynamoDB expression builder, conditional
updates for shared fields, transactions for relation sets/reverse refs, pagination,
and sentinel errors checked through `errors.Is`.

Go chi integration requires API Gateway payload 1.0. Python QA is separate and uses
2.0. Existing OriginVerify, verified JWT parsing, ownership checks and service
permissions must be inspected before alleging an authorization bypass.

```bash
/usr/local/go/bin/go test ./... -count=1
/usr/local/go/bin/go vet ./...
GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```

Use stdlib testing and mock repositories. Tests in `cmd/summarize` and
`cmd/transcribe` are required; `./internal/...` alone is incomplete.
