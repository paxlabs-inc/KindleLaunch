#!/usr/bin/env node
/**
 * scripts/meta-ag/snapshot-storage-layout.js
 *
 * Dumps the Solidity `storageLayout` output for every deployed meta-ag UUPS
 * contract to `storage-layout/meta-ag/<Name>.json`. The full Phase 9 script
 * (Task 9.1) will extend this with diff logic; this Phase 2 drop already
 * produces the canonical baseline for PriceOracle + OracleHub.
 *
 * Usage:
 *   node scripts/meta-ag/snapshot-storage-layout.js
 *
 * Exits 0 if all snapshots were refreshed (or there are no UUPS contracts yet).
 */

"use strict";

const fs = require("fs");
const path = require("path");

// Contracts that are currently upgradeable and therefore need a layout baseline.
// Extend this list as each UUPS contract lands in later phases.
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

  const outDir = path.resolve(__dirname, "..", "..", "storage-layout", "meta-ag");
  fs.mkdirSync(outDir, { recursive: true });

  let snapshots = 0;
  for (const fq of TRACKED) {
    const [sourceName, contractName] = fq.split(":");
    const buildInfo = await hre.artifacts.getBuildInfo(fq);
    if (!buildInfo) {
      console.warn(`[meta-ag:layout] skip — no build info for ${fq}`);
      continue;
    }
    const contract = buildInfo.output.contracts[sourceName][contractName];
    if (!contract || !contract.storageLayout) {
      console.warn(`[meta-ag:layout] skip — no storageLayout for ${fq}`);
      continue;
    }

    const out = {
      contract: fq,
      compiler: buildInfo.solcLongVersion,
      input: {
        optimizer: buildInfo.input.settings.optimizer,
        viaIR: buildInfo.input.settings.viaIR === true,
      },
      storage: contract.storageLayout.storage,
      types: contract.storageLayout.types,
    };

    const file = path.join(outDir, `${contractName}.json`);
    fs.writeFileSync(file, JSON.stringify(out, null, 2) + "\n");
    console.log(`[meta-ag:layout] wrote ${path.relative(path.resolve(__dirname, "..", ".."), file)}`);
    snapshots++;
  }

  if (snapshots === 0) {
    console.log("[meta-ag:layout] No UUPS contracts tracked yet.");
  }
}

main().catch((err) => {
  console.error("[meta-ag:layout] failed:", err);
  process.exit(1);
});
