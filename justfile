lint:
    golangci-lint run

gen:
    protoc -I=. --go_out=. --go_opt=paths=source_relative ./**/*.proto
    go generate ./...

hub:
    go run ./cmd/proxyhub

node:
    go run ./cmd/proxynode

proxies:
    curl -v -H "Authorization: Bearer $PROXYHUB_TOKEN" "$PROXYHUB_ENDPOINT/api/proxies"

proxies-live:
    curl -v -H "Authorization: Bearer $PROXYHUB_TOKEN" "$PROXYHUB_ENDPOINT/api/proxies/live"
