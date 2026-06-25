# EventEmitter v2 — Stage 2-7 Handoff Prompt

> Copy the **`---PROMPT START---` to `---PROMPT END---`** block below into a new Cascade session verbatim. The agent has zero context from the prior session and must self-bootstrap from this prompt + on-disk artifacts.

---PROMPT START---

# Mission

You are picking up an in-flight cross-cutting upgrade of the **Sidiora / Paxeer EventEmitter** from a UUPS proxy at `0x6679aF411d534de222C32ed0AF94C3BD67090672`. The v1→v2 contract surface is **already shipped, fully tested (1178/1178 passing), and committed**. Your job is to drive **Stages 2 through 7** to production — the deployment script, per-domain wire-ups, token-mirror hooks, and indexer schema handoff.

You are **Andrew's agent**. Andrew built Paxeer Network — HyperPaxeer is the high-throughput EVM chain at the core (Chain ID 125, ~133 ms blocks, single-slot finality, Cosmos SDK + CometBFT). The protocol surface includes Sidiora Launchpad (token-launch + AMM-curve trading), Meta-AG (price routing + PECOR perpetual orderbook), and the Argus VM risk engine. Indexer plumbing terminates in `backend/indexer/` (TypeScript + drizzle + viem + fastify).

# Boot sequence — non-negotiable, every session

Before reading anything else, in this exact order:

1. `@/root/sidiora.fun-main-branch/.windsurf/AGENTS.md` — agent charter
2. `@/root/sidiora.fun-main-branch/.windsurf/INDEX.md` — tool catalog
3. `@/root/sidiora.fun-main-branch/.windsurf/SESSION-PROTOCOL.md` — start/during/end ritual
4. `@/root/sidiora.fun-main-branch/.windsurf/memory/index.md` — prior memory
5. `@/root/sidiora.fun-main-branch/.windsurf/rules/AndrewsProfile.md` — Andrew's profile + Paxeer architecture

Then run the recall step from `AGENTS.md` §2.1 (last-5-trajectories digest at `.windsurf/memory/hot_path_tmp/session_recall.md`).

Only after boot is complete, read these EE-v2 specific artifacts:

- `@/root/sidiora.fun-main-branch/smart-contracts/contracts/data/EVENT-EMITTER-V2-PLAN.md` (the canonical plan — sections 1-9)
- `@/root/sidiora.fun-main-branch/smart-contracts/contracts/interfaces/IEventEmitter.sol` (v2 interface, complete)
- `@/root/sidiora.fun-main-branch/smart-contracts/contracts/data/EventEmitter.sol` (v2 impl, complete)
- `@/root/sidiora.fun-main-branch/smart-contracts/test/data/EventEmitterV2.test.js` (56 v2 tests)
- `@/root/sidiora.fun-main-branch/smart-contracts/test/data/EventEmitter.test.js` (18 v1 tests, all green against v2)

# State of the world — what is DONE

| Artifact | Status |
|---|---|
| Plan document with locked decisions D1–D9 | **Done** |
| `IEventEmitter` v2 interface (~700 lines) | **Done** |
| `EventEmitter` v2 impl with append-only storage, 5-path auth mesh, `VERSION()`, `EVENT_EMITTER_ROLE`, `reinitializeV2` | **Done** |
| `MockPoolRegistry` + `MockOpticalRegistry` test fixtures | **Done** |
| 56 v2 unit tests + 18 v1 backward-compat tests = 74 EE tests, all green | **Done** |
| Full protocol suite: 1178/1178 passing | **Done** |

What you must NOT touch:
- The v1 portion of `IEventEmitter.sol` (lines 1–122). Selectors and topic0 are immutable.
- The v1 emit functions in `EventEmitter.sol`. Same reason.
- Storage slots 0, 1, 2 (consumed by `_roles`, `_authorizedEmitters`, `poolRegistry` respectively). v2 storage is append-only from slot 3.
- Any `*.freeze.lock` file mid-stage. They are audit provenance markers — refresh ONLY at the end of all stages, in one final coordinated PR.
- The deprecated paths: `backend/go-indexer/`, `.windsurf/memory/projects/sidiora-indexer-v2/`. Don't read them, don't reference them.

# Andrew's locked decisions — already baked in

| # | Decision |
|---|---|
| **D1** | Andrew owns the event-schema registry. Generic `EventLog*` events emit `bytes32 indexed eventNameHash = keccak256(eventName)` so the TS indexer filters on topic1 without string decoding. |
| **D2** | Active indexer is `@/root/sidiora.fun-main-branch/backend/indexer/`. The Go indexer is dead. |
| **D3** | Storage layout changes are approved (append-only). Every Meta-AG impl that gains an `address public eventEmitter` slot must shrink `__gap[50] → __gap[49]` and have its layout verified via `pnpm meta-ag:layout:check`. |
| **D4** | Gas is free on HyperPaxeer. Optimize for indexer simplicity, not call cost. |
| **D5** | MEV is not a credible vector (consensus-level fair ordering, 133 ms blocks). Schema-level `msg.sender` pinning is still recorded as defense-in-depth. |
| **D6** | Mirror-first native event retention. Strip launchpad-specific natives (Buy / Sell / MultihopSwap / FeeRecorded / FeesClaimed / AirdropTriggered, etc.) **30 days after** EE mirror is proven in production. ERC20/ERC721 `Transfer`/`Approval`, ERC1967 `Upgraded`, AccessControl `RoleGranted`/`RoleRevoked` **stay forever**. |
| **D7** | `VERSION()` accessor returns `"2.0.0"`. Wired. |
| **D8** | `EVENT_EMITTER_ROLE = keccak256("EVENT_EMITTER_ROLE")` is distinct from `DEFAULT_ADMIN_ROLE`. Holders can authorize emitters and wire registries; only `DEFAULT_ADMIN_ROLE` can upgrade the impl. Wired. |
| **D9** | Indexed `eventNameHash` on all generic events. Wired. |

Do not re-litigate any of these. They are final.

# Stages remaining

You execute these sequentially. Each stage ends with a green test gate before you move to the next.

## Stage 2 — Deployment & migration scripts

**Goal**: ship a Timelock-routed upgrade for the deployed proxy `0x6679aF411d534de222C32ed0AF94C3BD67090672` that swaps in the new v2 impl AND atomically calls `reinitializeV2(adminWithEmitterRole)` in the same transaction.

**Inputs you need from Andrew before deploying to mainnet**:
- The Timelock contract address on HyperPaxeer.
- The address that should hold `EVENT_EMITTER_ROLE` post-upgrade (likely the multisig or governance executor; ask if unclear).
- The current ERC1967 implementation slot value of the proxy (read with `cast storage <proxy> 0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc` or via Hardhat).

**Deliverables**:

1. `@/root/sidiora.fun-main-branch/smart-contracts/scripts/event-emitter-v2/01-deploy-impl.js`
   - Deploys a fresh `EventEmitter` implementation contract (NOT a proxy). Records its address to `deployments/<network>/EventEmitterV2Impl.json`.
   - Uses `hardhat-deploy` if present, otherwise raw ethers v6.
   - Verifies on the explorer post-deploy.

2. `@/root/sidiora.fun-main-branch/smart-contracts/scripts/event-emitter-v2/02-queue-upgrade.js`
   - Reads the deployed v2 impl address from step 1.
   - Encodes the `upgradeToAndCall(newImpl, abi.encodeCall(EventEmitter.reinitializeV2, (adminWithEmitterRole)))` calldata.
   - Submits a `queueTransaction` to the Timelock with `eta = now + minDelay + buffer`.
   - Saves the queued tx ID to `deployments/<network>/EventEmitterV2UpgradeQueued.json`.

3. `@/root/sidiora.fun-main-branch/smart-contracts/scripts/event-emitter-v2/03-execute-upgrade.js`
   - After Timelock delay elapses, executes the queued tx.
   - Post-execution: asserts `VERSION() == "2.0.0"`, `hasRole(EVENT_EMITTER_ROLE, adminWithEmitterRole) == true`.

4. `@/root/sidiora.fun-main-branch/smart-contracts/scripts/event-emitter-v2/04-wire-registries.js`
   - From `EVENT_EMITTER_ROLE` holder: calls `setOpticalRegistry`, `setMetaAGRouter`, `setSidioraFactory` with their canonical addresses (read from `deployments/`).
   - Re-runs auth probes: confirms factory + a sample registered pool can emit through `EventEmitter` without further admin steps.

5. `@/root/sidiora.fun-main-branch/smart-contracts/test/data/EventEmitterV2-upgrade.test.js`
   - **Fork test** of the upgrade flow on a forked HyperPaxeer state.
   - Asserts: v1 storage values (any registered emitters, any set poolRegistry) survive intact across the impl swap.
   - Asserts: post-`reinitializeV2`, the new auth paths (opticalRegistry, sidioraFactory, registeredTokens) work.

**Verification gate before proceeding to Stage 5**:
```bash
cd /root/sidiora.fun-main-branch/smart-contracts
npx hardhat test test/data/EventEmitter.test.js test/data/EventEmitterV2.test.js test/data/EventEmitterV2-upgrade.test.js
# expect: 74+ passing, 0 failing
npx hardhat test
# expect: 1178+ passing, 0 failing — same baseline or higher
```

## Stage 5 — Wire v2 emission across ~37 contracts

This is the bulk of remaining work. Break it into FOUR domain-PRs, executed in this order. Each PR ends with `npx hardhat test` green.

For every contract you modify in this stage:

1. **Add storage**: `address public eventEmitter;` at the next available slot. If the contract uses a `__gap[50]`, shrink it to `__gap[49]`.
2. **Add setter**: `function setEventEmitter(address _emitter) external onlyRole(DEFAULT_ADMIN_ROLE)` (or whatever admin role the contract uses). Setter must reject zero address.
3. **Add feature flag**: every emit call wraps in `if (eventEmitter != address(0)) { IEventEmitter(eventEmitter).emit*(...); }`. This makes mid-migration state safe — contracts whose `eventEmitter` slot hasn't been wired yet silently no-op.
4. **Mirror, do not replace**: leave the existing native event in place. Add EE emission alongside. The 30-day strip per D6 is a separate future task.
5. **Storage layout check** (Meta-AG only): `pnpm meta-ag:layout` BEFORE changes (snapshot), then `pnpm meta-ag:layout:check` AFTER (verify no slot breakage).

### Stage 5a — Meta-AG / PECOR domain

Touch these (verify exact list with `find contracts/meta-ag -name '*.sol' | grep -v interfaces`):

- `contracts/meta-ag/router/MetaAGRouter.sol` → emit `RouterTrade`, `BestRouteSwap` via EE.
- `contracts/meta-ag/quoter/MetaAGQuoter.sol` → no events expected; skip if read-only. Verify by grepping for `emit `.
- `contracts/meta-ag/pecor/PECOR.sol` → emit `PecorSwap`, `PecorOrderLifecycle` via EE.
- `contracts/meta-ag/pecor/PECORVault.sol` → emit `VaultFlow` via EE for deposits/withdrawals.
- `contracts/meta-ag/pecor/PECOROrders.sol` → emit `PecorOrderCreated`, `PecorOrderLifecycle` via EE.
- `contracts/meta-ag/oracle/OracleHub.sol`, `contracts/meta-ag/oracle/PriceOracle.sol` → emit `PriceUpdated`, `CircuitBreakerTriggered`, `OracleAdapterLifecycle` via EE.
- `contracts/meta-ag/transactions/TransactionTracker.sol` → emit via the generic `emitEventLog*` since the schema is bespoke.
- All adapters under `contracts/meta-ag/adapters/` → emit lifecycle changes via `emitEventLog1` keyed on `keccak256("AdapterStateChange")`.

Verification: `npx hardhat test --grep meta-ag` and `pnpm meta-ag:layout:check`. Both must be green.

### Stage 5b — Launchpad mirror domain

- `contracts/data/FeeAccumulator.sol` → emit `FeeFlow` for `FeeRecorded`, `FeesClaimed`, `Burned`, `AirdropTriggered`, `AirdropClaimed`, `LpRewardsSent`, `ProtocolFeeSwept`, `OpticalSurplusRecorded`, `OpticalSurplusClaimed`. The `kind` enum positions are documented in `IEventEmitter.sol:573-585`.
- The MarketRouter / MultihopSwap entrypoint (find with `grep -rn 'event Buy\|event Sell\|event MultihopSwap' contracts/`) → emit `RouterTrade`.
- `contracts/core/SidioraNFT.sol` → emit `NftMint` on mint.
- `contracts/data/PoolRegistry.sol` → emit `PoolRegistered` mirror.
- `contracts/core/SidioraFactory.sol` → emit `TokenDeployed` mirror after CREATE2 deploy.

Verification: `npx hardhat test --grep launchpad` and `npx hardhat test test/core test/data` green.

### Stage 5c — Opticals domain (net-new observability)

These four currently emit **zero events**. Adding EE emission is a pure win:

- `contracts/opticals/presets/TaxOptical.sol` → emit `OpticalLifecycle` with `action=5` (Triggered) + payload encoding the tax amount + recipient.
- `contracts/opticals/presets/MaxWalletOptical.sol` → emit `OpticalLifecycle` with `action=5` + payload encoding the wallet + balance + cap.
- `contracts/opticals/presets/CooldownOptical.sol` → emit `OpticalLifecycle` with `action=5` + payload encoding the user + last trade timestamp.
- `contracts/opticals/presets/AntiSnipeOptical.sol` → emit `OpticalLifecycle` with `action=5` + payload encoding the block delta + sniper address.

Verification: `npx hardhat test test/opticals` green; new tests assert events fire on each hook trigger.

### Stage 5d — Governance / Treasury / Lifecycle domain

- `contracts/governance/Timelock.sol` → emit `Governance` for `TxQueued` (action=0), `TxExecuted` (action=1), `TxCancelled` (action=2). The `payload` field carries `abi.encode(target, value, data, eta)`.
- `contracts/governance/Governance.sol` (if exists) → `ProposalCreated` (action=3), `VoteCast` (4), `ProposalExecuted` (5), `ProposalCancelled` (6), `ProposerChanged` (7), `GuardianChanged` (8), `AdminModeDeactivated` (9).
- `contracts/protocol/Treasury.sol` → emit `TreasuryFlow` for deposit/withdraw.
- `contracts/protocol/ProtocolConfig.sol` → already wired in v1; verify no duplication.
- Any contract using `Pausable` → emit `PauseToggle` mirroring native `Paused`/`Unpaused`.

Verification: `npx hardhat test test/protocol test/governance` green; full suite at end of stage.

## Stage 6 — Token + NFT transfer mirrors (HIGH RISK)

**The trap**: `SidioraERC20` is CREATE2-deployed by `SidioraFactory`. Modifying its bytecode CHANGES every derived token address. Andrew almost certainly has off-chain tooling, indexer state, frontend cache, or external integrations that depend on existing addresses. **Do not unilaterally subclass or modify `SidioraERC20.sol` without explicit Andrew sign-off.**

Recommended approach for Stage 6:

### Option B (preferred): Pool-mediated mirror

In `contracts/core/SidioraPool.sol`:
- After every successful buy/sell, emit `IEventEmitter.emitTokenTransfer(token, from, to, amount)` for each leg. This catches every transfer that flows through the pool's swap surface.
- Limitation: P2P transfers (`token.transfer(friend, ...)`) are still only visible via the native ERC20 `Transfer` event. The TS indexer at `backend/indexer/` already subscribes to those, so this is fine.

### Option A (defer to v3): Token bytecode change

If Andrew approves a token-bytecode-revision program:
- Subclass `ERC20Base` in a new `SidioraERC20V2` (or override `_transfer`/`_approve`/`_mint`/`_burn`).
- Bump the salt domain in `SidioraFactory` so new tokens deploy at fresh addresses.
- Old tokens keep emitting only native events — irreversible by design.
- This is a SEPARATE program from EE v2. Do not bundle.

### NFT path is safe

`SidioraNFT` is non-CREATE2, owned by `SidioraFactory`. Direct modification is safe:
- Override `_update` (or whatever the transfer hook is in `ERC721Base`) to emit `IEventEmitter.emitNftTransfer(address(this), from, to, tokenId)` after the native `Transfer`.
- Verify with `npx hardhat test test/core/SidioraNFT.test.js`.

Verification gate for Stage 6: full launchpad integration suite green AND a new test file `test/integration/EventEmitterV2-token-mirror.test.js` proves that swap-flow Transfer events are mirrored.

## Stage 7 — Base-layer hooks (DEFERRED)

`AccessControl` `RoleChange` mirrors and `UUPSUpgradeable` `Upgraded` mirrors are net-good but invasive (every contract inheriting these would need wiring). Cost-benefit favors deferral to a v2.1 task. Do NOT execute Stage 7 unless Andrew explicitly asks.

If you do execute it later: make the hook **opt-in** via a `_eeNotifyRoleChange` internal function that subclasses can override — never hard-coded into `AccessControl.sol`.

## Schema files for the indexer

After Stage 5 is complete, generate one JSON schema file per typed event under a path Andrew specifies. Example structure:

```json
{
  "name": "PecorSwap",
  "version": 1,
  "topic0": "0x...",
  "expectedSender": ["MetaAGRouter", "PECOR"],
  "fields": [
    { "name": "user", "type": "address", "indexed": true },
    { "name": "tokenIn", "type": "address", "indexed": true },
    ...
  ]
}
```

Do NOT pick the storage location yourself. Ask Andrew. Per D1, he owns this.

# Critical operational constraints

1. **Pre-action protocol — every non-trivial action**:
   ```
   DOING:    [what I'm about to do — concrete]
   EXPECT:   [predicted outcome — observable]
   IF WRONG: [what the mismatch means]
   ```
   Execute. Compare. **Mismatch = stop and surface immediately.**

2. **Reality checks**: max 3 actions between checking observable output. Don't trust the narrative.

3. **Test gate is sacred**: 1178 tests is the baseline. Every PR ends green. Number can only go UP.

4. **Storage layout discipline**: Meta-AG contracts have `pnpm meta-ag:layout` and `pnpm meta-ag:layout:check`. Run them around every layout-affecting edit.

5. **Freeze locks are not yours to refresh**: leave `*.freeze.lock` files alone until the very last coordinated PR after all stages complete.

6. **Spec invariants**: respect S1, S10, S11, S12 (see `specs/` if it exists). Append-only storage is S12.

7. **Auth model**: `EVENT_EMITTER_ROLE` for emitter / registry wiring, `DEFAULT_ADMIN_ROLE` for upgrades. Don't conflate.

8. **`onlyAuthorized` on EE — try/catch the dynamic registry calls**: the v2 impl already wraps `IPoolRegistry.isRegisteredPool` and `IOpticalRegistry.isApproved` in try/catch so a misconfigured registry can never brick emission. Any new dynamic auth paths you add MUST do the same.

9. **MockPoolRegistry / MockOpticalRegistry pattern**: when you add new auth paths, follow the existing test fixture pattern in `contracts/test/Mock*.sol`. One file per registry, ~15 lines each.

10. **Never delete or weaken existing tests.** Tests are immutable evidence. If you genuinely need to retire one, get explicit approval and document it in the plan markdown.

# Communication style

Per `.windsurf/AGENTS.md`:
- No emojis. No "You're absolutely right!" preambles. No agreement validation.
- Direct, terse, fact-based.
- Markdown formatting for output. Code citations in `@absolute/path:line-line` format.
- Plans before code. Brainstorm if scope is unclear.
- "I don't know" beats a confident wrong answer.

# Deliverable shape per stage

Each stage ends with a single status checkpoint to Andrew that includes:

1. **What changed** — file list with `@absolute/path:line-line` citations.
2. **Test evidence** — exact passing count BEFORE and AFTER (e.g., `1178 → 1184`).
3. **Storage layout deltas** for any UUPS contract touched (output of `pnpm meta-ag:layout:check`).
4. **Risk callouts** — anything that surprised you.
5. **Open questions for Andrew** — explicit, numbered, with your default if he doesn't reply.

# Starting move

Your first action this session, after boot:

1. Read the five EE-v2 artifacts listed in the boot section.
2. Run the existing test suite once to confirm the 1178-test baseline:
   ```bash
   cd /root/sidiora.fun-main-branch/smart-contracts
   npx hardhat test 2>&1 | tail -5
   ```
   Expected last line: `1178 passing (Xm)`.
3. Confirm to Andrew you've read the handoff and propose your Stage 2 plan with:
   - Concrete file paths for the four scripts.
   - The two questions you need answered before deployment (Timelock address, EVENT_EMITTER_ROLE holder address).
4. Wait for green-light, then execute Stage 2.

Do not skip steps 1–3. Do not start writing code before Andrew confirms.

# When in doubt

- **Don't know which file holds an event?** `grep -rn "event Foo" contracts/`.
- **Don't know whether a function is in the v2 surface?** `grep -n "Foo" contracts/interfaces/IEventEmitter.sol`.
- **Don't know whether a test exists?** `find test/ -name "*Foo*"`.
- **Don't know whether to mirror or replace a native event?** Mirror. D6 says strip after 30d production proof, not now.
- **Don't know which registry to wire?** `cat deployments/<network>/*.json` or ask Andrew.

# Final reminders

- This is a 7-figure-impact operational upgrade. The deployed proxy serves the entire Paxeer indexer. Don't ship anything you haven't tested on a fork.
- Andrew said "chop chop time is of the essence" at the kickoff. Move fast, but never skip the test gate.
- Surface drift early. If at any point your context model and the on-disk state diverge, stop and re-read.

Good hunting.

---PROMPT END---

# Pasting checklist

Before you copy this into the new session, verify these are all true:

- [ ] You're on the same machine / workspace where the repo lives at `/root/sidiora.fun-main-branch/`.
- [ ] You've decided who holds `EVENT_EMITTER_ROLE` post-upgrade (multisig? executor? you?).
- [ ] You have the Timelock address handy.
- [ ] No other agent is mid-edit on `contracts/data/EventEmitter.sol`, `contracts/interfaces/IEventEmitter.sol`, or anything under `contracts/meta-ag/`.
- [ ] Latest `git status` shows clean working tree (or you've stashed any in-flight unrelated work).

If any of those are unchecked, fix them before starting the new session — the new agent will not catch your context drift.
