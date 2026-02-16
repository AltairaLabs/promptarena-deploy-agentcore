# Security Policy

## Supported Versions

| Version        | Supported          |
| -------------- | ------------------ |
| main           | :white_check_mark: |
| Latest release | :white_check_mark: |
| Older releases | :x:                |

## Reporting a Vulnerability

We appreciate responsible disclosure of security vulnerabilities. If you discover a security issue in this deploy adapter, please follow the steps below.

### Do Not Create Public Issues

**Please do not report security vulnerabilities through public GitHub issues.** Public disclosure before a fix is available can put users at risk.

### Report Privately

Send an email to our security team at: **[security@altairalabs.ai](mailto:security@altairalabs.ai)**

Include the following information in your report:

- **Description** of the vulnerability
- **Steps to reproduce** the issue
- **Potential impact** and attack scenarios
- **Suggested fixes** or mitigations, if any
- Your **contact information** for follow-up

### Response Timeline

- **Initial Response**: Within 48 hours of receiving your report
- **Triage**: Within 5 business days we will provide an initial assessment
- **Resolution**: Depending on severity and complexity, typically within 30-90 days

## Security Measures

### Static Analysis

- **gosec** via [golangci-lint](https://golangci-lint.run/) runs on all pull requests to catch common Go security issues (command injection, hardcoded credentials, weak crypto, etc.)

### Dependency Management

- **[Dependabot](https://github.com/dependabot)** monitors Go module dependencies and creates pull requests for security updates automatically.

### Code Review

- All changes require peer review before merging to `main`.

### Signed Releases

- Release binaries are signed and include checksums for verification.

## Security Considerations for Users

### AWS Credentials Handling

This adapter interacts with AWS Bedrock AgentCore. Follow these practices:

- **Never commit AWS access keys or secret keys** to version control.
- **Never commit IAM role ARNs** or account-specific identifiers into source or configuration files.
- Use **IAM roles** (instance profiles, IRSA, or ECS task roles) instead of long-lived credentials wherever possible.
- Use **environment variables** or the AWS credentials chain rather than hardcoded values.
- Apply the **principle of least privilege** when creating IAM policies for the adapter.

### Secure Defaults

- The adapter ships with secure defaults. Review any configuration overrides carefully before deploying to production.
- Use HTTPS/TLS for all network communication.

### Input Validation

- All configuration inputs are validated before use. Ensure configuration files are sourced from trusted locations and not writable by untrusted users.

## Vulnerability Disclosure Policy

### Our Commitment

- We will work with security researchers to understand and fix reported vulnerabilities.
- We will provide credit to researchers who report vulnerabilities responsibly.
- We will not take legal action against researchers who follow responsible disclosure practices.

### Researcher Guidelines

- Follow responsible disclosure practices.
- Do not access data that is not your own.
- Do not perform actions that could harm the service or other users.
- Provide sufficient detail to reproduce the vulnerability.

### Public Disclosure

Once a vulnerability is fixed:

1. We will publish a security advisory with details about the issue.
2. We will credit the reporting researcher (unless they prefer anonymity).
3. We may coordinate with the researcher on the timing of public disclosure.

## Resources

- **Security Advisories**: [GitHub Security Advisories](https://github.com/AltairaLabs/promptarena-deploy-agentcore/security/advisories)
- **Security Contact**: [security@altairalabs.ai](mailto:security@altairalabs.ai)
- **Parent Project**: This adapter is part of the [PromptKit](https://github.com/AltairaLabs/PromptKit) ecosystem.

---

**Last Updated**: February 16, 2026

For questions about this security policy, contact: [security@altairalabs.ai](mailto:security@altairalabs.ai)
