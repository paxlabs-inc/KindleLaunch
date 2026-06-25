#!/usr/bin/env node
/**
 * scripts/meta-ag/check-storage-layout.js
 *
 * Append-only enforcement for meta-ag UUPS storage layouts (S12).
 * Compares each tracked contract's current storage layout against the
 * frozen baseline under `storage-layout/meta-ag/<Name>.json` and exits
 * non-zero if any slot was removed, resized, renamed, or re-typed.
 *
 * Adding new slots AFTER the existing tail (i.e. inside `__gap[50]`) is
 * permitted. Phase 9 (Task 9.2) will harden this with richer diffs; this
 * Phase 2 drop covers the invariants required to protect the baselines
 * committed today.
 *
 * Usage:
 *   node scripts/meta-ag/check-storage-layout.js
 */

"use strict";

const fs = require("fs");
const path = require("path");

const TRACKED = [
  "contracts/meta-ag/oracle/PriceOracle.sol:PriceOracle",
  "contracts/meta-ag/oracle/OracleHub.sol:OracleHub",
  "contracts/meta-ag/vault/PECORVault.sol:PECORVault",
  "contracts/meta-ag/engine/PECOR.sol:PECOR",
  "contracts/meta-ag/engine/PECOROrders.sol:PECOROrders",
  "contracts/meta-ag/analytics/TransactionTracker.sol:TransactionTracker",
  "contracts/meta-ag/router/MetaAGRouter.sol:MetaAGRouter",
  "contracts/meta-ag/quoter/MetaAGQuoter.sol:MetaAGQuoter",
];

async function main() {
  const hre = require("hardhat");
  await hre.run("compile", { quiet: true });

  const baseDir = path.resolve(__dirname, "..", "..", "storage-layout", "meta-ag");
  const errors = [];

  for (const fq of TRACKED) {
    const [sourceName, contractName] = fq.split(":");
    const baselinePath = path.join(baseDir, `${contractName}.json`);
    if (!fs.existsSync(baselinePath)) {
      console.warn(`[meta-ag:layout:check] no baseline for ${fq} — run snapshot first.`);
      continue;
    }

    const baseline = JSON.parse(fs.readFileSync(baselinePath, "utf8"));
    const buildInfo = await hre.artifacts.getBuildInfo(fq);
    const current =
      buildInfo.output.contracts[sourceName][contractName].storageLayout;

    const baseSlots = baseline.storage;
    const curSlots = current.storage;

    // AST IDs embedded in solc type strings (e.g. `t_struct(PriceData)936_storage`)
    // are compiler-internal and re-number whenever contracts are added/removed from
    // the project. They are NOT part of the upgradeable storage contract. Strip them
    // before comparing so layout-equivalent changes don't trigger a false positive.
    const stripAstIds = (t) => t.replace(/\)\d+_storage/g, ")_storage");

    // Baseline entries must exist in current at the same index with same
    // label/type/offset/slot. New appended entries are allowed.
    for (let i = 0; i < baseSlots.length; i++) {
      const b = baseSlots[i];
      const c = curSlots[i];
      if (!c) {
        errors.push(`${fq}: slot ${i} ('${b.label}') removed`);
        continue;
      }
      if (c.label !== b.label) {
        errors.push(`${fq}: slot ${i} renamed '${b.label}' -> '${c.label}'`);
      }
      if (c.slot !== b.slot || c.offset !== b.offset) {
        errors.push(
          `${fq}: slot ${i} ('${b.label}') moved from slot=${b.slot}/offset=${b.offset} to slot=${c.slot}/offset=${c.offset}`
        );
      }
      if (stripAstIds(c.type) !== stripAstIds(b.type)) {
        errors.push(`${fq}: slot ${i} ('${b.label}') type changed '${b.type}' -> '${c.type}'`);
      }
    }
  }

  if (errors.length > 0) {
    console.error("[meta-ag:layout:check] Layout drift detected:");
    for (const e of errors) console.error("  - " + e);
    process.exit(1);
  }
  console.log("[meta-ag:layout:check] OK — layouts match baselines.");
}

main().catch((err) => {
  console.error("[meta-ag:layout:check] failed:", err);
  process.exit(1);
});
