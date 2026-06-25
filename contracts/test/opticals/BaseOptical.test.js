const { expect } = require("chai");
const { ethers } = require("hardhat");
const { ZERO_ADDRESS } = require("../helpers/constants");

describe("IOptical + BaseOptical", function () {
  let mockOptical, mockOpticalWithFlags;
  let deployer, alice;

  beforeEach(async function () {
    [deployer, alice] = await ethers.getSigners();

    // Deploy base mock (no flags)
    const MockOptical = await ethers.getContractFactory("MockOptical");
    mockOptical = await MockOptical.deploy(ZERO_ADDRESS, deployer.address);
    await mockOptical.waitForDeployment();

    // Deploy mock with flags
    const MockOpticalWithFlags = await ethers.getContractFactory("MockOpticalWithFlags");
    mockOpticalWithFlags = await MockOpticalWithFlags.deploy(ZERO_ADDRESS, deployer.address, 0);
    await mockOpticalWithFlags.waitForDeployment();
  });

  describe("Default no-op hooks", function () {
    it("should return getFlags = 0 by default", async function () {
      expect(await mockOptical.getFlags()).to.equal(0);
    });

    it("should return proceed=true, amountDelta=0 from beforeSwap", async function () {
      const result = await mockOptical.beforeSwap.staticCall(
        deployer.address, alice.address, true, ethers.parseUnits("100", 6)
      );
      expect(result.proceed).to.be.true;
      expect(result.amountDelta).to.equal(0);
    });

    it("should return correct selector from afterSwap", async function () {
      const selector = await mockOptical.afterSwap.staticCall(
        deployer.address, alice.address, true, ethers.parseUnits("100", 6), ethers.parseUnits("50", 6)
      );
      const expectedSelector = mockOptical.interface.getFunction("afterSwap").selector;
      expect(selector).to.equal(expectedSelector);
    });

    it("should return unchanged feeAmount from beforeFeeDistribution", async function () {
      const feeAmount = ethers.parseUnits("10", 6);
      const adjusted = await mockOptical.beforeFeeDistribution.staticCall(deployer.address, feeAmount);
      expect(adjusted).to.equal(feeAmount);
    });

    it("should return correct selector from afterFeeDistribution", async function () {
      const selector = await mockOptical.afterFeeDistribution.staticCall(
        deployer.address, ethers.parseUnits("10", 6)
      );
      const expectedSelector = mockOptical.interface.getFunction("afterFeeDistribution").selector;
      expect(selector).to.equal(expectedSelector);
    });
  });

  describe("Immutable state", function () {
    it("should store poolRegistry and owner correctly", async function () {
      expect(await mockOptical.poolRegistry()).to.equal(ZERO_ADDRESS);
      expect(await mockOptical.owner()).to.equal(deployer.address);
    });
  });
});
