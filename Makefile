.PHONY: fmt lint test build build-runtime build-runtime-arm64 check install-hooks docker-build

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

# Cross-compile runtime binary for Linux ARM64 (AgentCore code deploy)
build-runtime-arm64:
	GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o promptkit-runtime ./cmd/agentcore-runtime/

# Run all quality checks (what pre-commit runs)
check: fmt lint test build

# Build Docker image locally
docker-build:
	docker build -f Dockerfile.agentcore -t promptkit-agentcore:local .

# Install git hooks
install-hooks:
	git config core.hooksPath .githooks
