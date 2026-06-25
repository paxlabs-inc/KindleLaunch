const { expect } = require("chai");
const { ethers } = require("hardhat");
const { ZERO_ADDRESS, HookFlags } = require("../helpers/constants");

describe("BuybackBurnOptical", function () {
  let buybackBurn;
  let deployer, alice;
  let poolAddr;

  const BUYBACK_BPS = 2000n; // 20% of fees go to buyback

  beforeEach(async function () {
    [deployer, alice] = await ethers.getSigners();

    const MockPool = await ethers.getContractFactory("MockPoolForOptical");
    const mockPool = await MockPool.deploy();
    await mockPool.waitForDeployment();
    poolAddr = await mockPool.getAddress();

    const BuybackBurn = await ethers.getContractFactory("BuybackBurnOptical");
    buybackBurn = await BuybackBurn.deploy(ZERO_ADDRESS, deployer.address, BUYBACK_BPS);
    await buybackBurn.waitForDeployment();
  });

  describe("Configuration", function () {
    it("should store immutable config correctly", async function () {
      expect(await buybackBurn.buybackBps()).to.equal(BUYBACK_BPS);
      expect(await buybackBurn.DEAD_ADDRESS()).to.equal("0x000000000000000000000000000000000000dEaD");
    });

    it("should return BEFORE_FEE_DISTRIBUTION flag only", async function () {
      expect(await buybackBurn.getFlags()).to.equal(HookFlags.BEFORE_FEE_DISTRIBUTION);
    });

    it("should revert if buyback exceeds 50%", async function () {
      const BuybackBurn = await ethers.getContractFactory("BuybackBurnOptical");
      await expect(
        BuybackBurn.deploy(ZERO_ADDRESS, deployer.address, 5001n)
      ).to.be.reverted;
    });
  });

  describe("beforeFeeDistribution", function () {
    it("should redirect buybackBps% of fee and accumulate", async function () {
      const feeAmount = ethers.parseUnits("100", 6);
      const adjustedFee = await buybackBurn.beforeFeeDistribution.staticCall(poolAddr, feeAmount);

      // 20% redirected → 80 USDL adjusted fee
      const expectedBuyback = (feeAmount * BUYBACK_BPS) / 10000n;
      expect(adjustedFee).to.equal(feeAmount - expectedBuyback);
    });

    it("should accumulate USDL across multiple fee distributions", async function () {
      const feeAmount = ethers.parseUnits("100", 6);

      // Two fee distributions
      await buybackBurn.beforeFeeDistribution(poolAddr, feeAmount);
      await buybackBurn.beforeFeeDistribution(poolAddr, feeAmount);

      // 20% of 100 * 2 = 40
      const expectedAccumulated = ((feeAmount * BUYBACK_BPS) / 10000n) * 2n;
      expect(await buybackBurn.getAccumulatedUsdl(poolAddr)).to.equal(expectedAccumulated);
    });

    it("should emit BuybackAccumulated event", async function () {
      const feeAmount = ethers.parseUnits("100", 6);
      const expectedBuyback = (feeAmount * BUYBACK_BPS) / 10000n;

      await expect(buybackBurn.beforeFeeDistribution(poolAddr, feeAmount))
        .to.emit(buybackBurn, "BuybackAccumulated")
        .withArgs(poolAddr, expectedBuyback);
    });

    it("should return full fee when buybackBps is 0", async function () {
      const BuybackBurn = await ethers.getContractFactory("BuybackBurnOptical");
      const zeroBuyback = await BuybackBurn.deploy(ZERO_ADDRESS, deployer.address, 0n);
      await zeroBuyback.waitForDeployment();

      const feeAmount = ethers.parseUnits("100", 6);
      const adjusted = await zeroBuyback.beforeFeeDistribution.staticCall(poolAddr, feeAmount);
      expect(adjusted).to.equal(feeAmount);
    });
  });

  describe("markBuybackExecuted", function () {
    it("should reset accumulated and emit event", async function () {
      await buybackBurn.beforeFeeDistribution(poolAddr, ethers.parseUnits("100", 6));
      const accumulated = await buybackBurn.getAccumulatedUsdl(poolAddr);
      expect(accumulated).to.be.gt(0);

      await expect(buybackBurn.markBuybackExecuted(poolAddr))
        .to.emit(buybackBurn, "BuybackExecuted")
        .withArgs(poolAddr, accumulated);

      expect(await buybackBurn.getAccumulatedUsdl(poolAddr)).to.equal(0);
    });

    it("should revert when nothing to execute", async function () {
      await expect(
        buybackBurn.markBuybackExecuted(poolAddr)
      ).to.be.revertedWithCustomError(buybackBurn, "NothingToExecute");
    });

    it("should revert for non-owner", async function () {
      await buybackBurn.beforeFeeDistribution(poolAddr, ethers.parseUnits("100", 6));
      await expect(
        buybackBurn.connect(alice).markBuybackExecuted(poolAddr)
      ).to.be.revertedWithCustomError(buybackBurn, "NotOwner");
    });
  });
});
