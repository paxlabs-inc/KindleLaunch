const { expect } = require("chai");
const { ethers } = require("hardhat");
const { PROTOCOL_FEE_BPS, BPS_DENOMINATOR, DEAD_ADDRESS } = require("../helpers/constants");

describe("Phase 4: Data Layer Integration", function () {
  let eventEmitter, eventEmitterProxy;
  let registryProxy, accumulatorProxy, configProxy, treasuryProxy;
  let usdl, token;
  let deployer, alice, bob, poolSigner, feesRouterSigner, factorySigner;

  before(async function () {
    [deployer, alice, bob, poolSigner, feesRouterSigner, factorySigner] = await ethers.getSigners();
  });

  beforeEach(async function () {
    const Proxy = await ethers.getContractFactory("UUPSProxy");

    // --- Deploy real EventEmitter behind proxy ---
    const EventEmitter = await ethers.getContractFactory("EventEmitter");
    const eeImpl = await EventEmitter.deploy();
    await eeImpl.waitForDeployment();
    let proxy = await Proxy.deploy(
      await eeImpl.getAddress(),
      EventEmitter.interface.encodeFunctionData("initialize", [deployer.address])
    );
    await proxy.waitForDeployment();
    eventEmitterProxy = EventEmitter.attach(await proxy.getAddress());

    // --- Deploy mock USDL + token ---
    const MockERC20 = await ethers.getContractFactory("MockERC20");
    usdl = await MockERC20.deploy("USD Ledger", "USDL", 6);
    await usdl.waitForDeployment();
    token = await MockERC20.deploy("Launchpad Token", "LAUNCH", 18);
    await token.waitForDeployment();

    // --- Deploy ProtocolConfig ---
    const Config = await ethers.getContractFactory("ProtocolConfig");
    const configImpl = await Config.deploy();
    await configImpl.waitForDeployment();
    proxy = await Proxy.deploy(
      await configImpl.getAddress(),
      Config.interface.encodeFunctionData("initialize", [
        await usdl.getAddress(),
        await eventEmitterProxy.getAddress(),
        deployer.address,
      ])
    );
    await proxy.waitForDeployment();
    configProxy = Config.attach(await proxy.getAddress());

    // --- Deploy Treasury ---
    const Treasury = await ethers.getContractFactory("Treasury");
    const treasuryImpl = await Treasury.deploy();
    await treasuryImpl.waitForDeployment();
    proxy = await Proxy.deploy(
      await treasuryImpl.getAddress(),
      Treasury.interface.encodeFunctionData("initialize", [
        await eventEmitterProxy.getAddress(),
        deployer.address,
      ])
    );
    await proxy.waitForDeployment();
    treasuryProxy = Treasury.attach(await proxy.getAddress());

    // --- Deploy PoolRegistry ---
    const Registry = await ethers.getContractFactory("PoolRegistry");
    const registryImpl = await Registry.deploy();
    await registryImpl.waitForDeployment();
    proxy = await Proxy.deploy(
      await registryImpl.getAddress(),
      Registry.interface.encodeFunctionData("initialize", [
        await eventEmitterProxy.getAddress(),
        deployer.address,
      ])
    );
    await proxy.waitForDeployment();
    registryProxy = Registry.attach(await proxy.getAddress());

    // --- Deploy FeeAccumulator ---
    const Acc = await ethers.getContractFactory("FeeAccumulator");
    const accImpl = await Acc.deploy();
    await accImpl.waitForDeployment();
    proxy = await Proxy.deploy(
      await accImpl.getAddress(),
      Acc.interface.encodeFunctionData("initialize", [
        await configProxy.getAddress(),
        await treasuryProxy.getAddress(),
        await registryProxy.getAddress(),
        await eventEmitterProxy.getAddress(),
        await usdl.getAddress(),
        deployer.address,
      ])
    );
    await proxy.waitForDeployment();
    accumulatorProxy = Acc.attach(await proxy.getAddress());

    // --- Wire roles ---
    // EventEmitter: authorize ProtocolConfig and PoolRegistry as emitters
    await eventEmitterProxy.setAuthorizedEmitter(await configProxy.getAddress(), true);
    await eventEmitterProxy.setAuthorizedEmitter(await registryProxy.getAddress(), true);
    await eventEmitterProxy.setAuthorizedEmitter(await accumulatorProxy.getAddress(), true);

    // PoolRegistry: grant FACTORY_ROLE to factorySigner
    const FACTORY_ROLE = ethers.keccak256(ethers.toUtf8Bytes("FACTORY_ROLE"));
    await registryProxy.grantRole(FACTORY_ROLE, factorySigner.address);

    // FeeAccumulator: grant POOL_ROLE and FEES_ROUTER_ROLE
    const POOL_ROLE = ethers.keccak256(ethers.toUtf8Bytes("POOL_ROLE"));
    const FEES_ROUTER_ROLE = ethers.keccak256(ethers.toUtf8Bytes("FEES_ROUTER_ROLE"));
    await accumulatorProxy.grantRole(POOL_ROLE, poolSigner.address);
    await accumulatorProxy.grantRole(FEES_ROUTER_ROLE, feesRouterSigner.address);

    // Treasury: grant DEPOSITOR_ROLE to FeeAccumulator
    const DEPOSITOR_ROLE = ethers.keccak256(ethers.toUtf8Bytes("DEPOSITOR_ROLE"));
    await treasuryProxy.grantRole(DEPOSITOR_ROLE, await accumulatorProxy.getAddress());

    // Register pool in PoolRegistry
    await registryProxy.connect(factorySigner).register(
      poolSigner.address, await token.getAddress(), alice.address, ethers.ZeroAddress, 1
    );

    // Fund pool signer with USDL
    await usdl.mint(poolSigner.address, ethers.parseUnits("100000", 6));
  });

  async function recordFee(amount) {
    await usdl.connect(poolSigner).transfer(await accumulatorProxy.getAddress(), amount);
    await accumulatorProxy.connect(poolSigner).recordFee(poolSigner.address, amount);
  }

  it("EventEmitter + PoolRegistry: registration emits correct event through real EventEmitter", async function () {
    // Deploy a second token for a new pool registration
    const MockERC20 = await ethers.getContractFactory("MockERC20");
    const token2 = await MockERC20.deploy("Token 2", "TK2", 18);
    await token2.waitForDeployment();

    await expect(
      registryProxy.connect(factorySigner).register(
        bob.address, await token2.getAddress(), alice.address, ethers.ZeroAddress, 2
      )
    ).to.emit(registryProxy, "PoolRegistered");

    // Verify registry state
    expect(await registryProxy.getPoolCount()).to.equal(2);
    expect(await registryProxy.getPoolByToken(await token2.getAddress())).to.equal(bob.address);
  });

  it("FeeAccumulator + Treasury: protocol fees arrive in Treasury", async function () {
    const feeAmount = ethers.parseUnits("100", 6);
    await recordFee(feeAmount);

    const expectedProtocolCut = (feeAmount * PROTOCOL_FEE_BPS) / BPS_DENOMINATOR;
    expect(await treasuryProxy.getBalance(await usdl.getAddress())).to.equal(expectedProtocolCut);

    // Verify actual USDL balance in treasury contract
    const treasuryAddr = await treasuryProxy.getAddress();
    expect(await usdl.balanceOf(treasuryAddr)).to.equal(expectedProtocolCut);
  });

  it("Full CLAIM flow: recordFee -> claim", async function () {
    const feeAmount = ethers.parseUnits("50", 6);
    await recordFee(feeAmount);

    const expectedPoolCut = feeAmount - (feeAmount * PROTOCOL_FEE_BPS) / BPS_DENOMINATOR;
    const aliceBalBefore = await usdl.balanceOf(alice.address);

    await accumulatorProxy.connect(feesRouterSigner).claim(poolSigner.address, alice.address);

    const aliceBalAfter = await usdl.balanceOf(alice.address);
    expect(aliceBalAfter - aliceBalBefore).to.equal(expectedPoolCut);
    expect(await accumulatorProxy.getAccumulatedFees(poolSigner.address)).to.equal(0);
  });

  it("Full BURN flow: recordFee -> burn", async function () {
    const feeAmount = ethers.parseUnits("50", 6);
    await recordFee(feeAmount);

    const expectedPoolCut = feeAmount - (feeAmount * PROTOCOL_FEE_BPS) / BPS_DENOMINATOR;
    const deadBefore = await usdl.balanceOf(DEAD_ADDRESS);

    await accumulatorProxy.connect(feesRouterSigner).burn(poolSigner.address);

    const deadAfter = await usdl.balanceOf(DEAD_ADDRESS);
    expect(deadAfter - deadBefore).to.equal(expectedPoolCut);
    expect(await accumulatorProxy.getAccumulatedFees(poolSigner.address)).to.equal(0);
  });

  it("Full AIRDROP flow: recordFee -> triggerAirdrop -> claimAirdrop", async function () {
    // Mint tokens to holders
    await token.mint(alice.address, ethers.parseUnits("700", 6));
    await token.mint(bob.address, ethers.parseUnits("300", 6));

    const feeAmount = ethers.parseUnits("100", 6);
    await recordFee(feeAmount);

    const expectedPoolCut = feeAmount - (feeAmount * PROTOCOL_FEE_BPS) / BPS_DENOMINATOR;

    // Trigger airdrop
    await accumulatorProxy.connect(feesRouterSigner).triggerAirdrop(poolSigner.address);
    expect(await accumulatorProxy.getAirdropEpoch(poolSigner.address)).to.equal(1);
    expect(await accumulatorProxy.getAirdropBalance(poolSigner.address)).to.equal(expectedPoolCut);

    // Alice claims (70% share)
    const aliceBefore = await usdl.balanceOf(alice.address);
    await accumulatorProxy.claimAirdrop(poolSigner.address, alice.address);
    const aliceAfter = await usdl.balanceOf(alice.address);
    const aliceShare = (expectedPoolCut * 700n) / 1000n;
    expect(aliceAfter - aliceBefore).to.equal(aliceShare);

    // Bob claims (30% share)
    const bobBefore = await usdl.balanceOf(bob.address);
    await accumulatorProxy.claimAirdrop(poolSigner.address, bob.address);
    const bobAfter = await usdl.balanceOf(bob.address);
    const bobShare = (expectedPoolCut * 300n) / 1000n;
    expect(bobAfter - bobBefore).to.equal(bobShare);

    // Double claim reverts
    await expect(
      accumulatorProxy.claimAirdrop(poolSigner.address, alice.address)
    ).to.be.revertedWithCustomError(accumulatorProxy, "AlreadyClaimed");
  });

  it("Full LP_REWARDS flow: recordFee -> sendLpRewards to pool", async function () {
    const feeAmount = ethers.parseUnits("50", 6);
    await recordFee(feeAmount);

    const expectedPoolCut = feeAmount - (feeAmount * PROTOCOL_FEE_BPS) / BPS_DENOMINATOR;
    const poolBalBefore = await usdl.balanceOf(poolSigner.address);

    await accumulatorProxy.connect(feesRouterSigner).sendLpRewards(poolSigner.address);

    const poolBalAfter = await usdl.balanceOf(poolSigner.address);
    expect(poolBalAfter - poolBalBefore).to.equal(expectedPoolCut);
    expect(await accumulatorProxy.getAccumulatedFees(poolSigner.address)).to.equal(0);
  });

  it("ProtocolConfig updates emit through real EventEmitter", async function () {
    await expect(configProxy.setBaseFeeBps(50))
      .to.emit(eventEmitterProxy, "ConfigUpdated");
  });
});
