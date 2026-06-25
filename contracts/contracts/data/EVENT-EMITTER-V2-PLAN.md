# EventEmitter v2 — Unification & Optimization Plan

**Status:** Draft for Andrew review
**Target contract:** `contracts/data/EventEmitter.sol` (deployed at `0x6679aF411d534de222C32ed0AF94C3BD67090672` — UUPS proxy, upgradeable in-place)
**Goal:** Make the EventEmitter the **sole indexing surface** for the entire Sidiora + Meta-AG/PECOR + Opticals + Protocol stack. A well-behaved indexer listening only to `EventEmitter` recovers 100 % of protocol state.
**Gas constraint:** None. Network is owned. Optimize for **indexer simplicity, forward-compatibility, and data completeness**, not gas.
**Author:** Cascade, session 2026-05-01

---

## 1. Executive Summary

### What exists today
`EventEmitter.sol` (141 lines) is a **UUPS-upgradeable, single-admin, authorized-emitter hub** that currently exposes **8 typed emit functions** covering the Launchpad core hot path only:

```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/interfaces/IEventEmitter.sol:11-86
event MarketCreated / Swap / FeeRecorded / FeeDistributed /
      FeeStrategyChanged / OpticalExecuted / PoolStateUpdated / ConfigUpdated
```

Actual call sites (auto-wired):

| Caller | EE function used |
|---|---|
| `@/root/sidiora.fun-main-branch/smart-contracts/contracts/core/SidioraFactory.sol:151` | `emitMarketCreated` |
| `@/root/sidiora.fun-main-branch/smart-contracts/contracts/core/SidioraPool.sol:194` | `emitSwap` |
| `@/root/sidiora.fun-main-branch/smart-contracts/contracts/core/SidioraPool.sol:209-211` | `emitPoolStateUpdated` |
| `@/root/sidiora.fun-main-branch/smart-contracts/contracts/protocol/ProtocolConfig.sol:109` | `emitConfigUpdated` |
| *(any other caller)* | **NONE — every other contract emits natively only** |

### The gap
Everything else emits **native events that never touch the EventEmitter**. The indexer must therefore enumerate **21 watched addresses** and parse their individual ABIs. The freeze lock at `backend/go-indexer/upgrade.freeze.lock` exists precisely because of this proliferation. An EventEmitter v2 that absorbs the full surface collapses the indexer from "21 contracts × N ABIs" to "1 contract × 1 ABI".

### Recommended architecture
A **hybrid typed + generic** universal event hub, inspired by GMX V2's `EventEmitter` and Synthetix V3's `EventLog` schema:

1. **Typed fast-path methods** for the ~25 highest-volume / indexer-critical events (Swap, Trade, MarketCreated, PriceUpdated, CircuitBreakerTriggered, LimitOrderExecuted, …). Stable `topic0`, zero ambiguity, indexer decodes with a hardcoded ABI entry.
2. **Generic schemaless catch-all**: one `EventLog{1,2}` event carrying `(eventName, topic1, topic2, EventData)` where `EventData` is GMX-V2-style `addressItems / uintItems / intItems / boolItems / bytes32Items / bytesItems / stringItems` key→value maps. Adding a new event type later = **zero upgrade, zero new topic0, indexer adds a schema entry**.
3. **Auto-authorization mesh**: Factory, LaunchpadOpticalFactory, PoolRegistry, OpticalRegistry, and the Meta-AG registry collectively whitelist every contract they spawn so pools/opticals/adapters can emit without per-address admin txs.
4. **ERC20/ERC721 transfer mirror**: `emitTokenTransfer` / `emitNftTransfer` called from `SidioraERC20._update` hook and `SidioraNFT._update` hook. Native `Transfer` stays for wallet compatibility, but the canonical indexer stream is the mirror.

This keeps UUPS upgradability (v1 → v2 impl swap, no proxy change, no address change, no downstream reconfiguration) while giving us infinite forward extensibility.

---

## 2. Full Event Inventory (Current State)

Numbers are direct greps of `contracts/**/*.sol` (excluding `test/` and `mocks/`).

### 2.1 Launchpad Core (8 EE routes, 22 native events un-routed)
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/interfaces/IRouter.sol:27-54
event MarketCreated, Buy, Sell, MultihopSwap
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/interfaces/IFeeAccumulator.sol:18-66
event FeeRecorded, FeesClaimed, FeesBurned, AirdropTriggered, AirdropClaimed,
      LpRewardsSent, ProtocolFeeSwept, OpticalSurplusRecorded, OpticalSurplusClaimed
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/interfaces/IFeesRouter.sol:13-18
event FeesClaimed, FeesBurned, AirdropExecuted, AirdropClaimed,
      LpRewardsExecuted, FeeStrategyChanged
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/interfaces/ISidioraNFT.sol:11-12
event FeeStrategyChanged, PoolNFTMinted
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/interfaces/IPoolRegistry.sol:24
event PoolRegistered
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/interfaces/ITreasury.sol:14-15
event Deposited, Withdrawn
```

### 2.2 Meta-AG / PECOR (61 events, 0 routed through EE)

| Interface | Events |
|---|---|
| `IPECOR.sol:136-178` | SimpleSwap, MarketOrderExecuted, NativeSwap, PriceImpactApplied, TieredFeeApplied, SwapFeeUpdated, TieredFeesUpdated, PriceImpactConfigUpdated, FeeCollectorUpdated, PriceOracleUpdated, TransactionTrackerUpdated, FeesCollected **(12)** |
| `IPECOROrders.sol:160-180` | LimitOrderCreated, LimitOrderExecuted, LimitOrderCancelled, StopLimitOrderCreated, StopLimitActivated, StopLimitExecuted, StopLimitCancelled, KeeperUpdated **(8)** |
| `IPECORVault.sol:111-140` | TokenRegistered, StablecoinStatusUpdated, Deposit, Withdrawal, NativeDeposit, NativeWithdrawal, OperatorUpdated, TransactionTrackerUpdated, ReservesUpdated, ReservesSync, EmergencyWithdraw **(11)** |
| `IPriceOracle.sol:200-236` | PriceUpdated, BatchPriceUpdate, TokenRegistered, TokenConfigUpdated, RelayerUpdated, StalePriceDetected **(6)** |
| `IOracleHub.sol:188-209` | AdapterRegistered, AdapterDeactivated, AdapterActivated, AdapterPriorityUpdated, DeviationThresholdUpdated, MinConfidenceUpdated, PrimaryOracleUpdated, CircuitBreakerTriggered, PriceServed **(9)** |
| `IDataFeedAdapter.sol:79-87` | AdapterRegistered, AdapterPriceServed **(2)** |
| `IProtocolAdapter.sol:130-…` | SwapExecuted **(1)** |
| `IMetaAGRouter.sol:158-…` | AdapterRegistered, AdapterDeactivated, AdapterActivated, OracleHubUpdated, OracleSanityDeviationUpdated, OracleSanityEnabledUpdated, BestRouteSwap **(7)** |
| `ITransactionTracker.sol:137-247` | TradeExecuted, MarketTrade, LimitOrderPlaced/Executed/Cancelled, StopLossPlaced/Triggered, StopLimitPlaced/Activated/Executed, LiquidityAdded, LiquidityRemoved, DailyStatsSnapshot, TokenVolumeUpdate, VaultLiquidityUpdate, UserTradeMetrics, AuthorizedEmitterUpdated **(17)** |
| `IWETH.sol:28-29` | Deposit, Withdrawal **(2)** — native WPAX |

### 2.3 Opticals (11 events, 0 routed)
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/opticals/OpticalRegistry.sol:27-29
event OpticalRegistered, OpticalDeregistered, OpticalMetadataUpdated
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/opticals/LaunchpadOpticalFactory.sol:23-30
event LaunchpadOpticalCreated
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/opticals/presets/LaunchpadOptical.sol:37-41
event PoolStartTimeRecorded, SellBlockedCliff, SellBlockedVesting,
      CapitalRaiseAccumulated, CapitalRaiseClaimed
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/opticals/presets/BuybackBurnOptical.sol:29-30
event BuybackAccumulated, BuybackExecuted
```
**Note:** `TaxOptical`, `MaxWalletOptical`, `CooldownOptical`, `AntiSnipeOptical` currently **emit zero events**. They silently trigger side effects. This is a major indexer blind spot Andrew should want plugged.

### 2.4 Protocol / Governance (11 events, 0 routed)
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/protocol/Timelock.sol:22-38
event TransactionQueued, TransactionExecuted, TransactionCancelled,
      ProposerChanged, GuardianChanged
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/protocol/GovernanceModule.sol
event ProposalCreated, VoteCast, ProposalExecuted, ProposalCancelled,
      AdminModeDeactivated
```
```
@/root/sidiora.fun-main-branch/smart-contracts/contracts/protocol/Treasury.sol:37,53
event Deposited, Withdrawn
```

### 2.5 Base infra (ubiquitous — fires from every UUPS proxy)
- `AccessControl`: RoleGranted, RoleRevoked, RoleAdminChanged (every permissioned mutation)
- `ERC1967Utils`: Upgraded, BeaconUpgraded, AdminChanged (every UUPS/beacon upgrade)
- `UpgradeableBeacon`: Upgraded, OwnershipTransferred
- `Pausable`: PauseToggled (pause/unpause)
- `Initializable`: Initialized
- `ERC20Base`: **Transfer, Approval** (every pool token mint/burn/xfer/approval)
- `ERC721Base`: **Transfer, Approval, ApprovalForAll** (every NFT mint/xfer/burn)

### Totals
- **~108 distinct event types** across the surface.
- **~300 `emit` call-sites**.
- **Only ~6** flow through EventEmitter today.
- **Coverage ratio: ≈ 5 %.**

That is the gap.

---

## 3. Architecture — EventEmitter v2

### 3.1 Design principles
1. **Single source of truth** — indexer watches one address; no ABI juggling.
2. **Stable topic0 for hot paths** — don't break existing indexers; v1 typed events stay and gain siblings.
3. **Generic catch-all for cold paths** — adding event #109 next month requires **no contract upgrade**.
4. **Explicit provenance** — every emission records `msg.sender` as the source so indexers can attribute which contract fired without trusting event content.
5. **No native event removal** that wallets/standards depend on (ERC20/ERC721 `Transfer`, `Approval`, ERC1967 `Upgraded`). We **mirror** those, don't delete them.
6. **Auto-authorization**, not manual whitelist maintenance.
7. **Structured block-level metadata** appended to every event so indexers don't re-decode block state (GMX pattern).

### 3.2 Three architectural options considered

| Option | Description | Gas | Indexer ergonomics | Forward-compat | Verdict |
|---|---|---|---|---|---|
| **A. Expand typed methods only** | Add ~100 typed `emitX(...)` functions mirroring every event. | Cheapest. | Excellent — strict ABI. | **Bad** — every new event = proxy upgrade. | ❌ brittle |
| **B. Pure generic schemaless** | One `event EventLog(string name, EventData data, address sender)`, every contract builds the struct. | Highest (string+struct encoding). | Adequate — indexer needs per-name schema map. | **Perfect** — zero upgrades forever. | ⚠ ok but loses indexer fast-path |
| **C. Hybrid (recommended)** | Typed fast-path for hot events (~25) + generic `EventLog{1,2}` catch-all for everything else + ERC20/ERC721 mirrors. | Middle. | Best of both. | Excellent. | ✅ **Pick this** |

**Recommendation: Option C.** This is the exact tradeoff GMX V2 made in their `EventEmitter` and it has survived 3+ years of protocol evolution. Synthetix V3's `EventLog` schema is the same shape.

### 3.3 Proposed `IEventEmitter` v2 surface

Keep all v1 functions (backward-compatible). Add:

```solidity
/// @title IEventEmitter v2 (addendum)
/// @notice Generic schemaless emission + expanded typed surface

// --- Generic schemaless payload (GMX V2 pattern) ---
struct AddressItems  { AddressKeyValue[] items;  AddressArrayKeyValue[] arrayItems; }
struct UintItems     { UintKeyValue[] items;     UintArrayKeyValue[] arrayItems; }
struct IntItems      { IntKeyValue[] items;      IntArrayKeyValue[] arrayItems; }
struct BoolItems     { BoolKeyValue[] items;     BoolArrayKeyValue[] arrayItems; }
struct Bytes32Items  { Bytes32KeyValue[] items;  Bytes32ArrayKeyValue[] arrayItems; }
struct BytesItems    { BytesKeyValue[] items;    BytesArrayKeyValue[] arrayItems; }
struct StringItems   { StringKeyValue[] items;   StringArrayKeyValue[] arrayItems; }

struct EventData {
    AddressItems addressItems;
    UintItems    uintItems;
    IntItems     intItems;
    BoolItems    boolItems;
    Bytes32Items bytes32Items;
    BytesItems   bytesItems;
    StringItems  stringItems;
}

// Three emit variants based on topic count (GMX pattern — saves indexer time)
event EventLog (address msgSender, string eventName, string eventNameHash,
                EventData eventData);
event EventLog1(address msgSender, string eventName, string indexed eventNameHash,
                bytes32 indexed topic1, EventData eventData);
event EventLog2(address msgSender, string eventName, string indexed eventNameHash,
                bytes32 indexed topic1, bytes32 indexed topic2, EventData eventData);

function emitEventLog (string calldata eventName, EventData calldata data) external;
function emitEventLog1(string calldata eventName, bytes32 topic1, EventData calldata data) external;
function emitEventLog2(string calldata eventName, bytes32 topic1, bytes32 topic2, EventData calldata data) external;

// --- Typed fast-path additions (highest-volume events) ---

// Meta-AG / PECOR
function emitPecorSwap(address user, address tokenIn, address tokenOut,
                       uint256 amountIn, uint256 amountOut, uint256 priceIn,
                       uint256 priceOut, uint256 volumeUSD, uint256 feeBps,
                       uint256 feeAmount, uint256 impactBps) external;
function emitPecorOrderCreated(uint8 orderKind, uint256 orderId, address user,
                               address tokenIn, address tokenOut, uint256 amount,
                               uint256 targetPrice, uint256 stopPrice, uint256 limitPrice,
                               bool isBuy) external;
function emitPecorOrderLifecycle(uint8 orderKind, uint8 phase, uint256 orderId,
                                 address user, uint256 price) external;
function emitBestRouteSwap(address user, address tokenIn, address tokenOut,
                           uint256 amountIn, uint256 amountOut, bytes32 routeId,
                           address[] calldata hops, uint256 protocolFeeBps) external;

// Oracle
function emitPriceUpdated(address token, uint256 price, uint256 roundId,
                          address relayer, uint256 confidence, bytes32 sourceId) external;
function emitCircuitBreaker(address token, bytes32 sourceId, uint256 reportedPrice,
                            uint256 referencePrice, uint256 deviationBps) external;

// Vault
function emitVaultFlow(uint8 flowType, address token, address party,
                       uint256 amount, uint256 newReserve) external;
                       // flowType: 0=Deposit 1=Withdrawal 2=NativeDeposit 3=NativeWithdrawal 4=Emergency

// Governance / Timelock
function emitGovernance(uint8 action, bytes32 id, address actor, bytes calldata payload) external;
                       // action: 0=TxQueued 1=TxExecuted 2=TxCancelled
                       //         3=ProposalCreated 4=VoteCast 5=ProposalExecuted 6=ProposalCancelled

// Treasury
function emitTreasuryFlow(uint8 direction, address token, address party, uint256 amount) external;
                       // 0=Deposit 1=Withdraw

// Optical
function emitOpticalLifecycle(uint8 action, address optical, address pool,
                              bytes32 name, bytes calldata payload) external;

// NFT / token transfers (the big ones)
function emitTokenTransfer(address token, address from, address to, uint256 value) external;
function emitNftTransfer(address nft, address from, address to, uint256 tokenId) external;
function emitApproval(address asset, address owner, address spender, uint256 valueOrTokenId) external;

// Access control (attribution of every role change across every UUPS contract)
function emitRoleChange(uint8 action, bytes32 role, address account, address sender) external;
                       // 0=Granted 1=Revoked 2=AdminChanged

// Upgrades (attribution of every UUPS / beacon upgrade)
function emitUpgraded(address proxy, address newImpl, uint8 kind) external;
                       // kind: 0=UUPS 1=Beacon 2=AdminChanged

// Pause toggles
function emitPauseToggle(address pausedContract, bool paused) external;
```

Estimated final `IEventEmitter` size: ~40 typed functions + 3 generic variants + existing v1 surface = **~50 function signatures**. File length: ~400 lines. Well under the 800-line hard cap.

### 3.4 Authorization mesh

Current auth is a static `mapping(address => bool) _authorizedEmitters` plus a dynamic lookup via `poolRegistry.isRegisteredPool`. Extend it to:

```
┌─────────────────────────────────────────────────────────────────┐
│                 EventEmitter v2 authorization                    │
├─────────────────────────────────────────────────────────────────┤
│  Authorized if:                                                  │
│   (1) _authorizedEmitters[msg.sender] == true      [static]      │
│   (2) PoolRegistry.isRegisteredPool(msg.sender)    [dyn Launchpad]│
│   (3) OpticalRegistry.isApproved(msg.sender)       [dyn opticals] │
│   (4) MetaAGRouter.isAdapter(msg.sender)           [dyn adapters] │
│   (5) tokenRegistry[msg.sender] != address(0)      [dyn ERC20s]   │
│   (6) SidioraFactory.isDeployedPoolToken(msg.sender) [CREATE2 verify]│
└─────────────────────────────────────────────────────────────────┘
```

Static set (one-time admin setup after v2 upgrade): Factory, Router, Quoter, FeesRouter, FeeAccumulator, PoolRegistry, OpticalRegistry, LaunchpadOpticalFactory, Treasury, Timelock, GovernanceModule, ProtocolConfig, PECOR, PECOROrders, PECORVault, OracleHub, PriceOracle, MetaAGRouter, MetaAGQuoter, TransactionTracker, SidioraFeedAdapter, PriceOracleAdapter, WPAX, SidioraNFT — **~24 addresses**.

Dynamic sets cover every future pool, pool-token, optical, adapter without another admin tx.

### 3.5 Block metadata enrichment

Every event already captures `block.timestamp` and `block.number` today. Add:
- `uint256 logIndex` — **NOT available on-chain**. Skip. Indexer computes from receipt.
- `bytes32 txOrigin` — `tx.origin` packed with `msg.sender` — already exposed indirectly but surfaces user attribution cheaply for meta-transactions. **Add as optional `sender`/`origin` pair in `EventLog*`.**

---

## 4. Per-Contract Wire-up Plan

For each contract, the change is a **one-line injection** after every state mutation. Native events stay unless noted.

### Phase 1 — Touch nothing structural, add EE hooks
- `core/SidioraPool.sol` → already wired. Add `emitReserveCheckpoint` on every `_update`. **[+ 3 LOC]**
- `core/SidioraNFT.sol` → add `emitNftTransfer`, `emitFeeStrategyChanged`. **[+ 5 LOC]**
- `core/SidioraERC20.sol` → override `_update` / `_approve` hooks, mirror Transfer/Approval to EE. **[+ 8 LOC]**
- `core/SidioraFactory.sol` → already wires `emitMarketCreated`. Add `emitTokenDeployed(tokenAddr, creator, salt)` pre-NFT. **[+ 3 LOC]**
- `data/PoolRegistry.sol` → mirror `PoolRegistered`. **[+ 3 LOC]**
- `data/FeeAccumulator.sol` → mirror all 9 events. **[+ 27 LOC]**
- `periphery/Router.sol` → mirror `Buy`, `Sell`, `MultihopSwap` (MarketCreated duplicate can stay — de-dupe is indexer-side). **[+ 12 LOC]**
- `periphery/FeesRouter.sol` → mirror all 6 events. **[+ 18 LOC]**
- `protocol/Treasury.sol` → `emitTreasuryFlow(0/1, token, party, amount)`. **[+ 6 LOC]**
- `protocol/Timelock.sol` → `emitGovernance(0/1/2, txHash, target, payload)`. **[+ 9 LOC]**
- `protocol/GovernanceModule.sol` → `emitGovernance(3/4/5/6, proposalId, actor, payload)`. **[+ 12 LOC]**
- `protocol/ProtocolConfig.sol` → already wired.

### Phase 2 — Meta-AG / PECOR
All 9 contracts gain `address public eventEmitter;` storage slot (append-only for storage safety per the freeze lock §S12) + setter + emissions:
- `meta-ag/engine/PECOR.sol` → `emitPecorSwap` on every swap variant, `emitTieredFee`, `emitPriceImpact`, `emitConfigUpdated` variants. **[+ 30 LOC]**
- `meta-ag/engine/PECOROrders.sol` → `emitPecorOrderCreated` / `emitPecorOrderLifecycle` across all order events. **[+ 20 LOC]**
- `meta-ag/vault/PECORVault.sol` → `emitVaultFlow` across Deposit/Withdrawal/Native*/Emergency/ReservesSync. **[+ 25 LOC]**
- `meta-ag/oracle/PriceOracle.sol` → `emitPriceUpdated` on every `pushPrice`, `emitBatchPriceUpdate`, `emitStalePriceDetected`. **[+ 20 LOC]**
- `meta-ag/oracle/OracleHub.sol` → `emitCircuitBreaker`, `emitAdapterLifecycle`, `emitPriceServed`. **[+ 18 LOC]**
- `meta-ag/oracle/adapters/*.sol` → `emitAdapterPriceServed`. **[+ 6 LOC each × 2]**
- `meta-ag/router/MetaAGRouter.sol` → `emitBestRouteSwap`, adapter lifecycle. **[+ 15 LOC]**
- `meta-ag/analytics/TransactionTracker.sol` → 17 typed events → **generic `emitEventLog*` with schema `TrackerEvent.v1`**. Gets us all 17 in one surface without bloating the typed interface. **[+ 20 LOC total]**
- `meta-ag/adapters/SidioraAdapter.sol`, `VaultAdapter.sol` → `emitAdapterSwap`. **[+ 4 LOC each]**

### Phase 3 — Opticals
- `opticals/OpticalRegistry.sol` → `emitOpticalLifecycle` across Register/Deregister/MetadataUpdated. **[+ 9 LOC]**
- `opticals/LaunchpadOpticalFactory.sol` → `emitOpticalLifecycle(DEPLOY, optical, address(0), name, creationParams)`. **[+ 3 LOC]**
- `opticals/presets/LaunchpadOptical.sol` → mirror all 5 events. **[+ 15 LOC]**
- `opticals/presets/BuybackBurnOptical.sol` → mirror both events. **[+ 6 LOC]**
- **NEW emissions** for `TaxOptical`, `MaxWalletOptical`, `CooldownOptical`, `AntiSnipeOptical` — these silently trigger effects today. Each needs ≥ 1 event: `TaxApplied`, `MaxWalletEnforced`, `CooldownEnforced`, `SnipeBlocked`. **[+ 4 LOC each]**

### Phase 4 — Base-layer enrichment (cross-cutting)
These require changes in `base/*` which ripple across every contract. Do LAST.
- `base/AccessControl.sol` → optional `_emitRoleChange` hook if `eventEmitter` set. **[+ 8 LOC]**
- `base/UUPSUpgradeable.sol` + `base/ERC1967Utils.sol` → optional `_emitUpgraded` hook in `_upgradeToAndCall`. **[+ 8 LOC]**
- `base/Pausable.sol` → optional `_emitPauseToggle`. **[+ 6 LOC]**
- `base/UpgradeableBeacon.sol` → same. **[+ 4 LOC]**

These hooks are **opt-in** — they fire only when the descendant contract has set `eventEmitter != address(0)`. That keeps base contracts stateless and prevents breakage of test fixtures.

### Phase 5 — Pool token transfer mirror (HIGH VALUE)
`SidioraERC20._update` (or whatever hook path we have) fires `emitTokenTransfer(address(this), from, to, value)`. This lets an indexer subscribed to a single EventEmitter address receive **every transfer of every ever-launched pool token** with zero additional address subscriptions. Closes Q-Impl-4 from the indexer-v2 open questions list in one shot.

### Phase 6 — NFT transfer mirror
Same idea for `SidioraNFT._update`. Every pool-fee-rights transfer becomes indexable from the EventEmitter stream.

### Phase 7 — Third-party ERC20s (USDL, WPAX, USDC, USDT, SID)
These are not ours to modify. The indexer **still** has to watch their native `Transfer` — unless we deploy a `TokenTransferProxy` that wraps every movement and fires EE, which is overkill. **Out of scope for v2.** Document clearly.

---

## 5. Migration Sequence (ordered, atomic)

```
Stage 0  Author EventEmitter v2 impl  +  IEventEmitter v2 interface  + storage gap
Stage 1  Unit tests for EE v2 (typed + generic + auth mesh)
Stage 2  Deploy EE v2 impl, UUPS upgrade via Timelock   (proxy stays 0x6679…)
Stage 3  Admin tx: setAuthorizedEmitter × 24 static addresses
Stage 4  Admin tx: setPoolRegistry (already done in v1) + setOpticalRegistry +
         setMetaAGRouter + setTokenRegistry + setSidioraFactory
Stage 5  Per-contract wire-ups (Phase 1–3 above). Each is its own UUPS upgrade,
         atomic per contract, rollback-safe. Use feature flag:
         `if (eventEmitter != address(0)) IEventEmitter(eventEmitter).emit*(...)`
Stage 6  Phase 5 / 6 token + NFT mirrors (SidioraERC20/SidioraNFT impl upgrade)
Stage 7  Phase 4 base-layer enrichment (opt-in; no behavior change unless EE set)
Stage 8  Indexer schema migration: add EventLog* handlers to go-indexer decoder;
         deprecate per-address subscriptions one contract at a time; prove parity;
         finally cut over to EventEmitter-only ingest.
```

Every stage is **rollback-safe** (UUPS impl swap, no storage layout changes since we append-only a few slots). Stages 5-7 can proceed in any intra-phase order.

---

## 6. Testing Strategy

Following the workspace TDD rules (80 % coverage on new code):

### Unit — `test/EventEmitterV2.t.sol` (new)
- Typed function signatures match IEventEmitter v2 interface.
- Generic `emitEventLog{,1,2}` round-trips EventData payloads losslessly.
- Auth mesh: reject non-authorized, accept every static + dynamic path. Include negative test for each path.
- ERC20/ERC721 mirror: confirm `msg.sender` in event payload matches actual caller.
- Storage layout: hardhat-storage-layout diff vs v1 — only append.

### Integration — scenario tests
1. **Full launch flow** — Factory.createMarket → pool swap → NFT mint → fee claim. Assert every state change appears exactly once in EE stream (no duplicates, no gaps).
2. **PECOR flow** — swapExactIn + order place/cancel/execute. Same parity assertion.
3. **Oracle flow** — price push + circuit breaker trigger. Same.
4. **Governance flow** — propose → vote → execute → timelock queue/execute. Same.
5. **Pool token life** — mint → transfer → burn. Assert every `Transfer` has a matching `emitTokenTransfer` record.

### Indexer integration (go-indexer)
- Fork PG DB, replay last 1000 blocks twice:
  - Once with current multi-address ingest.
  - Once with EventEmitter-only ingest.
- Assert row-count parity on every typed table (except address-filtered `token_transfers`, which should **increase** since we now capture pool-token transfers the v1 filter skipped).

---

## 7. Locked Decisions (Andrew, 2026-05-01)

All open questions resolved. Decisions baked into Stage 0.

| # | Topic | Decision |
|---|-------|----------|
| D1 | **Schema governance** | **Andrew owns the schema** — sole authority. Path TBD by Andrew. Stage 0 emits `eventNameHash = keccak256(eventName)` indexed so the indexer can filter on `topic1` without parsing the string. |
| D2 | **Active indexer** | `backend/go-indexer/` is **deprecated**. Active indexer is the **TypeScript** stack at `@/root/sidiora.fun-main-branch/backend/indexer/` (drizzle + viem + fastify, package `@sidiora/indexer@1.0.0`). All earlier `sidiora-indexer-v2` references in this doc are obsolete; ingest plumbing happens in the TS indexer. |
| D3 | **Storage slots** | Approved. `address public eventEmitter;` slot added to each Meta-AG contract by shrinking `__gap[50] → __gap[49]`. Storage-layout diff required on every impl upgrade. |
| D4 | **Gas budget** | Confirmed unbounded. Optimize for indexer simplicity. |
| D5 | **MEV / fake-event injection** | **No MEV on the network** (consensus-level fair ordering, ~133ms blocks, single-slot finality). Front-running fake-event injection is not a credible vector. Schema-level `msg.sender` pinning still recorded as defense-in-depth. |
| D6 | **Native event retention** | Mirror first. Strip launchpad-specific natives (Buy, Sell, MultihopSwap, FeeRecorded, FeesClaimed, AirdropTriggered, etc.) **30 days after** EE mirror is proven in production. ERC20/ERC721 `Transfer`/`Approval`, ERC1967 `Upgraded`, AccessControl `RoleGranted`/`RoleRevoked` stay forever. |
| D7 | **`VERSION()` accessor** | **WIRED IN.** `EventEmitter.VERSION()` returns `"2.0.0"`. |
| D8 | **`EVENT_EMITTER_ROLE` separation** | **WIRED IN.** Distinct from `DEFAULT_ADMIN_ROLE`. Holds permission for `setAuthorizedEmitter` and registry wiring; admin role retains upgrade authority. Timelock can delegate `EVENT_EMITTER_ROLE` without granting upgrade rights. |
| D9 | **Indexed `eventNameHash`** | **WIRED IN.** Generic `EventLog*` events emit `bytes32 indexed eventNameHash = keccak256(bytes(eventName))` so indexers filter on `topic1` without string decoding. |

---

## 8. Files Touched (full list if all phases execute)

```
contracts/interfaces/IEventEmitter.sol                              [EXPANDED]
contracts/data/EventEmitter.sol                                     [EXPANDED → v2]
contracts/core/SidioraPool.sol                                      [+ reserve checkpoint]
contracts/core/SidioraNFT.sol                                       [+ mirror]
contracts/core/SidioraERC20.sol                                     [+ transfer/approval mirror]
contracts/core/SidioraFactory.sol                                   [+ token deploy event]
contracts/data/PoolRegistry.sol                                     [+ mirror]
contracts/data/FeeAccumulator.sol                                   [+ 9 mirrors]
contracts/periphery/Router.sol                                      [+ 3 mirrors]
contracts/periphery/FeesRouter.sol                                  [+ 6 mirrors]
contracts/protocol/Treasury.sol                                     [+ flow events]
contracts/protocol/Timelock.sol                                     [+ governance]
contracts/protocol/GovernanceModule.sol                             [+ governance]
contracts/meta-ag/engine/PECOR.sol                                  [+ emitter + ~6 emissions]
contracts/meta-ag/engine/PECOROrders.sol                            [+ emitter + ~8 emissions]
contracts/meta-ag/vault/PECORVault.sol                              [+ emitter + ~11 emissions]
contracts/meta-ag/oracle/PriceOracle.sol                            [+ emitter + ~6 emissions]
contracts/meta-ag/oracle/OracleHub.sol                              [+ emitter + ~9 emissions]
contracts/meta-ag/oracle/adapters/PriceOracleAdapter.sol            [+ emitter + mirror]
contracts/meta-ag/oracle/adapters/SidioraFeedAdapter.sol            [+ emitter + mirror]
contracts/meta-ag/router/MetaAGRouter.sol                           [+ emitter + ~7 emissions]
contracts/meta-ag/quoter/MetaAGQuoter.sol                           [+ emitter, emit on quote]
contracts/meta-ag/analytics/TransactionTracker.sol                  [+ emitter + generic mirror]
contracts/meta-ag/adapters/SidioraAdapter.sol                       [+ emitter]
contracts/meta-ag/adapters/VaultAdapter.sol                         [+ emitter]
contracts/opticals/OpticalRegistry.sol                              [+ mirror]
contracts/opticals/LaunchpadOpticalFactory.sol                      [+ mirror]
contracts/opticals/presets/LaunchpadOptical.sol                     [+ 5 mirrors]
contracts/opticals/presets/BuybackBurnOptical.sol                   [+ 2 mirrors]
contracts/opticals/presets/TaxOptical.sol                           [+ NEW events]
contracts/opticals/presets/MaxWalletOptical.sol                     [+ NEW events]
contracts/opticals/presets/CooldownOptical.sol                      [+ NEW events]
contracts/opticals/presets/AntiSnipeOptical.sol                     [+ NEW events]
contracts/base/AccessControl.sol                                    [+ opt-in hook]
contracts/base/UUPSUpgradeable.sol                                  [+ opt-in hook]
contracts/base/ERC1967Utils.sol                                     [+ opt-in hook]
contracts/base/Pausable.sol                                         [+ opt-in hook]
contracts/base/UpgradeableBeacon.sol                                [+ opt-in hook]
contracts/test/*                                                    [new + updated suites]
```

**~37 Solidity files edited; no deletions; ~400 net new LOC (mostly one-liners).**

---

## 9. What NOT to Touch (Chesterton's Fence)

- **Freeze locks** under `contracts/{core,data,meta-ag,opticals,periphery,protocol}/*.freeze.lock`. These are audit provenance. Refresh them at the END of the migration, not during.
- **ERC20/ERC721 native `Transfer` / `Approval` emissions**. Removal would break wallets, block explorers, off-the-shelf tooling.
- **UUPS `Upgraded` native emission**. OpenZeppelin tooling and block explorers key off it for upgrade history.
- **`AccessControl` native role events**. Same reason — tooling expects the standard signature.
- **Meta-AG storage layouts**. `__gap[50]` slots exist for exactly this kind of append. Respect them.
- **PECOR spec invariants S1, S10, S11, S12** (roles, fee caps, storage append-only). The plan does not violate any of these.

---

## 10. Deliverables Checklist

- [ ] `IEventEmitter.sol` v2 — interface expansion with typed + generic surface.
- [ ] `EventEmitter.sol` v2 — impl with auth mesh + event routing.
- [ ] `test/EventEmitterV2.t.sol` — full suite (unit + integration + fuzz on generic payloads).
- [ ] Deployment script — UUPS upgrade via Timelock on proxy `0x6679aF411d534de222C32ed0AF94C3BD67090672`.
- [ ] Authorization script — set 24 static emitters + wire 4 registries.
- [ ] Per-contract wire-up PRs (Phases 1–4) — one PR per domain for reviewability.
- [ ] `backend/go-indexer/event-schemas/*.v1.json` — generic-event schema registry.
- [ ] Go indexer decoder updates — add `EventLog{,1,2}` handlers + schema dispatcher.
- [ ] Retired-native event list — document in `EVENT-MIGRATION.md` when each native emission is removed.
- [ ] Updated `meta-ag.freeze.lock` and sibling locks with new EE-routed events.
- [ ] Updated `sidiora-indexer-v2` project state + session log.

---

**End of plan.** Ready for Andrew review before Stage 0.
