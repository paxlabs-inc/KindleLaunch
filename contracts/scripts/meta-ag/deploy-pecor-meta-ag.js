/**
 * Sidiora Meta-AG + PECOR — Layered Deployment Script
 *
 * Deploys the 12 new PECOR + Meta-AG contracts ON TOP of an already-deployed
 * Sidiora launchpad (USDL, Router, Quoter, PoolRegistry, Timelock). Reads the
 * Sidiora addresses from `deployments/{NETWORK}-addresses.json` and merges the
 * new addresses back into the same file.
 *
 * Contract order (mirrors spec §11 build order):
 *   1.  PriceOracle          (UUPS)
 *   2.  OracleHub            (UUPS)
 *   3.  TransactionTracker   (UUPS)
 *   4.  PECORVault           (UUPS)
 *   5.  PECOR                (UUPS)
 *   6.  PECOROrders          (UUPS)
 *   7.  PriceOracleAdapter   (immutable)
 *   8.  SidioraFeedAdapter   (immutable)
 *   9.  VaultAdapter         (immutable)
 *  10.  SidioraAdapter       (immutable)
 *  11.  MetaAGRouter         (UUPS)
 *  12.  MetaAGQuoter         (UUPS)
 *
 * Plus (for local testing only):
 *   - MockWETH9               (WPAX rail for the vault)
 *
 * Wiring performed:
 *   - PriceOracle.RELAYER_ROLE            → RELAYER_ADDRESS (default: deployer)
 *   - PECORVault.OPERATOR_ROLE            → PECOR + PECOROrders + VaultAdapter
 *   - TransactionTracker.EMITTER_ROLE     → PECOR + PECOROrders + MetaAGRouter
 *   - PECOROrders.KEEPER_ROLE             → KEEPER_ADDRESS (default: deployer)
 *   - OracleHub                           → PriceOracleAdapter (priority 10) + SidioraFeedAdapter (priority 20)
 *   - MetaAGRouter                        → VaultAdapter + SidioraAdapter registered
 *   - PECORVault token registry           → USDL (stable), WPAX (non-stable)
 *   - PriceOracle token registry          → USDL + WPAX
 *
 * Optional (controlled by env flags, default ON for localhost):
 *   - SEED_PRICES=true                    → push canonical USD prices for USDL/WPAX
 *   - SEED_VAULT_LIQUIDITY=false          → seed vault reserves from deployer balance
 *
 * Usage:
 *   npx hardhat run scripts/meta-ag/deploy-pecor-meta-ag.js --network localhost
 *   npx hardhat run scripts/meta-ag/deploy-pecor-meta-ag.js --network paxeer-network
 *
 * Environment overrides:
 *   NETWORK_TYPE         = localhost | paxeer-network   (default: localhost)
 *   ADMIN_ADDRESS        = <timelock or EOA>             (default: Timelock from file, else deployer)
 *   RELAYER_ADDRESS      = <EOA or keeper bot>           (default: deployer)
 *   KEEPER_ADDRESS       = <EOA>                         (default: deployer)
 *   FEE_COLLECTOR        = <EOA or treasury>             (default: deployer)
 *   VAULT_ADAPTER_FEE_BPS = 0..200                       (default: 0 — spec Q6)
 *   ORACLE_DEVIATION_BPS  = 0..10000                     (default: 500 — 5%)
 *   WPAX_ADDRESS         = <pre-deployed WETH>           (optional; deploys MockWETH9 if absent)
 *   SEED_PRICES          = true | false                  (default: true on localhost)
 */

const { ethers } = require("hardhat");
const fs = require("fs");
const path = require("path");

const COLORS = {
  reset: "\x1b[0m",
  green: "\x1b[32m",
  yellow: "\x1b[33m",
  cyan: "\x1b[36m",
  gray: "\x1b[90m",
};
const c = (col, s) => `${COLORS[col]}${s}${COLORS.reset}`;
const step = (s) => console.log(`\n${c("yellow", `── ${s} ──`)}`);
const line = (label, val) => console.log(`  ${label.padEnd(22)} ${c("cyan", val)}`);
const ok = (msg) => console.log(`  ${c("green", "✓")} ${msg}`);

async function main() {
  const [deployer] = await ethers.getSigners();
  const net = await ethers.provider.getNetwork();
  const chainId = Number(net.chainId);
  const NETWORK_TYPE =
    process.env.NETWORK_TYPE ||
    (chainId === 125 ? "paxeer-network" : "localhost");

  console.log(`\n${c("yellow", "╔══════════════════════════════════════════════════════════╗")}`);
  console.log(`${c("yellow", "║ Sidiora Meta-AG + PECOR — Layered Deployment             ║")}`);
  console.log(`${c("yellow", "╚══════════════════════════════════════════════════════════╝")}`);
  line("Deployer", deployer.address);
  line("Balance", `${ethers.formatEther(await ethers.provider.getBalance(deployer.address))} PAX`);
  line("Network", `${NETWORK_TYPE} (chainId ${chainId})`);
  line("Addresses file", `deployments/${NETWORK_TYPE}-addresses.json`);

  // ────────────────────────────────────────────────────────────────────── //
  // 0) Load existing Sidiora addresses (USDL, Router, Quoter, PoolRegistry, Timelock)
  // ────────────────────────────────────────────────────────────────────── //
  const addrPath = path.join(
    __dirname,
    "..",
    "..",
    "deployments",
    `paxeer-addresses.json`
  );
  if (!fs.existsSync(addrPath)) {
    throw new Error(
      `Addresses file not found: ${addrPath}\nRun scripts/deploy.js first to deploy the Sidiora launchpad.`
    );
  }
  const addresses = JSON.parse(fs.readFileSync(addrPath, "utf8"));
  const required = [
    "Router_proxy",
    "Quoter_proxy",
    "PoolRegistry_proxy",
    "Timelock",
  ];
  for (const key of required) {
    if (!addresses[key]) {
      throw new Error(
        `Missing required Sidiora address "${key}" in ${addrPath}. Deploy Sidiora first.`
      );
    }
  }
  if (!addresses._meta?.usdl) {
    throw new Error(
      `Missing USDL address in ${addrPath}._meta.usdl. Ensure Sidiora deployment saved it.`
    );
  }

  const USDL = addresses._meta.usdl;
  const TIMELOCK = addresses.Timelock;
  const SIDIORA_ROUTER = addresses.Router_proxy;
  const SIDIORA_QUOTER = addresses.Quoter_proxy;
  const SIDIORA_POOL_REGISTRY = addresses.PoolRegistry_proxy;

  const ADMIN = process.env.ADMIN_ADDRESS || TIMELOCK || deployer.address;
  const RELAYER = process.env.RELAYER_ADDRESS || deployer.address;
  const KEEPER = process.env.KEEPER_ADDRESS || deployer.address;
  const FEE_COLLECTOR = process.env.FEE_COLLECTOR || deployer.address;
  const VAULT_ADAPTER_FEE_BPS = BigInt(process.env.VAULT_ADAPTER_FEE_BPS || "0");
  const ORACLE_DEVIATION_BPS = BigInt(process.env.ORACLE_DEVIATION_BPS || "500");
  const SEED_PRICES =
    process.env.SEED_PRICES === undefined
      ? NETWORK_TYPE !== "paxeer-network"
      : process.env.SEED_PRICES === "true";

  line("Admin", `${ADMIN}${ADMIN === deployer.address ? " (deployer — localhost mode)" : ""}`);
  line("Relayer", RELAYER);
  line("Keeper", KEEPER);
  line("Fee collector", FEE_COLLECTOR);
  line("VaultAdapter fee", `${VAULT_ADAPTER_FEE_BPS} bps`);
  line("Oracle deviation", `${ORACLE_DEVIATION_BPS} bps`);
  line("Seed prices", SEED_PRICES ? "yes" : "no");

  step("Sidiora addresses loaded");
  line("USDL", USDL);
  line("Sidiora Router", SIDIORA_ROUTER);
  line("Sidiora Quoter", SIDIORA_QUOTER);
  line("Sidiora Registry", SIDIORA_POOL_REGISTRY);
  line("Timelock", TIMELOCK);

  // ────────────────────────────────────────────────────────────────────── //
  // Helpers
  // ────────────────────────────────────────────────────────────────────── //
  const Proxy = await ethers.getContractFactory("ERC1967Proxy");
  const deployed = {};

  async function deployUUPS(name, initArgs, keyPrefix = name) {
    console.log(`  Deploying ${c("cyan", name)}...`);
    const Impl = await ethers.getContractFactory(name);
    const impl = await Impl.deploy();
    await impl.waitForDeployment();
    const implAddr = await impl.getAddress();

    const initData = Impl.interface.encodeFunctionData("initialize", initArgs);
    const proxy = await Proxy.deploy(implAddr, initData);
    await proxy.waitForDeployment();
    const proxyAddr = await proxy.getAddress();

    line(`  ${keyPrefix} impl`, implAddr);
    line(`  ${keyPrefix} proxy`, proxyAddr);

    deployed[`${keyPrefix}_impl`] = implAddr;
    deployed[`${keyPrefix}_proxy`] = proxyAddr;
    return Impl.attach(proxyAddr);
  }

  async function deployImmutable(name, args, key = name) {
    console.log(`  Deploying ${c("cyan", name)}...`);
    const Factory = await ethers.getContractFactory(name);
    const c_ = await Factory.deploy(...args);
    await c_.waitForDeployment();
    const addr = await c_.getAddress();
    line(`  ${key}`, addr);
    deployed[key] = addr;
    return c_;
  }

  async function waitTx(promise, label) {
    const tx = await promise;
    await tx.wait();
    if (label) ok(label);
  }

  // ────────────────────────────────────────────────────────────────────── //
  // 1) WPAX (WETH rail) — deploy MockWETH9 on local; use pre-deployed on Paxeer
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 1 — WPAX (wrapped native)");
  let WPAX = process.env.WPAX_ADDRESS || addresses.WPAX;
  if (!WPAX) {
    if (NETWORK_TYPE === "paxeer-network") {
      throw new Error(
        "WPAX_ADDRESS not set for paxeer-network. Set env WPAX_ADDRESS to the live WPAX contract."
      );
    }
    console.log(`  ${c("gray", "No WPAX provided — deploying MockWETH9 for local testing")}`);
    const weth = await deployImmutable("MockWETH9", [], "WPAX");
    WPAX = await weth.getAddress();
  } else {
    line("WPAX (preexisting)", WPAX);
    deployed.WPAX = WPAX;
  }

  // ────────────────────────────────────────────────────────────────────── //
  // 2) PriceOracle
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 2 — PriceOracle");
  const priceOracle = await deployUUPS("PriceOracle", [deployer.address], "PriceOracle");

  // ────────────────────────────────────────────────────────────────────── //
  // 3) OracleHub
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 3 — OracleHub");
  const oracleHub = await deployUUPS(
    "OracleHub",
    [
      await priceOracle.getAddress(),
      ORACLE_DEVIATION_BPS,
      3000n,
      deployer.address,
    ],
    "OracleHub"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // 4) TransactionTracker
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 4 — TransactionTracker");
  const tracker = await deployUUPS("TransactionTracker", [deployer.address], "TransactionTracker");

  // ────────────────────────────────────────────────────────────────────── //
  // 5) PECORVault
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 5 — PECORVault");
  const vault = await deployUUPS(
    "PECORVault",
    [WPAX, await tracker.getAddress(), deployer.address],
    "PECORVault"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // 6) PECOR
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 6 — PECOR");
  const pecor = await deployUUPS(
    "PECOR",
    [
      await priceOracle.getAddress(),
      await vault.getAddress(),
      WPAX,
      await tracker.getAddress(),
      deployer.address,
    ],
    "PECOR"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // 7) PECOROrders
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 7 — PECOROrders");
  const pecorOrders = await deployUUPS(
    "PECOROrders",
    [
      await priceOracle.getAddress(),
      await vault.getAddress(),
      await tracker.getAddress(),
      deployer.address,
    ],
    "PECOROrders"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // 8) PriceOracleAdapter (immutable)
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 8 — PriceOracleAdapter");
  const priceOracleAdapter = await deployImmutable(
    "PriceOracleAdapter",
    [await priceOracle.getAddress()],
    "PriceOracleAdapter"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // 9) SidioraFeedAdapter (immutable)
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 9 — SidioraFeedAdapter");
  const sidioraFeedAdapter = await deployImmutable(
    "SidioraFeedAdapter",
    [SIDIORA_POOL_REGISTRY, 0n, deployer.address],
    "SidioraFeedAdapter"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // 10) VaultAdapter (immutable)
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 10 — VaultAdapter");
  const vaultAdapter = await deployImmutable(
    "VaultAdapter",
    [
      await vault.getAddress(),
      await priceOracle.getAddress(),
      VAULT_ADAPTER_FEE_BPS,
      FEE_COLLECTOR,
      deployer.address,
    ],
    "VaultAdapter"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // 11) SidioraAdapter (immutable)
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 11 — SidioraAdapter");
  const sidioraAdapter = await deployImmutable(
    "SidioraAdapter",
    [SIDIORA_POOL_REGISTRY, SIDIORA_QUOTER, SIDIORA_ROUTER, USDL, deployer.address],
    "SidioraAdapter"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // 12) MetaAGRouter
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 12 — MetaAGRouter");
  const router = await deployUUPS(
    "MetaAGRouter",
    [await oracleHub.getAddress(), ORACLE_DEVIATION_BPS, deployer.address],
    "MetaAGRouter"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // 13) MetaAGQuoter
  // ────────────────────────────────────────────────────────────────────── //
  step("Step 13 — MetaAGQuoter");
  const quoter = await deployUUPS(
    "MetaAGQuoter",
    [
      await priceOracle.getAddress(),
      await vault.getAddress(),
      WPAX,
      await pecor.getAddress(),
      deployer.address,
    ],
    "MetaAGQuoter"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // Role wiring                                                            //
  // ────────────────────────────────────────────────────────────────────── //
  step("Role wiring");

  // PriceOracle relayer
  await waitTx(
    priceOracle.setRelayer(RELAYER, true),
    `PriceOracle.setRelayer(${RELAYER})`
  );

  // Vault operators
  await waitTx(vault.setOperator(await pecor.getAddress(), true), "PECORVault.setOperator(PECOR)");
  await waitTx(
    vault.setOperator(await pecorOrders.getAddress(), true),
    "PECORVault.setOperator(PECOROrders)"
  );
  await waitTx(
    vault.setOperator(await vaultAdapter.getAddress(), true),
    "PECORVault.setOperator(VaultAdapter)"
  );

  // Orders keeper
  await waitTx(pecorOrders.setKeeper(KEEPER, true), `PECOROrders.setKeeper(${KEEPER})`);

  // Tracker emitters
  await waitTx(
    tracker.setAuthorizedEmitter(await pecor.getAddress(), true),
    "TransactionTracker.setAuthorizedEmitter(PECOR)"
  );
  await waitTx(
    tracker.setAuthorizedEmitter(await pecorOrders.getAddress(), true),
    "TransactionTracker.setAuthorizedEmitter(PECOROrders)"
  );
  await waitTx(
    tracker.setAuthorizedEmitter(await router.getAddress(), true),
    "TransactionTracker.setAuthorizedEmitter(MetaAGRouter)"
  );

  // OracleHub adapters
  await waitTx(
    oracleHub.registerAdapter(await priceOracleAdapter.getAddress(), 10),
    "OracleHub.registerAdapter(PriceOracleAdapter, priority=10)"
  );
  await waitTx(
    oracleHub.registerAdapter(await sidioraFeedAdapter.getAddress(), 20),
    "OracleHub.registerAdapter(SidioraFeedAdapter, priority=20)"
  );

  // MetaAGRouter adapters
  await waitTx(
    router.registerAdapter(await vaultAdapter.getAddress()),
    "MetaAGRouter.registerAdapter(VaultAdapter)"
  );
  await waitTx(
    router.registerAdapter(await sidioraAdapter.getAddress()),
    "MetaAGRouter.registerAdapter(SidioraAdapter)"
  );

  // ────────────────────────────────────────────────────────────────────── //
  // Token registration (USDL + WPAX)                                      //
  // ────────────────────────────────────────────────────────────────────── //
  step("Token registry");
  const ONE = 10n ** 18n;
  const tokenConfig = [60, 100n, ONE / 100n, ONE * 1_000_000n, 3600];
  const bootstrapTokens = [
    { name: "USDL", addr: USDL, stable: true, price: ONE },
    { name: "WPAX", addr: WPAX, stable: false, price: ONE },
  ];

  for (const t of bootstrapTokens) {
    await waitTx(vault.registerToken(t.addr, t.stable), `PECORVault.registerToken(${t.name}, stable=${t.stable})`);
    await waitTx(
      priceOracle.registerToken(t.addr, ...tokenConfig),
      `PriceOracle.registerToken(${t.name})`
    );
  }

  if (SEED_PRICES) {
    const relayerSigner =
      RELAYER.toLowerCase() === deployer.address.toLowerCase()
        ? deployer
        : await ethers.getSigner(RELAYER);
    await waitTx(
      priceOracle
        .connect(relayerSigner)
        .batchUpdatePrices(
          bootstrapTokens.map((t) => t.addr),
          bootstrapTokens.map((t) => t.price)
        ),
      `PriceOracle.batchUpdatePrices([USDL, WPAX])`
    );
  }

  // ────────────────────────────────────────────────────────────────────── //
  // Transfer admin to Timelock if requested                                //
  // ────────────────────────────────────────────────────────────────────── //
  if (
    ADMIN.toLowerCase() !== deployer.address.toLowerCase() &&
    process.env.TRANSFER_ADMIN_TO_TIMELOCK === "true"
  ) {
    step("Transferring DEFAULT_ADMIN_ROLE → Timelock (one-way)");
    const DEFAULT_ADMIN_ROLE = "0x" + "00".repeat(32);
    const adminables = [
      { name: "PriceOracle", c: priceOracle },
      { name: "OracleHub", c: oracleHub },
      { name: "TransactionTracker", c: tracker },
      { name: "PECORVault", c: vault },
      { name: "PECOR", c: pecor },
      { name: "PECOROrders", c: pecorOrders },
      { name: "MetaAGRouter", c: router },
      { name: "MetaAGQuoter", c: quoter },
      { name: "SidioraFeedAdapter", c: sidioraFeedAdapter },
      { name: "VaultAdapter", c: vaultAdapter },
      { name: "SidioraAdapter", c: sidioraAdapter },
    ];
    for (const { name, c: ct } of adminables) {
      await waitTx(ct.grantRole(DEFAULT_ADMIN_ROLE, ADMIN), `  ${name}.grantRole(DEFAULT_ADMIN_ROLE, Timelock)`);
      await waitTx(
        ct.renounceRole(DEFAULT_ADMIN_ROLE, deployer.address),
        `  ${name}.renounceRole(DEFAULT_ADMIN_ROLE, deployer)`
      );
    }
  } else if (ADMIN.toLowerCase() !== deployer.address.toLowerCase()) {
    console.log(
      `\n  ${c(
        "gray",
        "Skipping admin transfer. Set TRANSFER_ADMIN_TO_TIMELOCK=true to transfer DEFAULT_ADMIN_ROLE to Timelock."
      )}`
    );
  }

  // ────────────────────────────────────────────────────────────────────── //
  // Save merged addresses                                                  //
  // ────────────────────────────────────────────────────────────────────── //
  step("Saving merged addresses");
  Object.assign(addresses, deployed);
  addresses._meta_pecor = {
    deployer: deployer.address,
    admin: ADMIN,
    relayer: RELAYER,
    keeper: KEEPER,
    feeCollector: FEE_COLLECTOR,
    vaultAdapterFeeBps: VAULT_ADAPTER_FEE_BPS.toString(),
    oracleDeviationBps: ORACLE_DEVIATION_BPS.toString(),
    wpax: WPAX,
    network: chainId.toString(),
    timestamp: new Date().toISOString(),
  };
  fs.writeFileSync(addrPath, JSON.stringify(addresses, null, 2));
  line("Saved", addrPath);

  step("Summary");
  console.log(`  ${c("green", "✓ Deployment complete")}`);
  console.log(`  Total PECOR + Meta-AG artifacts: ${Object.keys(deployed).length}`);
  console.log(`\n  Adapters registered on MetaAGRouter:`);
  line("    VaultAdapter", deployed.VaultAdapter);
  line("    SidioraAdapter", deployed.SidioraAdapter);
  console.log(`\n  Adapters registered on OracleHub:`);
  line("    PriceOracleAdapter", deployed.PriceOracleAdapter);
  line("    SidioraFeedAdapter", deployed.SidioraFeedAdapter);
  console.log(`\n  Router proxy (user-facing):`);
  line("    MetaAGRouter", deployed.MetaAGRouter_proxy);
  console.log();
}

main()
  .then(() => process.exit(0))
  .catch((e) => {
    console.error(c("reset", "\n❌ Deployment failed:"), e);
    process.exit(1);
  });
