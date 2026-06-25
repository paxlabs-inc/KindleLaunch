const { expect } = require("chai");
const { ethers } = require("hardhat");
const { ZERO_ADDRESS, HookFlags } = require("../helpers/constants");

describe("CooldownOptical", function () {
  let cooldown;
  let deployer, alice, bob;
  let poolAddr;

  const COOLDOWN_SECONDS = 60n; // 60 seconds

  beforeEach(async function () {
    [deployer, alice, bob] = await ethers.getSigners();

    const MockPool = await ethers.getContractFactory("MockPoolForOptical");
    const mockPool = await MockPool.deploy();
    await mockPool.waitForDeployment();
    poolAddr = await mockPool.getAddress();

    const CooldownOptical = await ethers.getContractFactory("CooldownOptical");
    cooldown = await CooldownOptical.deploy(ZERO_ADDRESS, deployer.address, COOLDOWN_SECONDS);
    await cooldown.waitForDeployment();
  });

  describe("Configuration", function () {
    it("should store immutable config correctly", async function () {
      expect(await cooldown.cooldownSeconds()).to.equal(COOLDOWN_SECONDS);
    });

    it("should return BEFORE_SWAP flag only", async function () {
      expect(await cooldown.getFlags()).to.equal(HookFlags.BEFORE_SWAP);
    });
  });

  describe("Cooldown enforcement", function () {
    it("should allow first trade for any wallet", async function () {
      const result = await cooldown.beforeSwap.staticCall(
        poolAddr, alice.address, true, ethers.parseUnits("100", 6)
      );
      expect(result.proceed).to.be.true;
      expect(result.amountDelta).to.equal(0);
    });

    it("should block trade within cooldown period", async function () {
      // First trade (mutating call to record timestamp)
      await cooldown.beforeSwap(poolAddr, alice.address, true, ethers.parseUnits("100", 6));

      // Second trade immediately — should be rejected
      const result = await cooldown.beforeSwap.staticCall(
        poolAddr, alice.address, true, ethers.parseUnits("100", 6)
      );
      expect(result.proceed).to.be.false;
    });

    it("should allow trade after cooldown expires", async function () {
      // First trade
      await cooldown.beforeSwap(poolAddr, alice.address, true, ethers.parseUnits("100", 6));

      // Advance time past cooldown
      await ethers.provider.send("evm_increaseTime", [61]);
      await ethers.provider.send("evm_mine", []);

      // Second trade — should be allowed
      const result = await cooldown.beforeSwap.staticCall(
        poolAddr, alice.address, false, ethers.parseUnits("50", 6)
      );
      expect(result.proceed).to.be.true;
    });

    it("should track cooldown independently per wallet", async function () {
      // Alice trades
      await cooldown.beforeSwap(poolAddr, alice.address, true, ethers.parseUnits("100", 6));

      // Bob should still be able to trade (independent cooldown)
      const result = await cooldown.beforeSwap.staticCall(
        poolAddr, bob.address, true, ethers.parseUnits("100", 6)
      );
      expect(result.proceed).to.be.true;
    });

    it("should apply cooldown to both buys and sells", async function () {
      // Buy
      await cooldown.beforeSwap(poolAddr, alice.address, true, ethers.parseUnits("100", 6));

      // Immediate sell — should be blocked
      const result = await cooldown.beforeSwap.staticCall(
        poolAddr, alice.address, false, ethers.parseUnits("50", 6)
      );
      expect(result.proceed).to.be.false;
    });

    it("should track cooldown independently per pool", async function () {
      // Trade on pool 1
      await cooldown.beforeSwap(poolAddr, alice.address, true, ethers.parseUnits("100", 6));

      // Trade on different pool — should be allowed
      const otherPool = bob.address; // use as fake pool address
      const result = await cooldown.beforeSwap.staticCall(
        otherPool, alice.address, true, ethers.parseUnits("100", 6)
      );
      expect(result.proceed).to.be.true;
    });
  });
});
