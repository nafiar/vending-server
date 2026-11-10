# Convenience targets; every command also works without make (see README).
BIN := bin/vending-server

.PHONY: run build test test-db seed fmt vet windows contract generate

run:
	go run ./cmd/vending-server

build:
	go build -o $(BIN) ./cmd/vending-server

windows:
	GOOS=windows GOARCH=amd64 go build -o bin/vending-server.exe ./cmd/vending-server

seed:
	go run ./cmd/vending-server seed-demo

# Unit tests only (no database needed).
test:
	go test ./...

# Everything, including the MySQL integration tests (each test makes and drops its own database).
# Example: make test-db VENDING_TEST_DSN='root:secret@tcp(127.0.0.1:3306)/'
test-db:
	VENDING_TEST_DSN='$(VENDING_TEST_DSN)' go test -race -count=1 ./...

# Rewrite the API contract golden file after an intended contract change.
# Review the diff before committing it: it is what the admin web and the kiosk depend on.
contract:
	VENDING_TEST_DSN='$(VENDING_TEST_DSN)' go test ./internal/httpapi -run TestAPIContract -update -count=1

# Regenerate the port mocks (go.uber.org/mock).
generate:
	go generate ./...

fmt:
	gofmt -w .

vet:
	go vet ./...
