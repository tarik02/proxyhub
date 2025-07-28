lint:
    golangci-lint run

gen:
    find ./pb -iname "*.pb.go" -delete
    protoc -I=. \
        --go_out=. \
        --go_opt=paths=source_relative \
        --go-grpc_out=. \
        --go-grpc_opt=paths=source_relative \
        $(find ./pb -iname "*.proto")
    go generate ./...

hub:
    go run ./cmd/proxyhub

node:
    go run ./cmd/proxynode

proxies:
    curl -v -H "Authorization: Bearer $PROXYHUB_TOKEN" "$PROXYHUB_ENDPOINT/api/proxies"

proxies-live:
    curl -v -H "Authorization: Bearer $PROXYHUB_TOKEN" "$PROXYHUB_ENDPOINT/api/proxies/live"

client *args:
    go run ./cmd/proxyclient \
        --endpoint "$PROXYHUB_ENDPOINT" \
        --token "$PROXYHUB_TOKEN" \
        {{ args }}

build-snapshot:
    goreleaser --clean --snapshot

test-leaks:
     go test -v -count=1 ./cmd/proxyhub
