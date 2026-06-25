# Contributing to KindleLaunch

Thank you for helping make KindleLaunch better. This document describes how to
set up the project, run the test gates, and open changes.

## Quick Start

### Requirements

- **Go 1.25+** with `GOTOOLCHAIN=auto`.
- **Node 22+** and **pnpm 10** for the Solidity workspace.
- **PostgreSQL 16** and **Redis 7** for backend tests.
- **Docker** (optional) for the service image build and local strangler bring-up.

### Clone and build

```bash
git clone https://github.com/Sidiora-Technologies/KindleLaunch.git
cd KindleLaunch

# Go backend
make build
make ci

# Solidity contracts
cd contracts
pnpm install --frozen-lockfile
pnpm compile
pnpm test
```

## Development workflow

1. **Open an issue first** for large features, breaking changes, or anything that
   affects the contract trust model or the public API surface.
2. **Create a branch** from `main` with a descriptive name.
3. **Write tests** for every change. The CI coverage gate is ≥85% repo-wide and ≥90%
   for `shared/`, `protocol/`, `core/indexer`, `core/pnl-tracker`, and
   `core/trading-charts`.
4. **Run the full gate locally** before pushing:

   ```bash
   make ci
   ```

5. **Open a pull request** and fill out the template. PRs require a green CI run
   and at least one reviewer approval.

## Code standards

- **Go**: `gofmt`, `golangci-lint` v2, and `go vet` must pass with zero warnings.
- **Solidity**: `solhint`, `eslint`, and `prettier` must pass with zero warnings.
- **No floats for money**: token, price, and PnL math must use `math/big.Int` or
  `uint256`.
- **Real tests only**: exercise real code paths; do not use fakes or mocks of the
  database or chain client.
- **Strangler discipline**: Go services reuse the same Postgres and Redis
  schemas as the TypeScript stack, so cutover can happen service by service.

## Areas that need extra care

- `contracts/` — the AMM handles real money. Any change must be paired with tests,
  storage-layout review, and a security mindset.
- `core/indexer`, `core/pnl-tracker`, `core/trading-charts` — correctness-critical.
  Changes here need regression tests and must keep the coverage gate.
- `protocol/` and `shared/` — consumed by every other module. Keep the API
  surface minimal and stable.

## Reporting bugs

Use the [Bug report issue template](.github/ISSUE_TEMPLATE/bug_report.md). Include:

- Steps to reproduce.
- Expected vs actual behavior.
- Commit or environment where you observed it.
- Logs or test output if relevant.

## Security issues

Please see [`SECURITY.md`](SECURITY.md) for how to report vulnerabilities
responsibly.

## License

By contributing, you agree that your contributions will be licensed under the
same license as the project. Contract code is licensed under the Paxlabs
HyperPax-OS-Protocol License; the Go backend license is described in the
[`licenses/`](licenses/) directory and `README.md`.
