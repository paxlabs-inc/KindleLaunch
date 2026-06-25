/**
 * Quick inspector — prints the live on-chain state so we know what USDL /
 * pools / reserves to test against. Run after deploy-pecor-meta-ag.js to
 * get a factual baseline before writing on-chain tests.
 *
 *   npx hardhat run scripts/meta-ag/inspect-live-state.js --network localhost
 */

const { ethers } = require("hardhat");
const fs = require("fs");
const path = require("path");

async function main() {
  const [deployer] = await ethers.getSigners();
  const net = await ethers.provider.getNetwork();
  const NETWORK_TYPE = process.env.NETWORK_TYPE || (Number(net.chainId) === 125 ? "paxeer-network" : "localhost");
  const addrPath = path.join(__dirname, "..", "..", "deployments", `${NETWORK_TYPE}-addresses.json`);
  const A = JSON.parse(fs.readFileSync(addrPath, "utf8"));

  const USDL = A._meta.usdl;
  const SID = A._meta.sid;
  const WPAX = A.WPAX;
  const ROUTER = A.Router_proxy;
  const QUOTER = A.Quoter_proxy;
  const POOL_REG = A.PoolRegistry_proxy;
  const CONFIG = A.ProtocolConfig_proxy;
  const META_ROUTER = A.MetaAGRouter_proxy;
  const VAULT = A.PECORVault_proxy;
  const ORACLE = A.PriceOracle_proxy;

  console.log(`\nNetwork: ${NETWORK_TYPE} (chainId ${net.chainId})`);
  console.log(`Deployer: ${deployer.address}`);
  console.log(`Deployer native balance: ${ethers.formatEther(await ethers.provider.getBalance(deployer.address))}`);

  // USDL probe — try common surfaces
  const erc20Abi = [
    "function balanceOf(address) view returns (uint256)",
    "function totalSupply() view returns (uint256)",
    "function decimals() view returns (uint8)",
    "function symbol() view returns (string)",
    "function name() view returns (string)",
    "function owner() view returns (address)",
  ];
  const usdl = new ethers.Contract(USDL, erc20Abi, deployer);
  console.log(`\n[USDL @ ${USDL}]`);
  try { console.log(`  name:          ${await usdl.name()}`); } catch {}
  try { console.log(`  symbol:        ${await usdl.symbol()}`); } catch {}
  try { console.log(`  decimals:      ${await usdl.decimals()}`); } catch {}
  try {
    const ts = await usdl.totalSupply();
    console.log(`  totalSupply:   ${ethers.formatUnits(ts, 18)}`);
  } catch {}
  try {
    const bal = await usdl.balanceOf(deployer.address);
    console.log(`  deployer bal:  ${ethers.formatUnits(bal, 18)}`);
  } catch {}
  try { console.log(`  owner():       ${await usdl.owner()}`); } catch (e) { console.log(`  owner(): <not exposed> (${e.shortMessage || e.reason || "n/a"})`); }

  // Probe for mint signatures
  const mintAbis = [
    "function mint(address,uint256)",
    "function mint(uint256)",
    "function bridgeMint(address,uint256)",
    "function issue(address,uint256)",
  ];
  for (const sig of mintAbis) {
    try {
      const iface = new ethers.Interface([sig]);
      const frag = iface.fragments[0];
      const selector = iface.getFunction(frag.name).selector;
      const code = await ethers.provider.getCode(USDL);
      const hasSig = code.includes(selector.slice(2));
      console.log(`  bytecode has ${sig.padEnd(38)}: ${hasSig ? "YES" : "no"}`);
    } catch {}
  }

  // Sidiora PoolRegistry — how many pools?
  const poolRegAbi = [
    "function getPoolCount() view returns (uint256)",
    "function getPoolByToken(address) view returns (address)",
    "function getAllPools() view returns (address[])",
  ];
  const reg = new ethers.Contract(POOL_REG, poolRegAbi, deployer);
  console.log(`\n[Sidiora PoolRegistry @ ${POOL_REG}]`);
  try { console.log(`  getPoolCount(): ${await reg.getPoolCount()}`); } catch (e) { console.log(`  getPoolCount failed: ${e.shortMessage || e.reason || e.message}`); }
  try {
    const all = await reg.getAllPools();
    console.log(`  getAllPools():  ${all.length} pools`);
    for (const p of all.slice(0, 10)) console.log(`    - ${p}`);
  } catch (e) {
    console.log(`  getAllPools failed: ${e.shortMessage || e.reason || e.message}`);
  }

  // Meta-AG Router state
  const metaAbi = [
    "function adapterCount() view returns (uint256)",
    "function getAdapters() view returns (address[])",
    "function oracleHub() view returns (address)",
    "function maxOracleSanityDeviation() view returns (uint256)",
    "function paused() view returns (bool)",
  ];
  const meta = new ethers.Contract(META_ROUTER, metaAbi, deployer);
  console.log(`\n[MetaAGRouter @ ${META_ROUTER}]`);
  try { console.log(`  adapterCount:  ${await meta.adapterCount()}`); } catch {}
  try {
    const adapters = await meta.getAdapters();
    console.log(`  adapters:      ${adapters.length} registered`);
    for (const a of adapters) console.log(`    - ${a}`);
  } catch {}
  try { console.log(`  paused:        ${await meta.paused()}`); } catch {}

  // Vault state
  const vaultAbi = [
    "function getRegisteredTokens() view returns (address[])",
    "function getReserves(address) view returns (uint256)",
  ];
  const vault = new ethers.Contract(VAULT, vaultAbi, deployer);
  console.log(`\n[PECORVault @ ${VAULT}]`);
  try {
    const toks = await vault.getRegisteredTokens();
    console.log(`  registered tokens: ${toks.length}`);
    for (const t of toks) {
      const r = await vault.getReserves(t);
      console.log(`    - ${t}  reserves=${ethers.formatUnits(r, 18)}`);
    }
  } catch {}

  // Oracle prices
  const oracleAbi = [
    "function getPrice(address) view returns (uint256)",
    "function isRegistered(address) view returns (bool)",
  ];
  const oracle = new ethers.Contract(ORACLE, oracleAbi, deployer);
  console.log(`\n[PriceOracle @ ${ORACLE}]`);
  for (const [name, addr] of Object.entries({ USDL, WPAX })) {
    try {
      const p = await oracle.getPrice(addr);
      console.log(`  ${name} price: $${ethers.formatUnits(p, 18)}`);
    } catch (e) {
      console.log(`  ${name} price: <unavailable> (${e.shortMessage || e.reason || "n/a"})`);
    }
  }
}

main().then(() => process.exit(0)).catch((e) => { console.error(e); process.exit(1); });
