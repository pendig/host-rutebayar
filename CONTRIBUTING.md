# Contributing to host-rutebayar

Thank you for your interest in contributing to `host-rutebayar`! As an open-source project, we welcome contributions of all forms, including bug reports, feature requests, documentation improvements, and code submissions.

---

## Code of Conduct

By participating in this project, you agree to maintain a respectful, welcoming, and collaborative environment. Please be constructive and supportive in your discussions and code reviews.

---

## Development Prerequisites

Before contributing code, ensure you have the following installed on your machine:
- **Go**: Version 1.23 or newer.
- **SQLite**: Database system for persistence.
- **Git**: For version control.

---

## How to Contribute

### 1. Reporting Bugs & Feature Requests
- Check the existing issues/PRs to see if your concern has already been addressed.
- When opening an issue, provide a clear description of the problem, steps to reproduce, and your environment configuration.

### 2. Preparing Your Development Workspace
1. Fork and clone this repository.
2. Initialize the project dependencies:
   ```bash
   go mod download
   ```
3. Copy/configure your environment variables. Refer to the **Konfigurasi runtime** section in the `README.md` or `docs/runbook.md` for details.

### 3. Code Guidelines
- **Go Formatting**: All Go code must be formatted using the standard `gofmt` utility. Run:
   ```bash
   go fmt ./...
   ```
- **Linter & Static Analysis**: Run static analysis to catch potential issues:
   ```bash
   go vet ./...
   ```
- **Clean Architecture**: Adhere to the current packages:
  - `internal/domain`: Pure model schemas and structs.
  - `internal/orchestration`: Core engine and fee calculation rules.
  - `internal/storage`: SQLite implementation and DB queries.
  - `internal/http`: Handlers, middlewares, routing, and UI dashboards.
  - `internal/security`: Cryptography, signature verification, and security checklists.

### 4. Writing & Running Tests
We highly encourage writing unit tests for any new features or bug fixes.
To run all tests in the codebase, execute:
```bash
go test -v ./...
```
Ensure all tests pass successfully before submitting a pull request.

### 5. Submitting a Pull Request
1. Create a descriptive branch name for your changes (e.g., `feature/xyz` or `bugfix/abc`).
2. Keep your commits atomic and write clear, concise commit messages.
3. Push your branch and open a Pull Request (PR) against the `main` branch.
4. Verify that the CI tests build and pass successfully.
5. Address any review comments from maintainers.

---

## License

By contributing to this repository, you agree that your contributions will be licensed under the project's [MIT License](LICENSE).
