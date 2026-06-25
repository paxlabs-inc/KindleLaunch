// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../base/Initializable.sol";
import "../base/UUPSUpgradeable.sol";
import "../base/AccessControl.sol";
import "../base/ReentrancyGuard.sol";
import "../interfaces/IFeesRouter.sol";
import "../interfaces/IFeeAccumulator.sol";
import "../interfaces/ISidioraNFT.sol";
import "../interfaces/ISidioraPool.sol";
import "../interfaces/IPoolRegistry.sol";

/// @title FeesRouter
/// @notice NFT-holder interface for fee management.
/// @dev UUPS proxy. Validates NFT ownership, reads strategy, delegates to FeeAccumulator.
contract FeesRouter is IFeesRouter, Initializable, UUPSUpgradeable, AccessControl, ReentrancyGuard {
    address public nftContract;
    address public feeAccumulator;
    address public poolRegistry;

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(
        address _nftContract,
        address _feeAccumulator,
        address _poolRegistry,
        address _admin
    ) external initializer {
        if (_admin == address(0)) revert ZeroAddress();
        if (_nftContract == address(0)) revert ZeroAddress();
        if (_feeAccumulator == address(0)) revert ZeroAddress();

        nftContract = _nftContract;
        feeAccumulator = _feeAccumulator;
        poolRegistry = _poolRegistry;

        _grantRole(DEFAULT_ADMIN_ROLE, _admin);
        _initReentrancyGuard();
    }

    /// @inheritdoc IFeesRouter
    function setFeeStrategy(uint256 nftId, uint8 newStrategy) external override {
        _requireNftOwner(nftId);
        ISidioraNFT(nftContract).setFeeStrategy(nftId, newStrategy);

        uint8 oldStrategy = ISidioraNFT(nftContract).getFeeStrategy(nftId);
        emit FeeStrategyChanged(nftId, oldStrategy, newStrategy);
    }

    /// @inheritdoc IFeesRouter
    function claimFees(uint256 nftId) external override nonReentrant returns (uint256 amount) {
        _requireNftOwner(nftId);
        _requireStrategy(nftId, 0); // CLAIM = 0

        address pool = ISidioraNFT(nftContract).getPoolAddress(nftId);
        amount = IFeeAccumulator(feeAccumulator).claim(pool, msg.sender);

        emit FeesClaimed(nftId, msg.sender, amount);
    }

    /// @inheritdoc IFeesRouter
    function executeBurn(uint256 nftId) external override nonReentrant returns (uint256 amount) {
        _requireNftOwner(nftId);
        _requireStrategy(nftId, 1); // BURN = 1

        address pool = ISidioraNFT(nftContract).getPoolAddress(nftId);
        amount = IFeeAccumulator(feeAccumulator).burn(pool);

        emit FeesBurned(nftId, amount);
    }

    /// @inheritdoc IFeesRouter
    function executeAirdrop(uint256 nftId) external override nonReentrant returns (uint256 amount) {
        _requireNftOwner(nftId);
        _requireStrategy(nftId, 2); // AIRDROP = 2

        address pool = ISidioraNFT(nftContract).getPoolAddress(nftId);
        amount = IFeeAccumulator(feeAccumulator).triggerAirdrop(pool);

        emit AirdropExecuted(nftId, amount);
    }

    /// @inheritdoc IFeesRouter
    function claimAirdrop(uint256 nftId) external override nonReentrant returns (uint256 amount) {
        // Any token holder can claim — no NFT ownership check
        address pool = ISidioraNFT(nftContract).getPoolAddress(nftId);
        amount = IFeeAccumulator(feeAccumulator).claimAirdrop(pool, msg.sender);

        emit AirdropClaimed(nftId, msg.sender, amount);
    }

    /// @inheritdoc IFeesRouter
    function executeLpRewards(uint256 nftId) external override nonReentrant returns (uint256 amount) {
        _requireNftOwner(nftId);
        _requireStrategy(nftId, 3); // LP_REWARDS = 3

        address pool = ISidioraNFT(nftContract).getPoolAddress(nftId);
        amount = IFeeAccumulator(feeAccumulator).sendLpRewards(pool);

        // Trigger syncReserves on pool so it picks up the new USDL
        ISidioraPool(pool).syncReserves();

        emit LpRewardsExecuted(nftId, amount);
    }

    // --- Internal ---

    function _requireNftOwner(uint256 nftId) internal view {
        (bool success, bytes memory data) = nftContract.staticcall(
            abi.encodeWithSignature("ownerOf(uint256)", nftId)
        );
        if (!success || data.length < 32) revert NotNftOwner();
        address owner = abi.decode(data, (address));
        if (owner != msg.sender) revert NotNftOwner();
    }

    function _requireStrategy(uint256 nftId, uint8 expectedStrategy) internal view {
        uint8 currentStrategy = ISidioraNFT(nftContract).getFeeStrategy(nftId);
        if (currentStrategy != expectedStrategy) revert WrongStrategy();
    }

    function _authorizeUpgrade(address) internal override onlyRole(DEFAULT_ADMIN_ROLE) {}
}
