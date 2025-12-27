# Contributing to go-mink

First off, thank you for considering contributing to go-mink! It's people like you that make go-mink such a great tool.

Following these guidelines helps to communicate that you respect the time of the developers managing and developing this open source project. In return, they should reciprocate that respect in addressing your issue, assessing changes, and helping you finalize your pull requests.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [I Have a Question](#i-have-a-question)
- [I Want To Contribute](#i-want-to-contribute)
  - [Reporting Bugs](#reporting-bugs)
  - [Suggesting Enhancements](#suggesting-enhancements)
  - [Your First Code Contribution](#your-first-code-contribution)
  - [Pull Requests](#pull-requests)
- [Styleguides](#styleguides)
  - [Git Commit Messages](#git-commit-messages)
  - [Go Styleguide](#go-styleguide)

## Code of Conduct

This project and everyone participating in it is governed by the [go-mink Code of Conduct](CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code. Please report unacceptable behavior to the project maintainers.

## I Have a Question

Questions are often better asked in our [Discussions](https://github.com/AshkanYarmoradi/go-mink/discussions) (if enabled) or by opening an Issue with the `question` label.

## I Want To Contribute

### Reporting Bugs

This section guides you through submitting a bug report for go-mink. Following these guidelines helps maintainers and the community understand your report, reproduce the behavior, and find related reports.

**Before Submitting a Bug Report**

*   **Check the [documentation](docs/)** for a list of common questions and guides.
*   **Search the existing Issues** to see if the problem has already been reported.

**How to Submit a Good Bug Report**

Open an Issue and provide the following information:

*   **Use a clear and descriptive title** for the issue to identify the problem.
*   **Describe the exact steps to reproduce the problem** in as many details as possible.
*   **Provide specific examples to demonstrate the steps**. Include links to files or GitHub projects, or copy/pasteable snippets, which you use in those examples.
*   **Describe the behavior you observed after following the steps** and point out what exactly is the problem with that behavior.
*   **Explain which behavior you expected to see instead and why.**
*   **Include details about your configuration and environment**: Go version, OS, database version, etc.

### Suggesting Enhancements

This section guides you through submitting an enhancement suggestion for go-mink, including completely new features and minor improvements to existing functionality.

**How to Submit a Good Enhancement Suggestion**

*   **Use a clear and descriptive title** for the issue to identify the suggestion.
*   **Provide a step-by-step description of the suggested enhancement** in as many details as possible.
*   **Explain why this enhancement would be useful** to most go-mink users.

### Your First Code Contribution

1.  **Fork the repository** on GitHub.
2.  **Clone your fork** locally.
3.  **Create a new branch** for your feature or fix.
    ```bash
    git checkout -b feature/amazing-feature
    ```
4.  **Make your changes**.
5.  **Run tests** to ensure you haven't broken anything.
    ```bash
    go test ./...
    ```
6.  **Commit your changes** using descriptive commit messages.

### Pull Requests

The process described here has several goals:

- Maintain go-mink's quality
- Fix problems that are important to users
- Engage the community in working toward the best possible go-mink

Please follow these steps to have your contribution considered by the maintainers:

1.  Follow all instructions in [the template](.github/PULL_REQUEST_TEMPLATE.md) (if available).
2.  Follow the [styleguides](#styleguides).
3.  After you submit your pull request, verify that all status checks are passing.

## Styleguides

### Git Commit Messages

*   Use the present tense ("Add feature" not "Added feature")
*   Use the imperative mood ("Move cursor to..." not "Moves cursor to...")
*   Limit the first line to 72 characters or less
*   Reference issues and pull requests liberally after the first line
*   Consider using [Conventional Commits](https://www.conventionalcommits.org/)

### Go Styleguide

*   We follow the official [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments).
*   Run `go fmt` before committing.
*   Run `go vet` to catch common errors.
*   Ensure all public functions and types have documentation comments.

## Development Setup

### Prerequisites

- Go 1.21 or later
- PostgreSQL 14+ (for integration tests)
- Docker (optional, for test containers)

### Running Tests

```bash
# Run unit tests
go test -short ./...

# Run all tests (requires PostgreSQL)
go test ./...
```

Thank you for contributing!
