/**
 * Sidiora Launchpad AMM — Contract Verification Script (Blockscout)
 *
 * Verifies all deployed contracts on Paxscan (Blockscout-based explorer)
 * using the Blockscout v2 standard-input JSON verification API.
 *
 * Usage:
 *   npx hardhat run scripts/verify.js --network paxeer-network
 *
 * Requires: deployments/paxeer-addresses.json (output from deploy.js)
 */

const fs = require("fs");
const path = require("path");

const PAXSCAN_API = "https://paxscan-backend.up.railway.app/api/v2";
const COMPILER_VERSION = "v0.8.27+commit.40a35a09";
const BUILD_INFO_DIR = path.join(__dirname, "..", "artifacts", "build-info");

// Map contract names to their Solidity source paths
const CONTRACT_SOURCES = {
  ProtocolConfig:    "contracts/protocol/ProtocolConfig.sol",
  Treasury:          "contracts/protocol/Treasury.sol",
  GovernanceModule:  "contracts/protocol/GovernanceModule.sol",
  EventEmitter:      "contracts/data/EventEmitter.sol",
  PoolRegistry:      "contracts/data/PoolRegistry.sol",
  FeeAccumulator:    "contracts/data/FeeAccumulator.sol",
  SidioraNFT:        "contracts/core/SidioraNFT.sol",
  SidioraFactory:    "contracts/core/SidioraFactory.sol",
  SidioraPool:       "contracts/core/SidioraPool.sol",
  OpticalRegistry:   "contracts/opticals/OpticalRegistry.sol",
  Router:            "contracts/periphery/Router.sol",
  Quoter:            "contracts/periphery/Quoter.sol",
  FeesRouter:        "contracts/periphery/FeesRouter.sol",
  AntiSnipeOptical:  "contracts/opticals/presets/AntiSnipeOptical.sol",
  MaxWalletOptical:  "contracts/opticals/presets/MaxWalletOptical.sol",
  TaxOptical:        "contracts/opticals/presets/TaxOptical.sol",
  CooldownOptical:   "contracts/opticals/presets/CooldownOptical.sol",
  BuybackBurnOptical:"contracts/opticals/presets/BuybackBurnOptical.sol",
  PoolBeacon:        "contracts/core/PoolBeacon.sol",
  Timelock:          "contracts/protocol/Timelock.sol",
  UUPSProxy:         "contracts/test/MockUUPS.sol",
};

function findBuildInfo(contractName) {
  const sourcePath = CONTRACT_SOURCES[contractName];
  if (!sourcePath) return null;

  const files = fs.readdirSync(BUILD_INFO_DIR);
  for (const f of files) {
    const bi = JSON.parse(fs.readFileSync(path.join(BUILD_INFO_DIR, f), "utf8"));
    const sources = Object.keys(bi.input.sources);
    if (sources.includes(sourcePath)) {
      const output = bi.output?.contracts?.[sourcePath]?.[contractName];
      if (output) return bi;
    }
  }
  return null;
}

async function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function verifyViaStandardInput(address, contractName, sourcePath) {
  const bi = findBuildInfo(contractName);
  if (!bi) {
    return { ok: false, error: "Build info not found" };
  }

  const standardInput = JSON.stringify(bi.input);
  const contractPath = `${sourcePath}:${contractName}`;

  // Blockscout v2: POST multipart/form-data to /api/v2/smart-contracts/{address}/verification/via/standard-input
  const url = `${PAXSCAN_API}/smart-contracts/${address}/verification/via/standard-input`;

  // Build multipart form data manually (no external deps)
  const boundary = "----SidioraVerify" + Date.now();
  const parts = [];

  function addField(name, value) {
    parts.push(
      `--${boundary}\r\nContent-Disposition: form-data; name="${name}"\r\n\r\n${value}\r\n`
    );
  }

  function addFile(name, filename, content, contentType) {
    parts.push(
      `--${boundary}\r\nContent-Disposition: form-data; name="${name}"; filename="${filename}"\r\nContent-Type: ${contentType}\r\n\r\n${content}\r\n`
    );
  }

  addField("compiler_version", COMPILER_VERSION);
  addField("contract_name", contractPath);
  addField("autodetect_constructor_args", "true");
  addFile("files[0]", "input.json", standardInput, "application/json");

  const body = parts.join("") + `--${boundary}--\r\n`;

  try {
    const resp = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": `multipart/form-data; boundary=${boundary}` },
      body,
    });

    const text = await resp.text();
    let data;
    try { data = JSON.parse(text); } catch { data = { raw: text }; }

    if (resp.ok && !data.message?.includes("error")) {
      return { ok: true, data };
    } else {
      return { ok: false, error: data.message || data.raw || `HTTP ${resp.status}` };
    }
  } catch (err) {
    return { ok: false, error: err.message };
  }
}

async function main() {
  const addrPath = path.join(__dirname, "..", "deployments", "paxeer-addresses.json");
  if (!fs.existsSync(addrPath)) {
    throw new Error(`Addresses file not found: ${addrPath}\nRun deploy.js first.`);
  }

  const addresses = JSON.parse(fs.readFileSync(addrPath, "utf8"));
  const meta = addresses._meta;

  console.log(`\n🔍 Verifying Sidiora contracts on Paxscan (Blockscout)`);
  console.log(`   Network:  Chain ID ${meta.network}`);
  console.log(`   Compiler: ${COMPILER_VERSION}`);
  console.log(`   Deployed: ${meta.timestamp}\n`);

  const results = { passed: [], failed: [] };

  // Build verification queue: [{ label, address, contractName }]
  const queue = [];

  // UUPS implementations
  const uupsContracts = [
    "EventEmitter", "ProtocolConfig", "Treasury", "GovernanceModule",
    "PoolRegistry", "FeeAccumulator", "SidioraNFT", "SidioraFactory",
    "OpticalRegistry", "Router", "Quoter", "FeesRouter",
  ];
  for (const name of uupsContracts) {
    if (addresses[`${name}_impl`]) {
      queue.push({ label: `${name} (impl)`, address: addresses[`${name}_impl`], contractName: name });
    }
  }

  // SidioraPool impl
  if (addresses.SidioraPool_impl) {
    queue.push({ label: "SidioraPool (impl)", address: addresses.SidioraPool_impl, contractName: "SidioraPool" });
  }

  // Immutable contracts
  if (addresses.PoolBeacon) {
    queue.push({ label: "PoolBeacon", address: addresses.PoolBeacon, contractName: "PoolBeacon" });
  }
  if (addresses.Timelock) {
    queue.push({ label: "Timelock", address: addresses.Timelock, contractName: "Timelock" });
  }

  // Optical presets
  const opticals = ["AntiSnipeOptical", "MaxWalletOptical", "TaxOptical", "CooldownOptical", "BuybackBurnOptical"];
  for (const name of opticals) {
    if (addresses[name]) {
      queue.push({ label: name, address: addresses[name], contractName: name });
    }
  }

  // UUPS proxies (all use UUPSProxy contract)
  for (const name of uupsContracts) {
    if (addresses[`${name}_proxy`]) {
      queue.push({ label: `${name} (proxy)`, address: addresses[`${name}_proxy`], contractName: "UUPSProxy" });
    }
  }

  // Execute verification
  for (let i = 0; i < queue.length; i++) {
    const item = queue[i];
    const sourcePath = CONTRACT_SOURCES[item.contractName];
    if (!sourcePath) {
      console.log(`  ⚠️  ${item.label} — no source path mapped, skipping`);
      results.failed.push({ name: item.label, error: "No source path" });
      continue;
    }

    console.log(`  [${i + 1}/${queue.length}] ${item.label} at ${item.address.slice(0, 12)}...`);

    const result = await verifyViaStandardInput(item.address, item.contractName, sourcePath);

    if (result.ok) {
      console.log(`    ✅ Verified`);
      results.passed.push(item.label);
    } else {
      const errMsg = typeof result.error === "string" ? result.error : JSON.stringify(result.error);
      if (errMsg.includes("already verified") || errMsg.includes("Already Verified")) {
        console.log(`    ✅ Already verified`);
        results.passed.push(item.label);
      } else {
        console.log(`    ❌ ${errMsg.slice(0, 150)}`);
        results.failed.push({ name: item.label, error: errMsg.slice(0, 200) });
      }
    }

    // Rate limit: 1 req/sec to avoid Blockscout throttling
    if (i < queue.length - 1) await sleep(1500);
  }

  // ─── Summary ───
  console.log(`\n${"═".repeat(60)}`);
  console.log(`  Verification Summary`);
  console.log(`  ✅ Passed: ${results.passed.length}`);
  console.log(`  ❌ Failed: ${results.failed.length}`);
  if (results.failed.length > 0) {
    console.log(`\n  Failed contracts:`);
    for (const f of results.failed) {
      console.log(`    - ${f.name}: ${f.error}`);
    }
  }
  console.log(`${"═".repeat(60)}\n`);
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error("❌ Verification failed:", error);
    process.exit(1);
  });
