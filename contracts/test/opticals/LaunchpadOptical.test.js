const { expect } = require("chai");
const { ethers } = require("hardhat");
const {
  ZERO_ADDRESS,
  HookFlags,
  PROTOCOL_FEE_BPS,
  BPS_DENOMINATOR,
  ONE_DAY,
  p6,
} = require("../helpers/constants");

describe("LaunchpadOptical", function () {
  let launchpad, launchpadAddress;
  let mockPool, mockPoolAddress;
  let token, usdl;
  let deployer, creator, teamWallet1, teamWallet2, randomUser, teamClaimAddr;

  // Default config
  const CLIFF_DURATION = 30n * ONE_DAY; // 30 days
  const VESTING_DURATION = 180n * ONE_DAY; // 180 days
  const CAPITAL_RAISE_BPS = 500n; // 5%
  const CAPITAL_RAISE_DURATION = 90n * ONE_DAY; // 90 days

  before(async function () {
    [deployer, creator, teamWallet1, teamWallet2, randomUser, teamClaimAddr] =
      await ethers.getSigners();
  });

  async function deployOptical(overrides = {}) {
    const MockPool = await ethers.getContractFactory("MockPoolForOptical");
    mockPool = await MockPool.deploy();
    await mockPool.waitForDeployment();
    mockPoolAddress = await mockPool.getAddress();

    // Deploy a mock token and link it to the pool
    const MockERC20 = await ethers.getContractFactory("MockERC20");
    token = await MockERC20.deploy("Test Token", "TEST", 6);
    await token.waitForDeployment();
    await mockPool.setTokenAddress(await token.getAddress());

    const Launchpad = await ethers.getContractFactory("LaunchpadOptical");
    launchpad = await Launchpad.deploy(
      ZERO_ADDRESS, // no registry check for unit tests
      overrides.owner || deployer.address,
      overrides.creator || creator.address,
      overrides.teamWallets || [teamWallet1.address, teamWallet2.address],
      overrides.cliffDuration ?? CLIFF_DURATION,
      overrides.vestingDuration ?? VESTING_DURATION,
      overrides.capitalRaiseBps ?? CAPITAL_RAISE_BPS,
      overrides.capitalRaiseDuration ?? CAPITAL_RAISE_DURATION,
      overrides.teamClaimAddress || teamClaimAddr.address,
      overrides.feeAccumulator || deployer.address // placeholder for unit tests
    );
    await launchpad.waitForDeployment();
    launchpadAddress = await launchpad.getAddress();
  }

  // ============ CONFIGURATION ============

  describe("Configuration", function () {
    beforeEach(async function () {
      await deployOptical();
    });

    it("should store immutable config correctly", async function () {
      expect(await launchpad.creator()).to.equal(creator.address);
      expect(await launchpad.teamClaimAddress()).to.equal(teamClaimAddr.address);
      expect(await launchpad.cliffDuration()).to.equal(CLIFF_DURATION);
      expect(await launchpad.vestingDuration()).to.equal(VESTING_DURATION);
      expect(await launchpad.capitalRaiseBps()).to.equal(CAPITAL_RAISE_BPS);
      expect(await launchpad.capitalRaiseDuration()).to.equal(CAPITAL_RAISE_DURATION);
    });

    it("should return correct hook flags (beforeSwap | afterSwap | beforeFeeDistribution)", async function () {
      const flags = await launchpad.getFlags();
      const expected =
        HookFlags.BEFORE_SWAP | HookFlags.AFTER_SWAP | HookFlags.BEFORE_FEE_DISTRIBUTION;
      expect(flags).to.equal(expected);
    });

    it("should mark creator as vested", async function () {
      expect(await launchpad.isVested(creator.address)).to.be.true;
    });

    it("should mark team wallets as vested", async function () {
      expect(await launchpad.isVested(teamWallet1.address)).to.be.true;
      expect(await launchpad.isVested(teamWallet2.address)).to.be.true;
    });

    it("should NOT mark random addresses as vested", async function () {
      expect(await launchpad.isVested(randomUser.address)).to.be.false;
    });

    it("should revert if capitalRaiseBps exceeds max", async function () {
      await expect(
        deployOptical({ capitalRaiseBps: 1001n })
      ).to.be.revertedWithCustomError(
        await ethers.getContractFactory("LaunchpadOptical"),
        "CapitalRaiseTooHigh"
      );
    });

    it("should revert if cliffDuration exceeds max", async function () {
      await expect(
        deployOptical({ cliffDuration: 366n * ONE_DAY })
      ).to.be.revertedWithCustomError(
        await ethers.getContractFactory("LaunchpadOptical"),
        "DurationTooLong"
      );
    });

    it("should revert if too many team wallets", async function () {
      const tooMany = Array(21).fill(randomUser.address);
      await expect(
        deployOptical({ teamWallets: tooMany })
      ).to.be.revertedWithCustomError(
        await ethers.getContractFactory("LaunchpadOptical"),
        "TooManyTeamWallets"
      );
    });
  });

  // ============ VESTING: CLIFF ENFORCEMENT ============

  describe("beforeSwap — Cliff enforcement", function () {
    beforeEach(async function () {
      await deployOptical();
      // Mint tokens to creator (simulating pool allocation)
      await token.mint(creator.address, p6("1000000"));
    });

    it("should allow buys from vested wallet during cliff", async function () {
      const result = await launchpad.beforeSwap.staticCall(
        mockPoolAddress, creator.address, true, p6("100")
      );
      expect(result.proceed).to.be.true;
    });

    it("should block sells from creator during cliff", async function () {
      // First call to register pool start time
      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));

      const result = await launchpad.beforeSwap.staticCall(
        mockPoolAddress, creator.address, false, p6("100")
      );
      expect(result.proceed).to.be.false;
    });

    it("should block sells from team wallet during cliff", async function () {
      await token.mint(teamWallet1.address, p6("500000"));
      // Register pool start
      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));

      const result = await launchpad.beforeSwap.staticCall(
        mockPoolAddress, teamWallet1.address, false, p6("100")
      );
      expect(result.proceed).to.be.false;
    });

    it("should allow sells from non-vested wallet during cliff", async function () {
      await token.mint(randomUser.address, p6("500000"));
      // Register pool start
      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));

      const result = await launchpad.beforeSwap.staticCall(
        mockPoolAddress, randomUser.address, false, p6("100")
      );
      expect(result.proceed).to.be.true;
    });

    it("should emit SellBlockedCliff event", async function () {
      // Register pool start
      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));

      await expect(
        launchpad.beforeSwap(mockPoolAddress, creator.address, false, p6("100"))
      ).to.emit(launchpad, "SellBlockedCliff");
    });

    it("should record pool start time on first interaction", async function () {
      expect(await launchpad.poolStartTime(mockPoolAddress)).to.equal(0);

      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));

      expect(await launchpad.poolStartTime(mockPoolAddress)).to.be.gt(0);
    });
  });

  // ============ VESTING: LINEAR UNLOCK ============

  describe("beforeSwap — Linear vesting", function () {
    beforeEach(async function () {
      await deployOptical();
      // Mint 1M tokens to creator
      await token.mint(creator.address, p6("1000000"));
      // Register pool start
      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));
    });

    it("should allow full sell after cliff + vesting complete", async function () {
      // Fast forward past cliff + vesting
      const totalDuration = CLIFF_DURATION + VESTING_DURATION + 1n;
      await ethers.provider.send("evm_increaseTime", [Number(totalDuration)]);
      await ethers.provider.send("evm_mine", []);

      const result = await launchpad.beforeSwap.staticCall(
        mockPoolAddress, creator.address, false, p6("1000000")
      );
      expect(result.proceed).to.be.true;
    });

    it("should allow partial sell at 50% vesting", async function () {
      // Fast forward to cliff + half vesting
      const elapsed = CLIFF_DURATION + VESTING_DURATION / 2n;
      await ethers.provider.send("evm_increaseTime", [Number(elapsed)]);
      await ethers.provider.send("evm_mine", []);

      // At 50% vesting, should be able to sell ~500k of 1M
      const result = await launchpad.beforeSwap.staticCall(
        mockPoolAddress, creator.address, false, p6("400000")
      );
      expect(result.proceed).to.be.true;
    });

    it("should block sell exceeding vested amount at 50% vesting", async function () {
      // Fast forward to cliff + half vesting
      const elapsed = CLIFF_DURATION + VESTING_DURATION / 2n;
      await ethers.provider.send("evm_increaseTime", [Number(elapsed)]);
      await ethers.provider.send("evm_mine", []);

      // Try to sell 600k of 1M at 50% vesting — should fail
      const result = await launchpad.beforeSwap.staticCall(
        mockPoolAddress, creator.address, false, p6("600000")
      );
      expect(result.proceed).to.be.false;
    });

    it("should emit SellBlockedVesting when exceeding limit", async function () {
      const elapsed = CLIFF_DURATION + VESTING_DURATION / 2n;
      await ethers.provider.send("evm_increaseTime", [Number(elapsed)]);
      await ethers.provider.send("evm_mine", []);

      await expect(
        launchpad.beforeSwap(mockPoolAddress, creator.address, false, p6("600000"))
      ).to.emit(launchpad, "SellBlockedVesting");
    });

    it("should allow sell right at cliff boundary", async function () {
      // Fast forward to exactly cliff end (0% vesting → 0 sellable)
      await ethers.provider.send("evm_increaseTime", [Number(CLIFF_DURATION)]);
      await ethers.provider.send("evm_mine", []);

      // Even 1 token should fail at 0% linear vesting (just passed cliff)
      // Actually at elapsed == cliffDuration, vestedFraction = 0, so maxSellable = 0
      const result = await launchpad.beforeSwap.staticCall(
        mockPoolAddress, creator.address, false, p6("1")
      );
      expect(result.proceed).to.be.false;
    });
  });

  // ============ AFTER SWAP: TRACKING ============

  describe("afterSwap — tokensSold tracking", function () {
    beforeEach(async function () {
      await deployOptical();
      await token.mint(creator.address, p6("1000000"));
      // Register pool start
      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));
    });

    it("should track tokens sold for vested wallet on sell", async function () {
      expect(await launchpad.tokensSold(mockPoolAddress, creator.address)).to.equal(0);

      // Simulate afterSwap for a sell
      await launchpad.afterSwap(
        mockPoolAddress, creator.address, false, p6("1000"), p6("50")
      );

      expect(await launchpad.tokensSold(mockPoolAddress, creator.address)).to.equal(p6("1000"));
    });

    it("should NOT track buys", async function () {
      await launchpad.afterSwap(
        mockPoolAddress, creator.address, true, p6("100"), p6("5000")
      );

      expect(await launchpad.tokensSold(mockPoolAddress, creator.address)).to.equal(0);
    });

    it("should NOT track sells from non-vested wallets", async function () {
      await launchpad.afterSwap(
        mockPoolAddress, randomUser.address, false, p6("1000"), p6("50")
      );

      expect(await launchpad.tokensSold(mockPoolAddress, randomUser.address)).to.equal(0);
    });

    it("should accumulate across multiple sells", async function () {
      await launchpad.afterSwap(
        mockPoolAddress, creator.address, false, p6("1000"), p6("50")
      );
      await launchpad.afterSwap(
        mockPoolAddress, creator.address, false, p6("2000"), p6("80")
      );

      expect(await launchpad.tokensSold(mockPoolAddress, creator.address)).to.equal(p6("3000"));
    });

    it("should return afterSwap selector", async function () {
      const selector = await launchpad.afterSwap.staticCall(
        mockPoolAddress, randomUser.address, true, p6("100"), p6("5000")
      );
      // IOptical.afterSwap.selector
      const iface = new ethers.Interface([
        "function afterSwap(address,address,bool,uint256,uint256) returns (bytes4)",
      ]);
      const expectedSelector = iface.getFunction("afterSwap").selector;
      expect(selector).to.equal(expectedSelector);
    });
  });

  // ============ CAPITAL RAISE: beforeFeeDistribution ============

  describe("beforeFeeDistribution — Capital raise diversion", function () {
    beforeEach(async function () {
      await deployOptical();
      // Register pool start
      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));
    });

    it("should divert capitalRaiseBps of fee during raise period", async function () {
      const feeAmount = p6("100");
      const adjustedFee = await launchpad.beforeFeeDistribution.staticCall(
        mockPoolAddress, feeAmount
      );

      const expectedDivert = (feeAmount * CAPITAL_RAISE_BPS) / BPS_DENOMINATOR;
      expect(adjustedFee).to.equal(feeAmount - expectedDivert);
    });

    it("should accumulate USDL accounting for team", async function () {
      const feeAmount = p6("100");
      await launchpad.beforeFeeDistribution(mockPoolAddress, feeAmount);

      const expectedDivert = (feeAmount * CAPITAL_RAISE_BPS) / BPS_DENOMINATOR;
      expect(await launchpad.accumulatedUsdl(mockPoolAddress)).to.equal(expectedDivert);
      expect(await launchpad.totalRaised(mockPoolAddress)).to.equal(expectedDivert);
    });

    it("should accumulate across multiple fee recordings", async function () {
      await launchpad.beforeFeeDistribution(mockPoolAddress, p6("100"));
      await launchpad.beforeFeeDistribution(mockPoolAddress, p6("200"));

      const expected1 = (p6("100") * CAPITAL_RAISE_BPS) / BPS_DENOMINATOR;
      const expected2 = (p6("200") * CAPITAL_RAISE_BPS) / BPS_DENOMINATOR;
      expect(await launchpad.accumulatedUsdl(mockPoolAddress)).to.equal(expected1 + expected2);
    });

    it("should emit CapitalRaiseAccumulated event", async function () {
      await expect(
        launchpad.beforeFeeDistribution(mockPoolAddress, p6("100"))
      ).to.emit(launchpad, "CapitalRaiseAccumulated");
    });

    it("should stop diverting after capitalRaiseDuration expires", async function () {
      // Fast forward past raise duration
      await ethers.provider.send("evm_increaseTime", [Number(CAPITAL_RAISE_DURATION + 1n)]);
      await ethers.provider.send("evm_mine", []);

      const feeAmount = p6("100");
      const adjustedFee = await launchpad.beforeFeeDistribution.staticCall(
        mockPoolAddress, feeAmount
      );

      // Should return full fee (no diversion)
      expect(adjustedFee).to.equal(feeAmount);
    });

    it("should return full fee if capitalRaiseBps is 0", async function () {
      await deployOptical({ capitalRaiseBps: 0n });
      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));

      const feeAmount = p6("100");
      const adjustedFee = await launchpad.beforeFeeDistribution.staticCall(
        mockPoolAddress, feeAmount
      );
      expect(adjustedFee).to.equal(feeAmount);
    });

    it("should return full fee if pool not yet started", async function () {
      // Deploy fresh optical without triggering beforeSwap
      await deployOptical();

      const feeAmount = p6("100");
      const adjustedFee = await launchpad.beforeFeeDistribution.staticCall(
        mockPoolAddress, feeAmount
      );
      expect(adjustedFee).to.equal(feeAmount);
    });
  });

  // ============ VIEW FUNCTIONS ============

  describe("View functions", function () {
    beforeEach(async function () {
      await deployOptical();
      await token.mint(creator.address, p6("1000000"));
      // Register pool start
      await launchpad.beforeSwap(mockPoolAddress, randomUser.address, true, p6("1"));
    });

    it("getVestingInfo — during cliff", async function () {
      const info = await launchpad.getVestingInfo(mockPoolAddress, creator.address);
      expect(info.vested).to.be.true;
      expect(info.vestedAmount).to.equal(0);
      expect(info.maxSellableNow).to.equal(0);
    });

    it("getVestingInfo — after full vesting", async function () {
      await ethers.provider.send("evm_increaseTime", [
        Number(CLIFF_DURATION + VESTING_DURATION + 1n),
      ]);
      await ethers.provider.send("evm_mine", []);

      const info = await launchpad.getVestingInfo(mockPoolAddress, creator.address);
      expect(info.vested).to.be.true;
      // originalAllocation = balance + sold = 1M + 0
      expect(info.vestedAmount).to.equal(p6("1000000"));
      expect(info.maxSellableNow).to.equal(p6("1000000"));
    });

    it("getVestingInfo — non-vested wallet", async function () {
      const info = await launchpad.getVestingInfo(mockPoolAddress, randomUser.address);
      expect(info.vested).to.be.false;
      expect(info.vestedAmount).to.equal(0);
    });

    it("getCapitalRaiseInfo — active", async function () {
      const info = await launchpad.getCapitalRaiseInfo(mockPoolAddress);
      expect(info.isActive).to.be.true;
      expect(info.accumulated).to.equal(0);
    });

    it("getCapitalRaiseInfo — expired", async function () {
      await ethers.provider.send("evm_increaseTime", [Number(CAPITAL_RAISE_DURATION + 1n)]);
      await ethers.provider.send("evm_mine", []);

      const info = await launchpad.getCapitalRaiseInfo(mockPoolAddress);
      expect(info.isActive).to.be.false;
    });

    it("isTeamWallet", async function () {
      expect(await launchpad.isTeamWallet(creator.address)).to.be.true;
      expect(await launchpad.isTeamWallet(teamWallet1.address)).to.be.true;
      expect(await launchpad.isTeamWallet(randomUser.address)).to.be.false;
    });
  });

  // ============ CAPITAL CLAIM (with FeeAccumulator) ============

  describe("claimCapital — integration with FeeAccumulator", function () {
    let accumulatorProxy, configProxy, treasuryProxy, registryProxy, eventEmitter;
    let poolSigner, poolAddr;

    beforeEach(async function () {
      // Get an extra signer to act as the "pool" for FeeAccumulator calls
      const signers = await ethers.getSigners();
      poolSigner = signers[6]; // use a signer not already assigned
      poolAddr = poolSigner.address;

      // --- Deploy full stack needed for claim flow ---
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      usdl = await MockERC20.deploy("USD Ledger", "USDL", 6);
      await usdl.waitForDeployment();

      token = await MockERC20.deploy("Test Token", "TEST", 6);
      await token.waitForDeployment();

      const MockEE = await ethers.getContractFactory("MockEventEmitter");
      eventEmitter = await MockEE.deploy();
      await eventEmitter.waitForDeployment();

      // ProtocolConfig
      const Config = await ethers.getContractFactory("ProtocolConfig");
      const configImpl = await Config.deploy();
      await configImpl.waitForDeployment();
      const configInitData = Config.interface.encodeFunctionData("initialize", [
        await usdl.getAddress(),
        await eventEmitter.getAddress(),
        deployer.address,
      ]);
      const Proxy = await ethers.getContractFactory("UUPSProxy");
      let proxy = await Proxy.deploy(await configImpl.getAddress(), configInitData);
      await proxy.waitForDeployment();
      configProxy = Config.attach(await proxy.getAddress());

      // Treasury
      const Treasury = await ethers.getContractFactory("Treasury");
      const treasuryImpl = await Treasury.deploy();
      await treasuryImpl.waitForDeployment();
      const treasuryInitData = Treasury.interface.encodeFunctionData("initialize", [
        await eventEmitter.getAddress(),
        deployer.address,
      ]);
      proxy = await Proxy.deploy(await treasuryImpl.getAddress(), treasuryInitData);
      await proxy.waitForDeployment();
      treasuryProxy = Treasury.attach(await proxy.getAddress());

      // PoolRegistry
      const Registry = await ethers.getContractFactory("PoolRegistry");
      const registryImpl = await Registry.deploy();
      await registryImpl.waitForDeployment();
      const registryInitData = Registry.interface.encodeFunctionData("initialize", [
        await eventEmitter.getAddress(),
        deployer.address,
      ]);
      proxy = await Proxy.deploy(await registryImpl.getAddress(), registryInitData);
      await proxy.waitForDeployment();
      registryProxy = Registry.attach(await proxy.getAddress());

      // FeeAccumulator
      const Acc = await ethers.getContractFactory("FeeAccumulator");
      const accImpl = await Acc.deploy();
      await accImpl.waitForDeployment();
      const accInitData = Acc.interface.encodeFunctionData("initialize", [
        await configProxy.getAddress(),
        await treasuryProxy.getAddress(),
        await registryProxy.getAddress(),
        await eventEmitter.getAddress(),
        await usdl.getAddress(),
        deployer.address,
      ]);
      proxy = await Proxy.deploy(await accImpl.getAddress(), accInitData);
      await proxy.waitForDeployment();
      accumulatorProxy = Acc.attach(await proxy.getAddress());

      // Deploy LaunchpadOptical with real FeeAccumulator
      // Use poolAddr (signer) as the pool for FeeAccumulator interactions
      const Launchpad = await ethers.getContractFactory("LaunchpadOptical");
      launchpad = await Launchpad.deploy(
        ZERO_ADDRESS,
        deployer.address,
        creator.address,
        [teamWallet1.address],
        CLIFF_DURATION,
        VESTING_DURATION,
        CAPITAL_RAISE_BPS,
        CAPITAL_RAISE_DURATION,
        teamClaimAddr.address,
        await accumulatorProxy.getAddress()
      );
      await launchpad.waitForDeployment();
      launchpadAddress = await launchpad.getAddress();

      // Grant roles
      const POOL_ROLE = ethers.keccak256(ethers.toUtf8Bytes("POOL_ROLE"));
      const OPTICAL_CLAIM_ROLE = ethers.keccak256(ethers.toUtf8Bytes("OPTICAL_CLAIM_ROLE"));
      const DEPOSITOR_ROLE = ethers.keccak256(ethers.toUtf8Bytes("DEPOSITOR_ROLE"));
      const FACTORY_ROLE = ethers.keccak256(ethers.toUtf8Bytes("FACTORY_ROLE"));

      await accumulatorProxy.grantRole(POOL_ROLE, poolAddr);
      await accumulatorProxy.grantRole(OPTICAL_CLAIM_ROLE, launchpadAddress);
      await treasuryProxy.grantRole(DEPOSITOR_ROLE, await accumulatorProxy.getAddress());
      await registryProxy.grantRole(FACTORY_ROLE, deployer.address);

      // Register pool (signer address) in registry WITH optical address
      await registryProxy.register(
        poolAddr,
        await token.getAddress(),
        creator.address,
        launchpadAddress, // optical address
        1
      );

      // Register pool start time in optical (use poolAddr as the pool)
      await launchpad.beforeSwap(poolAddr, randomUser.address, true, p6("1"));

      // Mint USDL to pool signer (simulates pool having fee USDL)
      await usdl.mint(poolAddr, p6("100000"));
      // Pool signer approves FeeAccumulator
      await usdl.connect(poolSigner).approve(await accumulatorProxy.getAddress(), ethers.MaxUint256);
    });

    async function recordFeeFromPool(amount) {
      // Transfer USDL to FeeAccumulator first, then record
      await usdl.connect(poolSigner).transfer(await accumulatorProxy.getAddress(), amount);
      await accumulatorProxy.connect(poolSigner).recordFee(poolAddr, amount);
    }

    it("should divert fees through FeeAccumulator and track optical surplus", async function () {
      await recordFeeFromPool(p6("1000"));

      // Protocol cut = 10% = 100. Pool cut = 900.
      // Capital raise = 5% of 900 = 45
      const surplus = await accumulatorProxy.getOpticalSurplus(poolAddr);
      const poolCut = p6("1000") - (p6("1000") * PROTOCOL_FEE_BPS) / BPS_DENOMINATOR;
      const expectedDivert = (poolCut * CAPITAL_RAISE_BPS) / BPS_DENOMINATOR;
      expect(surplus).to.equal(expectedDivert);
    });

    it("should allow team to claim accumulated capital", async function () {
      await recordFeeFromPool(p6("1000"));

      const poolCut = p6("1000") - (p6("1000") * PROTOCOL_FEE_BPS) / BPS_DENOMINATOR;
      const expectedDivert = (poolCut * CAPITAL_RAISE_BPS) / BPS_DENOMINATOR;

      const balBefore = await usdl.balanceOf(teamClaimAddr.address);
      await launchpad.connect(teamClaimAddr).claimCapital(poolAddr);
      const balAfter = await usdl.balanceOf(teamClaimAddr.address);

      expect(balAfter - balBefore).to.equal(expectedDivert);
    });

    it("should reset accumulated amount after claim", async function () {
      await recordFeeFromPool(p6("1000"));
      await launchpad.connect(teamClaimAddr).claimCapital(poolAddr);

      expect(await launchpad.accumulatedUsdl(poolAddr)).to.equal(0);
    });

    it("should emit CapitalRaiseClaimed event", async function () {
      await recordFeeFromPool(p6("1000"));

      await expect(
        launchpad.connect(teamClaimAddr).claimCapital(poolAddr)
      ).to.emit(launchpad, "CapitalRaiseClaimed");
    });

    it("should revert if not teamClaimAddress", async function () {
      await recordFeeFromPool(p6("1000"));

      await expect(
        launchpad.connect(randomUser).claimCapital(poolAddr)
      ).to.be.revertedWithCustomError(launchpad, "NotTeam");
    });

    it("should revert if nothing to claim", async function () {
      await expect(
        launchpad.connect(teamClaimAddr).claimCapital(poolAddr)
      ).to.be.revertedWithCustomError(launchpad, "NothingToClaim");
    });

    it("should not divert after capital raise expires", async function () {
      await ethers.provider.send("evm_increaseTime", [Number(CAPITAL_RAISE_DURATION + 1n)]);
      await ethers.provider.send("evm_mine", []);

      const surplusBefore = await accumulatorProxy.getOpticalSurplus(poolAddr);
      await recordFeeFromPool(p6("1000"));
      const surplusAfter = await accumulatorProxy.getOpticalSurplus(poolAddr);

      // No new surplus should be added
      expect(surplusAfter).to.equal(surplusBefore);
    });
  });
});
