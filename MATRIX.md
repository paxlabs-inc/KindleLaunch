# MATRIX.md — File Map for Matrix Agents

This is a navigation guide for Matrix agents working on the KindleLaunch
repository. It maps each directory and key file to its purpose, lists the
commands that validate each area, and records the hard constraints that must
not be violated.

## Read these first

Before touching code, read the authoritative sources:

- `README.md` — architecture, deployed addresses, and development commands.
- `knowledge/kindlelaunch.frozen.kvx` — frozen master plan for the backend rewrite.
- `contracts/deployments/paxeer-addresses.json` — canonical on-chain addresses.
- `Makefile` and `.github/workflows/ci.yml` — the build and validation contract.

## Project identity

KindleLaunch is the launchpad AMM and real-time trading platform for the Paxeer
Network. It has two halves:

- `contracts/` — Solidity 0.8.27 launchpad AMM, live on Paxeer mainnet (EVM chain
  ID 125).
- Go backend — a strangler-pattern rewrite of the original TypeScript monorepo,
  split into 13 independent Go modules tied together by `go.work`.

## Root-level map

| Path | Purpose |
|------|---------|
| `Makefile` | Top-level driver for all Go modules (`build`, `vet`, `lint`, `test`, `race`, `cover`, `ci`). |
| `go.work` | Go workspace that ties the 13 backend modules for local development. |
| `deploy/` | Parameterized Dockerfile, deploy script, and docker-compose strangler. |
| `tools/` | `coverage-gate.sh` — enforces the per-module coverage floor. |
| `knowledge/` | Frozen specs and master plans (e.g., `kindlelaunch.frozen.kvx`). |
| `licenses/` | SPDX license texts. |
| `research/` | Research work, snapshots, and audit scripts. |
| `client/` | Front-end client workspace. |

## contracts/ — On-chain protocol

| Path | Purpose |
|------|---------|
| `contracts/contracts/protocol/` | Global invariants: `ProtocolConfig`, `Treasury`, `Timelock`, `GovernanceModule`. |
| `contracts/contracts/data/` | Persistent record-keeping: `EventEmitter`, `PoolRegistry`, `FeeAccumulator`. |
| `contracts/contracts/core/` | AMM engine: `SidioraFactory`, `SidioraPool`, `SidioraERC20`, `SidioraNFT`. |
| `contracts/contracts/opticals/` | Programmable pool hooks: `BaseOptical`, `OpticalRegistry`, and presets. |
| `contracts/contracts/periphery/` | User-facing entry points: `Router`, `Quoter`, `FeesRouter`. |
| `contracts/contracts/libraries/` | Math and utility primitives (`SidioraMath`, `FeeLib`, `ReserveLib`, etc.). |
| `contracts/contracts/base/` | Base contracts reused by other layers. |
| `contracts/contracts/interfaces/` | Solidity interfaces. |
| `contracts/contracts/meta-ag/` | Order-routing and oracle layer (PECOR engine, `OracleHub`, vault adapters). |
| `contracts/contracts/WPAX9.sol` | Wrapped PAX9 token. |
| `contracts/test/` | Hardhat unit, integration, and Meta-AG test suites. |
| `contracts/scripts/` | Deploy, verify, upgrade, and on-chain test scripts. |
| `contracts/deployments/` | Mainnet + localhost address books. |
| `contracts/integration-kit/` | Generated ABIs and bindings for JS/TS, Go, and Rust. |
| `contracts/docs/` | NatSpec-generated contract documentation. |
| `contracts/storage-layout/` | Pinned storage layouts for upgrade safety. |
| `contracts/hardhat.config.js` | Hardhat configuration. |
| `contracts/package.json` | pnpm workspace root. |
| `contracts/.env.example` | Environment template for contract deploys. |

## protocol/ — Go chain bindings

| Path | Purpose |
|------|---------|
| `protocol/addresses.go` | Typed on-chain address book; imports `contracts/deployments/paxeer-addresses.json`. |
| `protocol/events.go` | Decoded event types used by the indexer. |
| `protocol/bindings/` | abigen-generated Go bindings for every contract. |

## shared/ — Go runtime library

Every backend module depends on this. Do not introduce unstable or unrelated API
changes here.

| Path | Purpose |
|------|---------|
| `shared/auth/` | EIP-191 signature verification and webhook HMAC signing. |
| `shared/chain/` | `ethclient` wrapper and contract call helpers. |
| `shared/config/` | Environment parsing (`caarlos0/env`). |
| `shared/constants/` | Project-wide constants. |
| `shared/db/` | `pgxpool` and SQL helpers; migrations pattern. |
| `shared/http/` | Server, health, errors, rate limiting, API key, and virus-scan middleware. |
| `shared/log/` | Structured logging (slog). |
| `shared/process/` | Process lifecycle helpers. |
| `shared/queue/` | `hibiken/asynq` task-queue helpers. |
| `shared/redis/` | Redis pub/sub, cache, and rate-limit helpers. |
| `shared/secrets/` | Secrets management helpers. |
| `shared/util/` | General utilities. |

## core/ — Data plane

Internal organizing services. Public ingress is only through `core/api`.

| Path | Purpose |
|------|---------|
| `core/indexer/` | Chain ingest spine — live + backfill; HMAC-signed webhook + Redis pubsub fan-out. |
| `core/trading-charts/` | OHLCV candle engine and TradingView UDF adapter. |
| `core/stats-workers/` | Pool stats, holder counts, and ratings. |
| `core/pnl-tracker/` | Positions, realized/unrealized PnL, referrals, OG cards, and reconciler. |
| `core/ranking-algo/` | Scheduled ranking compute. |
| `core/api/` | Public data gateway — WSS/SSE + rate-limited REST. |

## media/ — Media plane

| Path | Purpose |
|------|---------|
| `media/metadata/` | Token metadata + image storage (Cloudflare R2). |
| `media/user/` | Profiles, avatars, watchlists. |
| `media/social/` | Realtime chat + comments. |
| `media/livestream/` | Livepeer stream management. |
| `media/gateway/` | Public media edge — serve+cache, chat/comment WSS, wizard upload. |

## Validation commands by area

### Go backend

```bash
make build        # build every module
make vet          # go vet across all modules
make fmt-check    # fail if any file is not gofmt-clean
make lint         # golangci-lint (zero warnings)
make test         # unit tests
make race         # race detector
make cover        # per-module coverage profiles
make cover-check  # enforce the coverage gate
make ci           # full CI gate
make tidy         # go mod tidy in every module
make sqlc         # regenerate sqlc code
```

### Solidity contracts

```bash
cd contracts
pnpm install --frozen-lockfile
pnpm compile          # hardhat compile
pnpm test             # full test suite
pnpm test:coverage    # with coverage
pnpm lint:sol         # solhint
pnpm lint:js          # eslint on scripts + tests
pnpm format:check     # prettier check
pnpm deploy:paxeer    # deploy to Paxeer mainnet
pnpm verify:paxeer    # verify on PaxScan
```

## Hard rules for agents

- **Consumer surface.** The UI and API must show users results and outcomes, not
  protocol jargon (MCL, cortex, Merkle, replay). Hide implementation language.
- **Cortex first.** Use `cortex_recall()` at session start and `cortex_search()`
  during work. Persist durable learnings with `cortex_remember_*`.
- **No commits from the agent.** The user drives commits via their own workflow. Do
  not run `git commit` or `git push` on the dev box.
- **No production infrastructure changes autonomously.** Do not chain deploys,
  machine updates, or background builds without the user's explicit go-ahead.
- **No validator cluster access.** Never touch the Gideon validator cluster
  (`147.93.139.18`, host id `validator-cluster`) for write/restart/exec.
- **No genesis or key risk.** Never wipe data dirs, remove volumes, run
  unsafe-reset, or delete genesis/private-validator/snapshot state without the
  user's explicit YES.
- **No rate-card changes.** Do not modify gateway rate cards or PAX/USD
  recalibration without explicit approval.
- **No floats for money.** Token, price, and PnL math uses `math/big.Int` or
  `uint256` only.
- **Coverage and tests.** Every package must have unit tests; coverage gates
  are ≥85% repo-wide and ≥90% for `shared/`, `protocol/`, `core/indexer`,
  `core/pnl-tracker`, `core/trading-charts`. Add regression tests for every bug
  fix.
- **No fake tests.** Exercise real code paths and real types; no stub mocks or
  placeholders that make tests pass.
- **Aesthetic.** Minimal, dark, high-signal; Paxeer Blue `#004CED`; Inter +
  JetBrains Mono. No emojis, purple gradients, or glow effects unless asked. No
  border strokes for UI depth — use background-color contrast.

## MatrixScript notes

If you author or edit `.mtx` files in this repository, always double-quote
string-typed KV values (`description`, `display`, `reason`, `hint`, `prompt`).
Unquoted values parse as space-separated ident lists and produce phantom
KVPairs.
