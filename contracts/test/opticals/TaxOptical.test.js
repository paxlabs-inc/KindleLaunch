const { expect } = require("chai");
const { ethers } = require("hardhat");
const { ZERO_ADDRESS, HookFlags } = require("../helpers/constants");

describe("TaxOptical", function () {
  let taxOptical;
  let deployer, alice;

  const BUY_TAX_BPS = 200n; // 2%
  const SELL_TAX_BPS = 300n; // 3%

  beforeEach(async function () {
    [deployer, alice] = await ethers.getSigners();

    const TaxOptical = await ethers.getContractFactory("TaxOptical");
    taxOptical = await TaxOptical.deploy(
      ZERO_ADDRESS, deployer.address, BUY_TAX_BPS, SELL_TAX_BPS
    );
    await taxOptical.waitForDeployment();
  });

  describe("Configuration", function () {
    it("should store immutable config correctly", async function () {
      expect(await taxOptical.buyTaxBps()).to.equal(BUY_TAX_BPS);
      expect(await taxOptical.sellTaxBps()).to.equal(SELL_TAX_BPS);
      expect(await taxOptical.MAX_TAX_BPS()).to.equal(1000n);
    });

    it("should return BEFORE_SWAP flag only", async function () {
      expect(await taxOptical.getFlags()).to.equal(HookFlags.BEFORE_SWAP);
    });

    it("should revert if buy tax exceeds 10%", async function () {
      const TaxOptical = await ethers.getContractFactory("TaxOptical");
      await expect(
        TaxOptical.deploy(ZERO_ADDRESS, deployer.address, 1001n, 0n)
      ).to.be.revertedWithCustomError(taxOptical, "TaxTooHigh");
    });

    it("should revert if sell tax exceeds 10%", async function () {
      const TaxOptical = await ethers.getContractFactory("TaxOptical");
      await expect(
        TaxOptical.deploy(ZERO_ADDRESS, deployer.address, 0n, 1001n)
      ).to.be.revertedWithCustomError(taxOptical, "TaxTooHigh");
    });
  });

  describe("beforeSwap tax", function () {
    it("should apply buy tax as negative amountDelta", async function () {
      const amountIn = ethers.parseUnits("1000", 6);
      const result = await taxOptical.beforeSwap.staticCall(
        deployer.address, alice.address, true, amountIn
      );
      expect(result.proceed).to.be.true;
      // 2% of 1000 = 20, so delta = -20
      const expectedDelta = -((amountIn * BUY_TAX_BPS) / 10000n);
      expect(result.amountDelta).to.equal(expectedDelta);
    });

    it("should apply sell tax as negative amountDelta", async function () {
      const amountIn = ethers.parseUnits("500", 6);
      const result = await taxOptical.beforeSwap.staticCall(
        deployer.address, alice.address, false, amountIn
      );
      expect(result.proceed).to.be.true;
      // 3% of 500 = 15, so delta = -15
      const expectedDelta = -((amountIn * SELL_TAX_BPS) / 10000n);
      expect(result.amountDelta).to.equal(expectedDelta);
    });

    it("should return zero delta when tax is zero", async function () {
      const TaxOptical = await ethers.getContractFactory("TaxOptical");
      const zeroTax = await TaxOptical.deploy(ZERO_ADDRESS, deployer.address, 0n, 0n);
      await zeroTax.waitForDeployment();

      const result = await zeroTax.beforeSwap.staticCall(
        deployer.address, alice.address, true, ethers.parseUnits("1000", 6)
      );
      expect(result.proceed).to.be.true;
      expect(result.amountDelta).to.equal(0);
    });

    it("should allow max 10% tax deployment", async function () {
      const TaxOptical = await ethers.getContractFactory("TaxOptical");
      const maxTax = await TaxOptical.deploy(ZERO_ADDRESS, deployer.address, 1000n, 1000n);
      await maxTax.waitForDeployment();

      const amountIn = ethers.parseUnits("1000", 6);
      const result = await maxTax.beforeSwap.staticCall(
        deployer.address, alice.address, true, amountIn
      );
      // 10% of 1000 = 100
      expect(result.amountDelta).to.equal(-(amountIn / 10n));
    });
  });
});
