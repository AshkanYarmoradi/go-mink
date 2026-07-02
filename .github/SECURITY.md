# Security Policy

We take the security of **go-mink** seriously. Thank you for helping keep the project and its users safe.

## Supported Versions

go-mink follows [Semantic Versioning](https://semver.org/). Security fixes are applied to the latest stable release line.

| Version | Supported          |
| ------- | ------------------ |
| 1.0.x   | :white_check_mark: |
| < 1.0.0 (pre-release / RC) | :x: |

Because go-mink is a library, the most effective way to receive security fixes is to stay on the latest tagged release (`go get go-mink.dev@latest`).

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues, discussions, or pull requests.**

Instead, report them privately through GitHub's coordinated disclosure workflow:

1. Go to the [**Security** tab](https://github.com/AshkanYarmoradi/go-mink/security) of the repository.
2. Click **"Report a vulnerability"** to open a private security advisory (GitHub Private Vulnerability Reporting).
3. Fill in the details described below.

This keeps the report confidential between you and the maintainers until a fix is released.

### What to Include

A good report helps us reproduce and fix the issue quickly. Please include as much of the following as you can:

- The type of issue (e.g., data exposure, injection, authentication/authorization flaw, cryptographic weakness).
- The affected component and version (tag/branch/commit, or a direct source URL).
- Full paths of the source file(s) involved.
- Any special configuration required to reproduce.
- Step-by-step instructions to reproduce the behavior.
- Proof-of-concept or exploit code, if possible.
- The impact — how an attacker might exploit the issue.

### Our Commitment

- **Acknowledgement** within **48 hours** of your report.
- **Assessment** of severity and impact, keeping you updated as we investigate (we may reach out for more detail).
- **Fix & release** — we develop a fix, release it, and publish an advisory.
- **Credit** — with your permission, we'll credit you in the advisory and release notes.

If you do not receive an acknowledgement within 48 hours, please open a *non-sensitive* placeholder issue asking us to check our security advisories (without disclosing any details), so we can follow up.

Thank you for practicing responsible disclosure and helping make go-mink safer for everyone. 🔐
