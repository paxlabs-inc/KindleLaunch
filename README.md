<div align="center">

# KindleLaunch

**The launchpad AMM and real-time trading platform for the Paxeer Network.**

[![CI](https://github.com/Sidiora-Technologies/KindleLaunch/actions/workflows/ci.yml/badge.svg)](https://github.com/Sidiora-Technologies/KindleLaunch/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Solidity](https://img.shields.io/badge/Solidity-0.8.27-363636?logo=solidity&logoColor=white)
![Chain](https://img.shields.io/badge/Paxeer-Chain%20ID%20125-6C4CF1)
![License](https://img.shields.io/badge/License-HyperPax--OS--Protocol-blue)

</div>

---

KindleLaunch previusly known as **Sidiora.fun** — is a token launchpad where anyone can deploy a
new ERC20 paired against USDL and trade it through a constant-product AMM from
block one. This repository contains both halves of the system:

- **`contracts/`** — the on-chain launchpad AMM written in Solidity 0.8.27:
  a six-layer protocol with programmable pool hooks ("Opticals"), a virtual
  reserve pricing model, dynamic fees, and a Timelock-governed upgrade path.
  Deployed on Paxeer mainnet (EVM chain ID 125).
- **Go backend** — a production-grade microservices backend that indexes the
  chain, serves real-time market data over WebSocket/SSE, tracks PnL and
  rankings, and handles media (metadata, user profiles, social, livestream).
  This is the strangler-pattern rewrite of the original TypeScript monorepo,
  engineered to carry 500K+ concurrent users on day one.

The master plan and single source of truth for the backend rewrite lives at
[`knowledge/kindlelaunch.frozen.kvx`](knowledge/kindlelaunch.frozen.kvx).

---

## Table of Contents

- [Repository Structure](#repository-structure)
- [Smart Contracts](#smart-contracts)
  - [Architecture](#architecture)
  - [Virtual Reserve Model](#virtual-reserve-model)
  - [Opticals](#opticals)
  - [Fee System](#fee-system)
  - [Upgradeability](#upgradeability)
  - [Deployed Addresses](#deployed-addresses)
  - [Contract Development](#contract-development)
- [Go Backend](#go-backend)
  - [Module Layout](#module-layout)
  - [Stack](#stack)
  - [Engineering Standards](#engineering-standards)
  - [Backend Development](#backend-development)
- [Continuous Integration](#continuous-integration)
- [Deployment](#deployment)
- [Integration Kit](#integration-kit)
- [License](#license)

---

## Repository Structure

```
KindleLaunch/
├── contracts/              Solidity protocol (Hardhat + pnpm)
│   ├── contracts/          .sol sources — 6-layer architecture
│   ├── test/               Hardhat test suites (unit + e2e + meta-ag)
│   ├── scripts/            Deploy, verify, upgrade, on-chain test scripts
│   ├── deployments/        Mainnet + localhost address books
│   ├── integration-kit/    Generated ABIs for JS/TS/Go/Rust + env templates
│   ├── docs/               NatSpec-generated contract documentation
│   └── storage-layout/     Pinned storage layouts for upgrade safety
│
├── shared/                 Go runtime library (config, db, redis, chain, auth, http, log)
├── protocol/               Go: abigen bindings + event registry + address book
├── core/                   Go: DATA plane (6 services)
│   ├── indexer/            Chain ingest spine — live + backfill, webhook + pubsub fan-out
│   ├── trading-charts/     OHLCV engine + TradingView UDF adapter
│   ├── stats-workers/      Pool stats, holders, ratings
│   ├── pnl-tracker/        Positions, PnL, referrals, OG cards, reconciler
│   ├── ranking-algo/       Scheduled ranking compute
│   └── api/                Public data gateway — WSS/SSE + rate-limited REST
├── media/                  Go: MEDIA plane (5 services)
│   ├── metadata/           Token metadata + image storage (Cloudflare R2)
│   ├── user/               Profiles, avatars, watchlists
│   ├── social/             Realtime chat + comments
│   ├── livestream/         Livepeer stream management
│   └── gateway/            Public media edge — serve+cache, chat/comment WSS, wizard upload
│
├── deploy/                 Parameterized Dockerfile + deploy scripts (registry-agnostic)
├── tools/                  Coverage gate script
├── knowledge/              Frozen master plan (.kvx)
├── licenses/               SPDX license texts
├── go.work                 Go workspace — ties 13 modules for local dev
└── Makefile                Drives every leaf module via go.work
```

---

## Smart Contracts

The `contracts/` directory contains the KindleLaunch launchpad AMM — a
purpose-built constant-product AMM where every pool pairs a newly launched
ERC20 token against **USDL**. It is not a general-purpose DEX; the single-base-asset
design eliminates multi-hop routing and enables per-pool fault isolation.

### Architecture

The protocol is organized into six layers. The dependency rule is absolute:
**data flows strictly downward.** No layer may import from anything above it.

```
Periphery  →  Opticals  →  Core-Logic  →  Data  →  Protocol  →  Libraries / Bases
```

| Layer | Contracts | Responsibility |
|-------|-----------|----------------|
| **Protocol** | `ProtocolConfig`, `Treasury`, `Timelock`, `GovernanceModule` | Global invariants: fee floors/ceilings, virtual USDL default, creation fee, governance. `Timelock` is immutable — the root of the trust model. |
| **Data** | `EventEmitter`, `PoolRegistry`, `FeeAccumulator` | Persistent record-keeping. `EventEmitter` is the single event hub (one address for indexers). `PoolRegistry` handles on-chain pool discovery. `FeeAccumulator` separates fee accounting from pool math. |
| **Core-Logic** | `SidioraFactory`, `SidioraPool`, `SidioraERC20`, `SidioraNFT` | The AMM engine. Factory orchestrates atomic market creation. Pool does constant-product math + optical hooks. ERC20 is intentionally minimal and immutable. NFT represents fee rights (not liquidity). |
| **Opticals** | `BaseOptical`, `OpticalRegistry`, 6 presets | Programmable pool behavior via lifecycle callbacks. See [Opticals](#opticals). |
| **Periphery** | `Router`, `Quoter`, `FeesRouter` | User-facing entry points. Router handles all swaps/creation. Quoter is read-only (staticcall). FeesRouter manages NFT fee strategies. |
| **Libraries / Bases** | `SidioraMath`, `FeeLib`, `ReserveLib`, `TransferHelper`, `BitFlag`, `FixedPoint128`, `PoolAddress`, `TokenAddress` + base contracts | All math and utility primitives are built in-house — **no OpenZeppelin, no external dependencies.** Fewer attack vectors, simpler audits. |

A separate **Meta-AG** subsystem (`contracts/meta-ag/`) provides an order-routing
and oracle layer (PECOR engine, OracleHub, PriceOracle, vault adapters) for
advanced trading integrations.

### Virtual Reserve Model

Every pool launches with a *virtual* USDL reserve — a fixed amount (10,000 USDL
by default) that exists for pricing only, not actual holdings. The real USDL
reserve starts at zero and grows as buys come in.

```
Effective USDL = virtualUsdlReserve + realUsdlBalance
```

This gives a token a defined starting price with zero real liquidity. As the
real reserve grows, the virtual reserve becomes proportionally insignificant and
the token graduates toward market-discovered pricing. The constant-product math:

```
BUY:   amountOut = (tokenReserve × amountInAfterFee) / (effectiveUsdl + amountInAfterFee)
SELL:  amountOut = (effectiveUsdl × amountInAfterFee) / (tokenReserve + amountInAfterFee)
```

The protocol can never pay out more real USDL than it holds — enforced
explicitly as defense-in-depth, though mathematically guaranteed when
`virtualUsdlReserve > 0`.

### Opticals

Opticals are plugin contracts that inject custom logic at four lifecycle hooks:
`beforeSwap`, `afterSwap`, `beforeFeeDistribution`, `afterFeeDistribution`. A
bitmap flags which hooks are active so the pool skips unused callbacks without
wasted gas. Six preset Opticals ship with the protocol:

| Optical | Behavior |
|---------|----------|
| **AntiSnipe** | Blocks buys above a supply-percentage threshold in the first N blocks |
| **MaxWallet** | Enforces a maximum token holding per wallet |
| **BuybackBurn** | Redirects a portion of fees to buy back and permanently burn tokens |
| **Tax** | Adds a configurable buy/sell tax routed to the NFT owner |
| **Cooldown** | Enforces minimum time between trades per wallet |
| **LaunchpadOptical** | Cliff + linear vesting for team wallets, plus a time-limited capital-raise fee diverting swap fees to the team |

Opticals are immutable once deployed — new behavior requires a new deployment
and audit. The `OpticalRegistry` provides trust signaling: unregistered opticals
are flagged as unverified to frontends but still function.

### Fee System

Fees are **dynamic**, recalculated on every swap from three inputs:

- **Pool age** — new pools carry higher fees; they decay as the market matures.
- **Volatility** — an 8-slot circular price buffer tracks short-term movement.
- **Whale concentration** — large relative order sizes raise fees proportionally.

Components combine with configurable weights from `ProtocolConfig`. Fees are
bounded by `minFeeBps` (0.10%) and `maxFeeBps` (3.00%), with a 0.30% base.

Each pool's fee-rights NFT can be set to one of four distribution strategies:

| Strategy | Description |
|----------|-------------|
| **CLAIM** | Fees accumulate in USDL; NFT holder withdraws on demand |
| **BURN** | Accumulated fees transferred to the dead address (deflationary) |
| **AIRDROP** | Fees distributed proportionally to all current token holders |
| **LP_REWARDS** | Fees added to the pool as real USDL reserve, deepening liquidity |

### Upgradeability

| Contract type | Pattern | Upgrade authority |
|---------------|---------|-------------------|
| Singletons (Factory, Router, NFT, etc.) | UUPS proxy | GovernanceModule → Timelock (48h delay) |
| Pool instances | Beacon proxy (single `PoolBeacon`) | Upgrading the beacon upgrades all pools atomically |
| User tokens (`SidioraERC20`) | **Immutable** | No upgrade path, ever |
| `Timelock` | **Immutable** | The root of the trust model |
| Optical presets | **Immutable** | New behavior = new contract = new audit |

No EOA admin key holds upgrade power. Every change is delayed, observable, and
cancellable by guardians.

### Deployed Addresses

The protocol is live on Paxeer mainnet (chain ID 125). The full address book —
including implementation/proxy pairs for every upgradeable contract and all six
Optical presets — is at
[`contracts/deployments/paxeer-addresses.json`](contracts/deployments/paxeer-addresses.json).

Key proxy addresses:

| Contract | Address |
|----------|---------|
| `SidioraFactory` | `0x8a1A09CEe72c1D39dF33B8284E38baeF8371f465` |
| `Router` | `0xCC7298801112682e10ee14b8a520309caD80336d` |
| `Quoter` | `0xB768e183b6EfDeDf8b2AA7af732039D1C3c452d0` |
| `EventEmitter` | `0x0E10286EE51F99c666CDcAb52451e58AbdA4048F` |
| `PoolRegistry` | `0x7684382c89f79104574D8EF9b31eFf2eD2C2BA0b` |
| `ProtocolConfig` | `0xEeDF5409cFD30bd14D0399318c7d2150265575e5` |
| `FeeAccumulator` | `0x50C69dF6637b3DCE6a7407C5A4b4F99E68514A76` |
| `SidioraNFT` | `0xDF73b354ed9dcB473cc9D01541c46f507591e190` |
| `FeesRouter` | `0x02Df12a44F2658080E76fbcF7D6B34Baa97843b6` |
| `PoolBeacon` | `0xf11f08afe33e020Cab22bCaffBbAfC471c75E9d4` |
| `OpticalRegistry` | `0x4CdA6e48632d51Ee4Fa735D81BF09F7543f644a1` |
| `Timelock` | `0x82e177ca309578dc5Ed7Fc583278D2C96b3c0F14` |

A localhost (chain ID 31337) address book including the Meta-AG subsystem is at
[`contracts/deployments/localhost-addresses.json`](contracts/deployments/localhost-addresses.json).

### Contract Development

Requires Node 22+ and pnpm 10.

```bash
cd contracts
pnpm install --frozen-lockfile

pnpm compile          # hardhat compile
pnpm test             # full Hardhat test suite
pnpm test:coverage    # with coverage report
pnpm test:gas         # with gas reporting (REPORT_GAS=true)

pnpm lint:sol         # solhint
pnpm lint:js          # eslint on scripts + tests
pnpm format           # prettier --write (solidity + js)
pnpm format:check     # prettier --check (CI gate)

pnpm slither          # static analysis (requires slither installed)
pnpm clean            # hardhat clean + remove coverage
```

**Deploy to Paxeer mainnet:**

```bash
# .env must set PRIVATE_KEY and Paxeer RPC URL
pnpm deploy:paxeer    # hardhat run scripts/deploy.js --network paxeer-network
pnpm verify:paxeer    # verify contracts on PaxScan
```

The test suite covers all six layers — unit tests per contract, library tests,
integration tests (happy path, edge cases, stress), and a full Meta-AG test
suite including gas benchmarks and cross-protocol interop tests.

---

## Go Backend

The Go backend is the strangler-pattern rewrite of the original TypeScript
monorepo at `/sidiora`. It is organized as **13 independent Go modules**, each
with its own `go.mod`, tied together for local development by `go.work` and
sharing code via local `replace` directives. Every leaf module builds and runs
standalone.

### Module Layout

Two domain groups, each holding independent Go modules:

**`core/` — DATA plane** (organizes and processes data; no broad public surface)

| Module | Daemon | Role |
|--------|--------|------|
| `indexer` | `indexerd` | Chain ingest spine — live + backfill; HMAC-signed webhook + Redis pubsub fan-out |
| `trading-charts` | `chartsd` | OHLCV candle engine + TradingView UDF adapter |
| `stats-workers` | `statsd` | Pool stats, holder counts, ratings |
| `pnl-tracker` | `pnld` | Positions, realized/unrealized PnL, referrals, OG cards, reconciler |
| `ranking-algo` | `rankingd` | Scheduled ranking compute |
| `api` | `apid` | **Public data gateway** — WSS/SSE + rate-limited REST |

**`media/` — MEDIA plane**

| Module | Daemon | Role |
|--------|--------|------|
| `metadata` | — | Token metadata + image storage (Cloudflare R2) |
| `user` | — | Profiles, avatars, watchlists |
| `social` | — | Realtime chat + comments |
| `livestream` | `livestreamd` | Livepeer stream management |
| `gateway` | — | **Public media edge** — serve+cache, chat/comment WSS, wizard upload |

**Foundations:**

| Module | Role |
|--------|------|
| `shared/` | Runtime library consumed by every leaf: config, db (pgxpool), redis (pubsub/cache), queue (asynq), chain (ethclient), auth (EIP-191 + webhook HMAC), http (server/health/error/ratelimit/apikey/virusscan), process, log, constants, secrets, util |
| `protocol/` | Lowest-level module: contract ABIs, abigen Go bindings, decoded event types, typed on-chain address book. Depends on nothing else in the monorepo. |

Gateways are the **only** public ingress: `core/api` (data) and `media/gateway`
(media). All other leaf services organize and process data internally.

### Stack

| | Choice |
|---|---|
| Language | Go 1.25 (`GOTOOLCHAIN=auto`) |
| HTTP | chi v5 over net/http |
| Database | PostgreSQL 16 via pgx/v5 (pgxpool) + sqlc (type-safe codegen) + goose migrations |
| Cache / bus | Redis 7 via go-redis v9 (pub/sub + cache + rate-limit) |
| Queues | hibiken/asynq (Redis-backed, near-BullMQ semantics) |
| Chain | ethereum/go-ethereum (ethclient) + abigen bindings from `protocol/` |
| Realtime | coder/websocket (WSS) + net/http flusher (SSE), both fed from Redis pub/sub with bounded per-connection send buffers + backpressure |
| Config | caarlos0/env — same env var names as the TS stack |
| Storage | Cloudflare R2 (S3-compatible) for all buckets |
| Observability | OpenTelemetry (otelhttp) |

### Engineering Standards

These are **hard, CI-enforced** rules — not aspirations:

- **Coverage gate:** ≥85% repo-wide; ≥90% for `shared`, `protocol`,
  `core/indexer`, `core/pnl-tracker`, `core/trading-charts` (the money/correctness-critical modules).
- **Real tests only:** tests exercise real code paths against real Postgres and
  Redis (via testcontainers) — no fakes, no mocks of the database.
- **No float for money:** all token/price/PnL math uses `math/big.Int` / `uint256` — never float.
- **Zero lint warnings:** `golangci-lint` (v2 config) must pass clean; `go test -race` must pass.
- **No merge while CI red.**
- **Strangler cutover:** Go services run against the **same** Postgres + Redis as
  the TS stack, reusing existing schemas 1:1, so each service can be cut over
  independently.
- **500K+ day one:** every design choice assumes 500K+ concurrent users —
  load-tested, backpressured, observable, gracefully degrading.

### Backend Development

Requires Go 1.25+.

```bash
make build        # build every module
make vet          # go vet across all modules
make fmt-check    # fail if any file is not gofmt-clean
make lint         # golangci-lint (zero warnings)
make test         # unit tests
make race         # race detector
make cover        # write per-module coverage profiles to .cover/
make cover-check  # enforce the coverage gate (85% / 90%)
make ci           # everything CI runs (build + vet + fmt + lint + race + cover)
make tidy         # go mod tidy in every module
make sqlc         # regenerate sqlc code
make help         # list all targets
```

Tests require live Postgres and Redis. Point them at local instances:

```bash
export TEST_DATABASE_URL=postgres://kindlelaunch:kindlelaunch@localhost:5432/kindlelaunch_test?sslmode=disable
export TEST_REDIS_URL=redis://localhost:6379/0
make test
```

---

## Continuous Integration

CI runs on every push and pull request via
[`.github/workflows/ci.yml`](.github/workflows/ci.yml) with three jobs:

1. **`verify`** — the Go gate: `build → vet → gofmt check → lint (golangci-lint v2) → race tests → coverage gate`. Spins up Postgres 16 and Redis 7 as service containers so tests run against real infrastructure.
2. **`contracts`** — the Solidity gate: `pnpm install --frozen-lockfile → hardhat compile → hardhat test`.
3. **`images`** — builds and pushes Docker images for each shipped Go service to GHCR (`ghcr.io/sidiora-technologies/kindlelaunch-<service>`), tagged by SHA, branch, PR, and semver. Only pushes on real pushes (fork PRs build but skip push).

---

## Deployment

### Go services

Every leaf service shares one parameterized multi-stage Dockerfile:
[`deploy/Dockerfile.svc`](deploy/Dockerfile.svc). It builds a static binary
from the repo root (so workspace `replace` targets resolve) and ships it on a
distroless nonroot runtime image.

```bash
# Build one service image locally
docker build -f deploy/Dockerfile.svc \
  --build-arg SERVICE=core/indexer \
  --build-arg BIN=indexerd \
  --build-arg GO_VERSION=1.25 \
  -t kindlelaunch-core-indexer:dev .
```

Registry-agnostic build + push driver:

```bash
REGISTRY=ghcr.io/Sidiora-Technologies TAG=v0.1.0 \
  deploy/deploy.sh core/indexer indexerd
```

For local strangler bring-up — running Go services against the same Postgres +
Redis as the TS stack — see
[`deploy/docker-compose.strangler.yml`](deploy/docker-compose.strangler.yml).

### Smart contracts

Deploy and verify via Hardhat (see [Contract Development](#contract-development)).

---

## Integration Kit

[`contracts/integration-kit/`](contracts/integration-kit/) contains
auto-generated contract bindings for external integrators, produced by
`scripts/generate-integration-kit.js`:

- **`abi/`** — every contract ABI in three formats: raw JSON, CommonJS (`.js`),
  and TypeScript const assertions (`.ts`, viem/wagmi-ready), plus barrel
  `index.{js,ts,json}` exports.
- **`go/`** — Go bindings for all contracts and interfaces.
- **`rust/`** — Rust bindings (`mod.rs` + per-contract `.rs` + `.json`).
- **`env/`** — environment variable templates for every major framework
  (Next.js, Vite, CRA, Nuxt, SvelteKit, Remix, Node, Python).

Network configuration:

| | Value |
|---|---|
| Chain | Paxeer Network (ID 125) |
| RPC | `https://public-mainnet.rpcpaxeer.online/evm` |
| Explorer | `https://paxscan.paxeer.app` |
| Native coin | PAX |

---

## License

The smart contracts are licensed under the **Paxlabs HyperPax-OS-Protocol
License** — see [`contracts/LICENSE`](contracts/LICENSE).

The Go backend license is not yet selected; SPDX texts will be added under
[`licenses/`](licenses/) once decided.

---

<div align="center">

Built by [Sidiora Technologies](https://github.com/Sidiora-Technologies) on the [Paxeer Network](https://paxeer.app).

</div>