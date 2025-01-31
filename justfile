lint:
    golangci-lint run

hub:
    go run ./cmd/proxyhub

node:
    go run ./cmd/proxynode

proxies:
    curl -v -H "Authorization: Bearer $PROXYHUB_TOKEN" "$PROXYHUB_ENDPOINT/api/proxies"

proxies-live:
    curl -v -H "Authorization: Bearer $PROXYHUB_TOKEN" "$PROXYHUB_ENDPOINT/api/proxies/live"
