# CLAUDE.md — Working with KindleLaunch

This file is for Claude / Cascade and other AI agents that edit the KindleLaunch
repository. It summarizes the project shape, sources of truth, and non-negotiable
discipline.

## Project identity

KindleLaunch is the launchpad AMM and real-time trading platform for the Paxeer
Network. It ships:

- `contracts/` — a Solidity 0.8.27 launchpad AMM, live on Paxeer mainnet (EVM
  chain ID 125).
- Go backend — a strangler-pattern rewrite of the original TypeScript monorepo,
  organized into 13 independent Go modules.

## Read this first

Before making any large decision, read the source of truth:

- `README.md` — architecture, addresses, development commands.
- `knowledge/kindlelaunch.frozen.kvx` — frozen master plan for the backend
  rewrite.
- `contracts/deployments/paxeer-addresses.json` — canonical on-chain addresses.
- `deploy/`, `Makefile`, `.github/workflows/ci.yml` — build, deploy, and CI
  plumbing.

## Non-negotiable rules

- **Every Go package must ship unit tests.** Table-driven, happy + error +
  boundary cases. No fakes, no mocks, no stubs that short-circuit the real code.
- **Coverage gate** is enforced in CI: ≥85% repo-wide, ≥90% for `shared/`,
  `protocol/`, `core/indexer`, `core/pnl-tracker`, `core/trading-charts`.
- **No float for money.** Use `math/big.Int` / `uint256` for all token, price,
  and PnL math.
- **Zero lint warnings.** `golangci-lint` v2 and `go vet` must pass clean. `go test
  -race` must pass.
- **No merge while CI is red.**
- **Production design standard** — 500K+ concurrent users day one: bounded WSS/SSE
  send buffers, rate limits on public ingress, circuit breakers/timeouts/retries on
  upstreams, observability (slog + Prometheus + OTel), and zero-downtime deploys.
- **Do not commit from the dev box.** The user drives commits via their own
  pre-commit hook and local workflow. Do not run `git commit` or `git push`.

## Tooling commands

```bash
make build        # build every Go module
make vet          # go vet across all modules
make fmt-check    # fail if any file is not gofmt-clean
make lint         # golangci-lint (zero warnings)
make test         # unit tests
make race         # race detector
make cover        # per-module coverage profiles
make cover-check  # enforce the coverage gate
make ci           # full CI gate
make tidy         # go mod tidy in every module
make help         # list all targets
```

For contracts:

```bash
cd contracts
pnpm install --frozen-lockfile
pnpm compile
pnpm test
pnpm lint:sol
pnpm format:check
```

## Agent-specific guidance

- **Prefer minimal upstream fixes over downstream workarounds.** Identify the root
  cause before changing code.
- **Add regression tests** for every bug fix. Keep the implementation minimal.
- **Do not change rate cards or PAX/USD recalibration** in gateway/internal/rates
  unless explicitly directed by the user.
- **Do not touch the validator cluster** at `147.93.139.18` (host id
  `validator-cluster`) for any write/restart/exec action.
- **Never touch genesis, private validator keys, or snapshots** without the user's
  explicit YES through a forced gate.
- **No border strokes for UI depth.** Separation must come purely from
  background-color contrast.
- **No emojis, purple gradients, or glow effects** in UI or documentation unless
  explicitly requested.

## When you are unsure

If a task conflicts with anything above, stop and ask the user. Do not guess on
money, security, or production-infrastructure changes.
