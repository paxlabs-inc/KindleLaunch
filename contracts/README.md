# Introducing KindleLaunch: The Architecture Behind the Next Generation AMM Launchpad

*A deep dive into the design decisions, layered architecture, and novel mechanisms powering the KindleLaunch Launchpad AMM on HyperPaxeer.*

---

## Foreword

Every protocol is a set of bets. Bets on what matters. Bets on what doesn't. Bets on what the next generation of DeFi users will actually need.

KindleLaunch is our bet.

This paper is not a whitepaper. It's an honest, technical account of how KindleLaunch is built — what we borrowed, what we rejected, what we invented, and why. If you've shipped smart contracts, you'll find the tradeoffs familiar. If you haven't, you'll find the principles accessible.

Let's get into it.

---

## The Problem Space

Launchpads have a trust problem.

The current generation suffers from one of two failure modes: they're either too permissive — allowing rug pulls, sniper bots, and whale manipulation to destroy communities before they form — or they're too restrictive, creating walled gardens where creativity and customization are sacrificed at the altar of safety.

The AMM layer underneath most launchpads is similarly compromised. Static fees don't reflect market reality. Monolithic contracts make auditing a nightmare. And when something breaks, it breaks everything.

KindleLaunch is designed to solve this at the architecture level — not the policy level. Good structure makes bad outcomes structurally harder to achieve.

---

## Standing on the Shoulders of Giants

We're not here to reinvent everything. We studied five protocols in depth and borrowed the best ideas from each:

**Uniswap V2** taught us the value of the Core/Periphery split: keep your mathematical heart minimal and immutable, and let a replaceable periphery handle user-facing complexity.

**Uniswap V4** gave us hooks — the idea that pool behavior can be extended through lifecycle callbacks without touching core logic. We've adapted this into what we call *Opticals*.

**Balancer V2** demonstrated that separating token accounting from pool math creates a cleaner, more auditable system. We took that lesson and applied it to fee accounting specifically.

**Pump.fun** pioneered the dual-reserve model — virtual reserves for pricing, real reserves for actual holdings. This is central to how KindleLaunch manages liquidity at launch.

**Aerodrome** showed how separating fee logic into a dedicated contract (rather than baking it into the pool itself) produces dramatically cleaner code and more flexible distribution strategies.

We've synthesized these lessons into a single coherent architecture. But we also made one major deliberate departure.

---

## The Key Architectural Decision: No Singleton

Uniswap V4 and Balancer use a singleton vault — one contract that manages all pools. It's elegant for multi-hop routing across arbitrary token pairs.

We chose not to do this.

Here's why: every KindleLaunch pool is paired with USDL. There are no multi-token paths. No complex routing trees. When your entire design assumes a single base asset, a singleton adds enormous complexity without meaningful benefit.

Instead, we use **per-pool Beacon Proxies**. Each pool is its own isolated contract instance. This gives us fault containment (a bug in one pool can't drain another), cleaner auditability, and — critically — atomic upgradability across all pools simultaneously via the Beacon pattern.

More on that shortly.

---

## The Virtual Reserve Model

Before diving into architecture layers, it's worth understanding the core AMM innovation: KindleLaunch's virtual USDL reserve.

Every pool launches with a *virtual* USDL reserve — a fixed amount (10,000 USDL by default) that exists for pricing purposes only, not actual holdings. The *real* USDL reserve starts at zero and grows as buys come in.

The effective reserve used for price calculation is:

```
Effective USDL = virtualUsdlReserve + realUsdlBalance
```

This means a token pool has a defined starting price even with zero real liquidity behind it. Early buyers are pricing against the virtual reserve. As the real reserve grows, the virtual reserve becomes proportionally less significant, and the token graduates toward real market-discovered pricing.

The math that flows from this is clean constant product:

```
BUY:   amountOut = (tokenReserve × amountInAfterFee) / (effectiveUsdl + amountInAfterFee)
SELL:  amountOut = (effectiveUsdl × amountInAfterFee) / (tokenReserve + amountInAfterFee)
```

One important invariant: **the protocol can never pay out more real USDL than it holds.** The check `require(amountOut <= realUsdlBalance)` is mathematically guaranteed by the formula when virtualUsdlReserve > 0, but it's enforced explicitly as defense-in-depth.

---

## The Six-Layer Architecture

KindleLaunch is organized into six distinct layers. The dependency rule is absolute: **data flows strictly downward.** No layer may import from anything above it.

```
Periphery → Opticals → Core-Logic → Data → Protocol → Libraries/Bases
```

Here's what lives in each layer and why.

---

### Layer 1: Protocol — The Rules of the Game

The Protocol layer defines the global invariants that every other contract respects.

**ProtocolConfig** is the single source of truth for all protocol parameters: fee floors and ceilings, the virtual USDL default, the creation fee (an anti-spam mechanism), and the weights used in dynamic fee calculation. Every contract that needs a parameter reads it from here — there's no duplication, no drift.

**Treasury** holds accumulated protocol revenue. It cannot modify config or interact with pools. Its only jobs are receiving and disbursing.

**Timelock** is the trust anchor of the entire system, and uniquely, it is the only governance contract we chose to make *immutable*. If the Timelock could be upgraded, the security model would be self-referential. The Timelock enforces a minimum delay (48 hours) on all governance-approved actions — no matter what.

**GovernanceModule** implements decentralized decision-making via the SID token. Initially, a multisig adapter fills this role while token distribution reaches a point where holder governance is meaningful. Transitions happen through the same Timelock it governs.

---

### Layer 2: Data — The Memory of the Protocol

Three contracts form the protocol's persistent record-keeping.

**EventEmitter** is the single event hub for the entire protocol. Inspired by GMX V2's design, every meaningful action in KindleLaunch — a swap, a market creation, a fee distribution, a config update — emits through this one contract. Every event carries a poolId, a block timestamp, and a block number. Indexers need to track exactly one address.

**PoolRegistry** handles on-chain pool discovery. It maps token addresses to pool addresses, creators to their pools, and stores creation metadata. The Router validates against it. The Quoter queries it. The Factory writes to it. Nobody else does.

**FeeAccumulator** is where fee accounting lives — completely separated from the pool itself. After each swap, the pool records the fee amount here and then focuses on what it's good at: AMM math. The FeeAccumulator handles the four distribution strategies (more below), the protocol's cut, and the treasury sweep. Clean separation of concerns.

---

### Layer 3: Core-Logic — The Heart

This is where value is created and traded.

**KindleLaunchFactory** orchestrates market creation in a single transaction. In sequence: read parameters from ProtocolConfig, collect the creation fee, deploy a new ERC20 token via CREATE2, deploy a new pool via Beacon Proxy, transfer the entire token supply to the pool, mint the fee rights NFT to the creator, register everything in PoolRegistry, emit MarketCreated. That's a complete market, atomically.

**KindleLaunchPool** is the AMM engine. It is deliberately narrow in scope: AMM math, state transitions, fee recording, and optical hooks. It does not manage fee strategies, mint tokens, or register itself. Dynamic fees are calculated via the FeeLib library, considering pool age, price volatility (tracked via an 8-slot circular price buffer), and whale concentration. After calculating the fee, it calls FeeAccumulator.recordFee() and moves on.

**KindleLaunchERC20** is intentionally the simplest contract in the system: a standard ERC20 with Permit support, minted once by the Factory, supply fixed forever. Users must be able to trust that their token code doesn't change. So it doesn't.

**KindleLaunchNFT** represents fee rights. One NFT per pool, minted to the creator at launch. Transfer it, and you transfer all future fee rights. The NFT stores the chosen fee strategy and can be updated by whoever holds it. This is a fundamentally different model from liquidity position tokens — here, the NFT isn't about liquidity, it's about ongoing protocol revenue rights.

---

### Layer 4: Opticals — Programmable Pool Behavior

Opticals are plugin contracts that inject custom logic at defined points in a pool's lifecycle. The interface exposes four hooks: `beforeSwap`, `afterSwap`, `beforeFeeDistribution`, and `afterFeeDistribution`. A bitmap flags which hooks are active, so the pool skips unused callbacks without wasted gas.

Six preset Opticals ship with the protocol:

| Optical | What It Does |
|---|---|
| **AntiSnipe** | Blocks buys above a supply percentage threshold in the first N blocks |
| **MaxWallet** | Enforces a maximum token holding per wallet |
| **BuybackBurn** | Redirects a portion of fees to buy back and permanently burn tokens |
| **Tax** | Adds a configurable buy/sell tax routed to the NFT owner |
| **Cooldown** | Enforces minimum time between trades per wallet (anti-bot) |
| **Vesting** | Implements gradual token unlock for specified wallets |

Opticals are immutable once deployed. New behavior requires a new deployment. Audited once, trusted forever.

The OpticalRegistry provides trust signaling — unregistered opticals are flagged as unverified to users and frontends, but they still function. This is freedom with transparency, not control.

---

### Layer 5: Periphery — What Users Touch

The Periphery layer is the public face of the protocol.

**Router** is the single entry point for all user transactions: creating markets, buying, and selling. It's effectively stateless — it validates, routes, and delegates. It handles all token transfers into pools before triggering swaps. It inherits Multicall, meaning multiple actions can be batched into a single transaction.

**Quoter** is read-only and consumes no gas via staticcall. It provides exact input/output quotes with fee breakdowns and price impact, pool stats, market cap calculations, and paginated pool discovery. The frontend leans on this heavily.

**FeesRouter** is the interface for NFT holders to manage their fee strategy: switch between CLAIM, BURN, AIRDROP, and LP_REWARDS, and execute each accordingly.

---

### Layer 6: Libraries and Bases — Zero External Dependencies

Every mathematical and utility primitive in KindleLaunch is built in-house. No OpenZeppelin. No external library dependencies. This was a deliberate choice: fewer external contracts means fewer attack vectors, simpler audits, and full understanding of every line of code in the system.

KindleLaunchMath handles fixed-point arithmetic. FeeLib encapsulates dynamic fee calculation. ReserveLib handles the constant product math. TransferHelper wraps token transfers safely. BitFlag manages the optical hook bitmap.

The base contracts (ERC20Base, ERC721Base, Proxy patterns, AccessControl, ReentrancyGuard, etc.) are similarly in-house implementations of battle-tested patterns.

---

## Upgradeability Without Trust Compromise

This is one of the harder problems in protocol design: how do you build something that can be improved over time without creating an upgrade backdoor that undermines user trust?

KindleLaunch's answer is layered and explicit:

**Singleton contracts** (Factory, Router, NFT, etc.) use UUPS proxies. An upgrade can only be queued through the GovernanceModule and executed through the Timelock after its mandatory delay.

**Pool instances** use Beacon Proxies. There is exactly one PoolBeacon contract. Every pool reads its implementation from this beacon. Upgrading the beacon's implementation upgrades all pools simultaneously — atomically, consistently, with no stragglers. The beacon itself is only upgradeable through the same Timelock.

**User tokens** (KindleLaunchERC20) are immutable. No upgrade path, ever. Token holders need this guarantee.

**The Timelock itself** is immutable. This is the root of the trust model.

**Optical presets** are immutable. New behavior = new contract = new audit.

The result: no EOA admin key holds upgrade power. No single developer can push a malicious upgrade in the middle of the night. Every change is delayed, observable, and cancellable by guardians.

---

## Fee Strategy: Four Ways to Win

When a creator launches a market on KindleLaunch, they mint an NFT that represents their fee rights. That NFT can be configured with one of four strategies at any time:

**CLAIM** — Fees accumulate in USDL and the NFT holder withdraws on demand. The purest form of protocol revenue sharing.

**BURN** — Accumulated fees are transferred to the dead address. A deflationary signal. A commitment mechanism.

**AIRDROP** — Fees accumulate and are distributed proportionally to all current token holders. The gas cost of on-chain iteration is viable on HyperPaxeer's subsidized network. It turns fees into community rewards.

**LP_REWARDS** — Fees are transferred to the pool as additional real USDL reserve. This increases the pool's real liquidity depth over time, compounding into tighter spreads and better prices for everyone. Mechanically clean: the pool simply calls syncReserves() and re-reads its own balance, avoiding any upward call across the layer boundary.

---

## Dynamic Fees

KindleLaunch's fee system is not static. It is calculated fresh on every swap, considering three inputs:

**Pool Age** — New pools carry higher fees. This reflects genuine uncertainty about price discovery in the early blocks. Fees decay over time as the market matures.

**Volatility** — A circular buffer of 8 recent price snapshots tracks short-term price movement. High volatility raises fees to compensate liquidity providers for increased risk.

**Whale Concentration** — Large relative order sizes indicate concentrated buying or selling. Higher concentration raises fees proportionally.

The three components combine with configurable weights defined in ProtocolConfig. The result is a fee that reflects actual market conditions — not a number someone picked at deployment and never changed.

Fees are bounded by minFeeBps (0.10%) and maxFeeBps (3.00%), with a 0.30% base.

---

## Deployment Details

- **Chain**: HyperPaxeer (EVM Chain ID 125)
- **Solidity**: 0.8.27, viaIR enabled
- **Upgrade Authority**: Timelock via GovernanceModule
- **Governance Token**: SID (existing ERC20 on Paxeer)
- **Initial Admin**: Network-owned multisig, transitioning to full SID-holder governance

---

## What We Are and Aren't

KindleLaunch is a launchpad AMM — specifically designed for the one-sided USDL-paired model that launchpads require. It is not a general-purpose DEX. It is not trying to be Uniswap.

It is designed for:
- Projects that want to launch a token with real market mechanics from block one
- Creators who want genuine fee rights, not just governance tokens
- Communities that want protection from snipers and bots built into the protocol layer
- Developers who want to extend pool behavior through a clean, auditable hook system

---



