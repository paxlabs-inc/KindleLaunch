/**
 * Sidiora Launchpad AMM — Full Deployment Script
 *
 * Deploy order (20 steps):
 *  1. EventEmitter (UUPS)          9.  PoolBeacon
 *  2. ProtocolConfig (UUPS)       10.  SidioraNFT (UUPS)
 *  3. Treasury (UUPS)             11.  SidioraFactory (UUPS)
 *  4. Timelock (immutable)        12.  OpticalRegistry (UUPS)
 *  5. GovernanceModule (UUPS)     13.  Router (UUPS)
 *  6. PoolRegistry (UUPS)         14.  Quoter (UUPS)
 *  7. FeeAccumulator (UUPS)       15.  FeesRouter (UUPS)
 *  8. SidioraPool impl            16-20. Wire roles + optical presets
 *
 * Usage:
 *   npx hardhat run scripts/deploy.js --network paxeer-network
 *
 * Requires .env:
 *   PRIVATE_KEY, USDL_ADDRESS, SID_ADDRESS, GUARDIAN_ADDRESS, TIMELOCK_DELAY
 */

const { ethers } = require("hardhat");
const fs = require("fs");
const path = require("path");

async function main() {
  const [deployer] = await ethers.getSigners();
  const balance = await ethers.provider.getBalance(deployer.address);
  console.log(`\n🚀 Deploying Sidiora Launchpad AMM`);
  console.log(`   Deployer:  ${deployer.address}`);
  console.log(`   Balance:   ${ethers.formatEther(balance)} PAX`);
  console.log(`   Network:   ${(await ethers.provider.getNetwork()).chainId}\n`);

  const USDL = process.env.USDL_ADDRESS;
  const SID = process.env.SID_ADDRESS;
  const GUARDIAN = process.env.GUARDIAN_ADDRESS || deployer.address;
  const ADMIN = process.env.ADMIN_ADDRESS || deployer.address;
  const TIMELOCK_DELAY = parseInt(process.env.TIMELOCK_DELAY || "172800");
  const NETWORK_TYPE= process.env.NETWORK_TYPE || "paxeer-network" || "localhost";

  if (!USDL) throw new Error("USDL_ADDRESS not set in .env");
  if (!SID) throw new Error("SID_ADDRESS not set in .env");

  const addresses = {};
  const Proxy = await ethers.getContractFactory("UUPSProxy");

  async function deployUUPS(name, args) {
    console.log(`  Deploying ${name}...`);
    const Factory = await ethers.getContractFactory(name);
    const impl = await Factory.deploy();
    await impl.waitForDeployment();
    const implAddr = await impl.getAddress();

    const initData = Factory.interface.encodeFunctionData("initialize", args);
    const proxy = await Proxy.deploy(implAddr, initData);
    await proxy.waitForDeployment();
    const proxyAddr = await proxy.getAddress();

    console.log(`    impl:  ${implAddr}`);
    console.log(`    proxy: ${proxyAddr}`);

    addresses[`${name}_impl`] = implAddr;
    addresses[`${name}_proxy`] = proxyAddr;
    return Factory.attach(proxyAddr);
  }

  // ═══════════════════════════════════════════════════════════════════
  // Step 1: EventEmitter (deploy first — many contracts reference it)
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 1: EventEmitter ──");
  const eventEmitterProxy = await deployUUPS("EventEmitter", [
    ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 2: ProtocolConfig
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 2: ProtocolConfig ──");
  const configProxy = await deployUUPS("ProtocolConfig", [
    USDL, addresses.EventEmitter_proxy, ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 3: Treasury
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 3: Treasury ──");
  const treasuryProxy = await deployUUPS("Treasury", [
    addresses.EventEmitter_proxy, ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 4: Timelock (immutable)
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 4: Timelock ──");
  const Timelock = await ethers.getContractFactory("Timelock");
  const timelock = await Timelock.deploy(TIMELOCK_DELAY, ADMIN, GUARDIAN);
  await timelock.waitForDeployment();
  addresses.Timelock = await timelock.getAddress();
  console.log(`    addr:  ${addresses.Timelock}`);

  // ═══════════════════════════════════════════════════════════════════
  // Step 5: GovernanceModule (6 args: votingToken, timelock, admin,
  //         proposalThreshold, votingPeriod, quorumVotes)
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 5: GovernanceModule ──");
  const PROPOSAL_THRESHOLD = ethers.parseUnits("100000", 6);  // 100k SID to propose
  const VOTING_PERIOD = 17280;                              // ~3 days in blocks
  const QUORUM_VOTES = ethers.parseUnits("1000000", 6);        // 1M SID quorum
  const govProxy = await deployUUPS("GovernanceModule", [
    SID, addresses.Timelock, ADMIN, PROPOSAL_THRESHOLD, VOTING_PERIOD, QUORUM_VOTES,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 6: PoolRegistry
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 6: PoolRegistry ──");
  const registryProxy = await deployUUPS("PoolRegistry", [
    addresses.EventEmitter_proxy, ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 7: FeeAccumulator
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 7: FeeAccumulator ──");
  const accProxy = await deployUUPS("FeeAccumulator", [
    addresses.ProtocolConfig_proxy,
    addresses.Treasury_proxy,
    addresses.PoolRegistry_proxy,
    addresses.EventEmitter_proxy,
    USDL,
    ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 8: SidioraPool implementation (not proxied — beacon target)
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 8: SidioraPool impl ──");
  const PoolFactory = await ethers.getContractFactory("SidioraPool");
  const poolImpl = await PoolFactory.deploy();
  await poolImpl.waitForDeployment();
  addresses.SidioraPool_impl = await poolImpl.getAddress();
  console.log(`    impl:  ${addresses.SidioraPool_impl}`);

  // ═══════════════════════════════════════════════════════════════════
  // Step 9: PoolBeacon
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 9: PoolBeacon ──");
  const PoolBeacon = await ethers.getContractFactory("PoolBeacon");
  const beacon = await PoolBeacon.deploy(addresses.SidioraPool_impl, ADMIN);
  await beacon.waitForDeployment();
  addresses.PoolBeacon = await beacon.getAddress();
  console.log(`    addr:  ${addresses.PoolBeacon}`);

  // ═══════════════════════════════════════════════════════════════════
  // Step 10: SidioraNFT
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 10: SidioraNFT ──");
  const nftProxy = await deployUUPS("SidioraNFT", [
    "Sidiora Pool NFT", "SIDNFT", addresses.EventEmitter_proxy, ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 11: SidioraFactory
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 11: SidioraFactory ──");
  const factoryProxy = await deployUUPS("SidioraFactory", [
    addresses.PoolBeacon,
    addresses.SidioraNFT_proxy,
    addresses.PoolRegistry_proxy,
    addresses.EventEmitter_proxy,
    addresses.ProtocolConfig_proxy,
    addresses.Treasury_proxy,
    addresses.FeeAccumulator_proxy,
    USDL,
    ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 12: OpticalRegistry
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 12: OpticalRegistry ──");
  const optRegistryProxy = await deployUUPS("OpticalRegistry", [
    addresses.EventEmitter_proxy, ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 13: Router
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 13: Router ──");
  const routerProxy = await deployUUPS("Router", [
    addresses.SidioraFactory_proxy,
    addresses.PoolRegistry_proxy,
    addresses.ProtocolConfig_proxy,
    USDL,
    ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 14: Quoter
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 14: Quoter ──");
  const quoterProxy = await deployUUPS("Quoter", [
    addresses.PoolRegistry_proxy,
    addresses.ProtocolConfig_proxy,
    ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 15: FeesRouter
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 15: FeesRouter ──");
  const feesRouterProxy = await deployUUPS("FeesRouter", [
    addresses.SidioraNFT_proxy,
    addresses.FeeAccumulator_proxy,
    addresses.PoolRegistry_proxy,
    ADMIN,
  ]);

  // ═══════════════════════════════════════════════════════════════════
  // Step 16: Deploy optical presets (immutable)
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 16: Optical Presets ──");
  const opticals = {};
  const presets = [
    { name: "AntiSnipeOptical", args: [addresses.PoolRegistry_proxy, ADMIN, 100, 10] },
    { name: "MaxWalletOptical", args: [addresses.PoolRegistry_proxy, ADMIN, 200] },
    { name: "TaxOptical", args: [addresses.PoolRegistry_proxy, ADMIN, 200, 200] },
    { name: "CooldownOptical", args: [addresses.PoolRegistry_proxy, ADMIN, 60] },
    { name: "BuybackBurnOptical", args: [addresses.PoolRegistry_proxy, ADMIN, 1000] },
  ];

  for (const preset of presets) {
    const F = await ethers.getContractFactory(preset.name);
    const c = await F.deploy(...preset.args);
    await c.waitForDeployment();
    const addr = await c.getAddress();
    opticals[preset.name] = addr;
    addresses[preset.name] = addr;
    console.log(`    ${preset.name}: ${addr}`);
  }

  // ═══════════════════════════════════════════════════════════════════
  // Step 17-19: Wire roles
  // ═══════════════════════════════════════════════════════════════════
  console.log("── Step 17-19: Wiring Roles ──");

  const FACTORY_ROLE = ethers.keccak256(ethers.toUtf8Bytes("FACTORY_ROLE"));
  const MINTER_ROLE = ethers.keccak256(ethers.toUtf8Bytes("MINTER_ROLE"));
  const DEPOSITOR_ROLE = ethers.keccak256(ethers.toUtf8Bytes("DEPOSITOR_ROLE"));
  const ROUTER_ROLE = ethers.keccak256(ethers.toUtf8Bytes("ROUTER_ROLE"));
  const FEES_ROUTER_ROLE = ethers.keccak256(ethers.toUtf8Bytes("FEES_ROUTER_ROLE"));
  const STRATEGY_SETTER_ROLE = ethers.keccak256(ethers.toUtf8Bytes("STRATEGY_SETTER_ROLE"));
  const EMITTER_ROLE = ethers.keccak256(ethers.toUtf8Bytes("EMITTER_ROLE"));

  // EventEmitter: set PoolRegistry so registered pools are auto-authorized to emit
  console.log("  EventEmitter.setPoolRegistry(PoolRegistry)");
  await (await eventEmitterProxy.setPoolRegistry(addresses.PoolRegistry_proxy)).wait();

  // PoolRegistry: FACTORY_ROLE → Factory
  console.log("  PoolRegistry.grantRole(FACTORY_ROLE, Factory)");
  await (await registryProxy.grantRole(FACTORY_ROLE, addresses.SidioraFactory_proxy)).wait();

  // SidioraNFT: MINTER_ROLE → Factory
  console.log("  SidioraNFT.grantRole(MINTER_ROLE, Factory)");
  await (await nftProxy.grantRole(MINTER_ROLE, addresses.SidioraFactory_proxy)).wait();

  // SidioraNFT: STRATEGY_SETTER_ROLE → FeesRouter
  console.log("  SidioraNFT.grantRole(STRATEGY_SETTER_ROLE, FeesRouter)");
  await (await nftProxy.grantRole(STRATEGY_SETTER_ROLE, addresses.FeesRouter_proxy)).wait();

  // Treasury: DEPOSITOR_ROLE → FeeAccumulator
  console.log("  Treasury.grantRole(DEPOSITOR_ROLE, FeeAccumulator)");
  await (await treasuryProxy.grantRole(DEPOSITOR_ROLE, addresses.FeeAccumulator_proxy)).wait();

  // SidioraFactory: ROUTER_ROLE → Router
  console.log("  SidioraFactory.grantRole(ROUTER_ROLE, Router)");
  await (await factoryProxy.grantRole(ROUTER_ROLE, addresses.Router_proxy)).wait();

  // FeeAccumulator: FEES_ROUTER_ROLE → FeesRouter
  console.log("  FeeAccumulator.grantRole(FEES_ROUTER_ROLE, FeesRouter)");
  await (await accProxy.grantRole(FEES_ROUTER_ROLE, addresses.FeesRouter_proxy)).wait();

  // FeeAccumulator: FACTORY_ROLE → Factory (so Factory can auto-authorize pools)
  const ACC_FACTORY_ROLE = ethers.keccak256(ethers.toUtf8Bytes("FACTORY_ROLE"));
  console.log("  FeeAccumulator.grantRole(FACTORY_ROLE, Factory)");
  await (await accProxy.grantRole(ACC_FACTORY_ROLE, addresses.SidioraFactory_proxy)).wait();

  // EventEmitter: authorize all contracts that emit events
  // NOTE: EventEmitter uses setAuthorizedEmitter(), NOT AccessControl roles
  const emitters = [
    addresses.SidioraFactory_proxy,
    addresses.ProtocolConfig_proxy,
    addresses.PoolRegistry_proxy,
    addresses.FeeAccumulator_proxy,
    addresses.SidioraNFT_proxy,
    addresses.Router_proxy,
    addresses.FeesRouter_proxy,
    addresses.OpticalRegistry_proxy,
  ];
  for (const emitter of emitters) {
    console.log(`  EventEmitter.setAuthorizedEmitter(${emitter.slice(0, 10)}...)`);
    await (await eventEmitterProxy.setAuthorizedEmitter(emitter, true)).wait();
  }

  // Register optical presets in OpticalRegistry
  console.log("── Step 20: Register Opticals ──");
  const OptRegistry = await ethers.getContractFactory("OpticalRegistry");
  const optReg = OptRegistry.attach(addresses.OpticalRegistry_proxy);
  for (const [name, addr] of Object.entries(opticals)) {
    console.log(`  Registering ${name}...`);
    await (await optReg.registerOptical(addr, name, `Default ${name} preset`, 1, "Sidiora")).wait();
  }

  // ═══════════════════════════════════════════════════════════════════
  // Save deployment addresses
  // ═══════════════════════════════════════════════════════════════════
  addresses._meta = {
    deployer: deployer.address,
    network: (await ethers.provider.getNetwork()).chainId.toString(),
    timestamp: new Date().toISOString(),
    usdl: USDL,
    sid: SID,
    guardian: GUARDIAN,
    timelockDelay: TIMELOCK_DELAY,
  };

  const outDir = path.join(__dirname, "..", "deployments");
  if (!fs.existsSync(outDir)) fs.mkdirSync(outDir, { recursive: true });
  const outPath = path.join(outDir, `${NETWORK_TYPE}-addresses.json`);
  fs.writeFileSync(outPath, JSON.stringify(addresses, null, 2));
  console.log(`\n✅ Deployment complete! Addresses saved to ${outPath}`);
  console.log(`   Total contracts deployed: ${Object.keys(addresses).filter(k => !k.startsWith("_")).length}`);
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error("❌ Deployment failed:", error);
    process.exit(1);
  });
