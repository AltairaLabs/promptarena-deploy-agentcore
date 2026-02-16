# Contributing to promptarena-deploy-agentcore

Thank you for your interest in contributing to the AWS Bedrock AgentCore deploy adapter for PromptKit. This document provides guidelines for contributing to this project.

## Code of Conduct

This project adheres to the [Contributor Covenant Code of Conduct](./CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code. Please report unacceptable behavior to [conduct@altairalabs.ai](mailto:conduct@altairalabs.ai).

## Developer Certificate of Origin (DCO)

This project uses the Developer Certificate of Origin (DCO) to ensure that contributors have the right to submit their contributions. By making a contribution, you certify that:

1. The contribution was created in whole or in part by you and you have the right to submit it under the open source license indicated in the file; or
2. The contribution is based upon previous work that, to the best of your knowledge, is covered under an appropriate open source license and you have the right under that license to submit that work with modifications; or
3. The contribution was provided directly to you by some other person who certified (1), (2) or (3) and you have not modified it.

### Signing Your Commits

Add the `-s` flag to your git commit command:

```bash
git commit -s -m "Your commit message"
```

This adds a "Signed-off-by" line to your commit message:

```
Signed-off-by: Your Name <your.email@example.com>
```

## How to Contribute

### Reporting Bugs

- Check existing issues first
- Include the adapter version (`promptarena-deploy-agentcore --version`)
- Provide clear reproduction steps
- Share relevant configuration (redact any AWS credentials or account IDs)

### Suggesting Features

- Open an issue describing the feature
- Explain the use case and how it relates to Bedrock AgentCore or PromptKit deploy workflows

### Submitting Changes

1. **Fork the repository**
2. **Create a feature branch**: `git checkout -b feature/your-feature-name`
3. **Make your changes**
4. **Write or update tests**
5. **Run tests**: `go test ./... -v -race -count=1`
6. **Run linter**: `golangci-lint run`
7. **Commit with sign-off**: `git commit -s -m "Your commit message"`
8. **Push to your fork**: `git push origin feature/your-feature-name`
9. **Open a Pull Request**

## Development Setup

### Prerequisites

- Go 1.25 or later

### Setup

```bash
# Clone your fork
git clone https://github.com/YOUR_USERNAME/promptarena-deploy-agentcore.git
cd promptarena-deploy-agentcore

# Build
go build -o promptarena-deploy-agentcore .

# Run tests
go test ./... -v -race -count=1
```

### Project Structure

This is a flat, single-module Go binary. There are no sub-packages.

```
promptarena-deploy-agentcore/
├── main.go          # Entrypoint — calls adaptersdk.Serve()
├── version.go       # Version info embedded at build time
├── config.go        # Configuration parsing and validation
├── provider.go      # deploy.Provider interface implementation
├── plan.go          # Plan phase — preview deployment changes
├── apply.go         # Apply phase — execute deployment to AgentCore
├── status.go        # Status phase — query deployment state
├── *_test.go        # Tests alongside each source file
├── go.mod           # Module definition
└── LICENSE          # MIT license
```

This adapter implements PromptKit's `deploy.Provider` interface and is served via `adaptersdk.Serve()`.

## Coding Guidelines

### Go Code Style

- Follow standard Go conventions
- Use `gofmt` for formatting
- Write clear, descriptive variable and function names
- Keep functions focused and testable

### Testing

- Write unit tests for new functionality
- Use table-driven tests where appropriate
- Mock external AWS dependencies
- Run the full suite before submitting: `go test ./... -v -race -count=1`

### Linting

- Run `golangci-lint run` before submitting
- Fix all warnings — CI enforces a clean lint pass

## Pull Request Process

1. **Ensure CI passes** - All tests and lint checks must be green
2. **Include tests** - New behavior needs corresponding tests
3. **Sign commits** - Use `git commit -s` for DCO compliance
4. **Keep branch updated** - Rebase on latest `main` before merging
5. **Address review feedback** - Respond to and resolve all review comments

## Release Process

Releases are handled by maintainers:

1. Tag the commit with a `v*` semver tag (e.g. `v0.2.0`)
2. GoReleaser builds multi-platform binaries automatically
3. Binaries are published to GitHub Releases

## Questions?

- Open a GitHub issue
- Check existing issues and closed PRs

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
