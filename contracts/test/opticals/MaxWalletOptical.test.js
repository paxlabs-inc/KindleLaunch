const { expect } = require("chai");
const { ethers } = require("hardhat");
const { ZERO_ADDRESS, HookFlags } = require("../helpers/constants");

describe("MaxWalletOptical", function () {
  let maxWallet, mockPool, mockToken;
  let deployer, alice, bob;

  const MAX_WALLET_BPS = 200n; // 2%
  const TOTAL_SUPPLY = ethers.parseUnits("1000000000", 6); // 1B

  beforeEach(async function () {
    [deployer, alice, bob] = await ethers.getSigners();

    // Deploy mock token
    const MockERC20 = await ethers.getContractFactory("MockERC20");
    mockToken = await MockERC20.deploy("TestToken", "TT", 18);
    await mockToken.waitForDeployment();

    // Mint total supply to deployer
    await mockToken.mint(deployer.address, TOTAL_SUPPLY);

    // Deploy mock pool
    const MockPool = await ethers.getContractFactory("MockPoolForOptical");
    mockPool = await MockPool.deploy();
    await mockPool.waitForDeployment();
    await mockPool.setTokenAddress(await mockToken.getAddress());

    // Deploy MaxWalletOptical
    const MaxWallet = await ethers.getContractFactory("MaxWalletOptical");
    maxWallet = await MaxWallet.deploy(ZERO_ADDRESS, deployer.address, MAX_WALLET_BPS);
    await maxWallet.waitForDeployment();
  });

  describe("Configuration", function () {
    it("should store immutable config correctly", async function () {
      expect(await maxWallet.maxWalletBps()).to.equal(MAX_WALLET_BPS);
      expect(await maxWallet.owner()).to.equal(deployer.address);
    });

    it("should return AFTER_SWAP flag only", async function () {
      const flags = await maxWallet.getFlags();
      expect(flags).to.equal(HookFlags.AFTER_SWAP);
    });
  });

  describe("Exemptions", function () {
    it("should allow owner to set exemptions", async function () {
      await maxWallet.setExempt(alice.address, true);
      expect(await maxWallet.exempt(alice.address)).to.be.true;
    });

    it("should revert non-owner setting exemptions", async function () {
      await expect(
        maxWallet.connect(alice).setExempt(bob.address, true)
      ).to.be.revertedWithCustomError(maxWallet, "NotOwner");
    });
  });

  describe("afterSwap enforcement", function () {
    it("should allow buy when wallet is within limit", async function () {
      // Alice has 1% of supply (within 2% max)
      const aliceAmount = TOTAL_SUPPLY * 1n / 100n; // 1%
      await mockToken.mint(alice.address, aliceAmount);

      const poolAddr = await mockPool.getAddress();
      // afterSwap should succeed (no revert)
      await maxWallet.afterSwap(
        poolAddr, alice.address, true, ethers.parseUnits("100", 6), ethers.parseUnits("1000", 6)
      );
    });

    it("should revert buy when wallet exceeds limit", async function () {
      // Alice has 3% of supply (exceeds 2% max)
      const aliceAmount = TOTAL_SUPPLY * 3n / 100n; // 3%
      await mockToken.mint(alice.address, aliceAmount);

      const poolAddr = await mockPool.getAddress();
      await expect(
        maxWallet.afterSwap(
          poolAddr, alice.address, true, ethers.parseUnits("100", 6), ethers.parseUnits("1000", 6)
        )
      ).to.be.revertedWithCustomError(maxWallet, "MaxWalletExceeded");
    });

    it("should not check sells", async function () {
      // Alice has 5% (way over limit) but selling — should pass
      const aliceAmount = TOTAL_SUPPLY * 5n / 100n;
      await mockToken.mint(alice.address, aliceAmount);

      const poolAddr = await mockPool.getAddress();
      // isBuy = false → no check
      await maxWallet.afterSwap(
        poolAddr, alice.address, false, ethers.parseUnits("1000", 6), ethers.parseUnits("100", 6)
      );
    });

    it("should skip check for exempt addresses", async function () {
      // Alice is exempt and holds 5%
      const aliceAmount = TOTAL_SUPPLY * 5n / 100n;
      await mockToken.mint(alice.address, aliceAmount);
      await maxWallet.setExempt(alice.address, true);

      const poolAddr = await mockPool.getAddress();
      // Should not revert despite exceeding limit
      await maxWallet.afterSwap(
        poolAddr, alice.address, true, ethers.parseUnits("100", 6), ethers.parseUnits("1000", 6)
      );
    });
  });
});
