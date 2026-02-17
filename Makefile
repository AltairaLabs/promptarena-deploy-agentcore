.PHONY: fmt lint test build build-runtime check install-hooks docker-build

# Format code with goimports
fmt:
	GOWORK=off goimports -w -local github.com/AltairaLabs/promptarena-deploy-agentcore .

# Run golangci-lint
lint:
	GOWORK=off golangci-lint run ./...

# Run tests with race detector
test:
	GOWORK=off go test ./... -race -count=1

# Build binary
build:
	GOWORK=off go build -o promptarena-deploy-agentcore .

# Build runtime binary
build-runtime:
	GOWORK=off go build -o agentcore-runtime ./cmd/agentcore-runtime/

# Run all quality checks (what pre-commit runs)
check: fmt lint test build

# Build Docker image locally
docker-build:
	docker build -f Dockerfile.agentcore -t promptkit-agentcore:local .

# Install git hooks
install-hooks:
	git config core.hooksPath .githooks
