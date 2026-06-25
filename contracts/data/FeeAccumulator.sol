// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../base/Initializable.sol";
import "../base/UUPSUpgradeable.sol";
import "../base/AccessControl.sol";
import "../base/ReentrancyGuard.sol";
import "../interfaces/IFeeAccumulator.sol";
import "../interfaces/IProtocolConfig.sol";
import "../interfaces/IPoolRegistry.sol";
import "../interfaces/ITreasury.sol";
import "../interfaces/IEventEmitter.sol";
import "../libraries/TransferHelper.sol";
import "../libraries/BitFlag.sol";

/// @dev Minimal interface for optical hooks used by FeeAccumulator
interface IOpticalFee {
    function beforeFeeDistribution(address pool, uint256 feeAmount) external returns (uint256 adjustedFee);
    function afterFeeDistribution(address pool, uint256 feeAmount) external returns (bytes4);
    function getFlags() external view returns (uint8);
}

/// @title FeeAccumulator
/// @notice Tracks accumulated fees per pool and executes fee distribution strategies
/// @dev UUPS proxy. Inspired by Aerodrome's PoolFees separation.
contract FeeAccumulator is IFeeAccumulator, Initializable, UUPSUpgradeable, AccessControl, ReentrancyGuard {
    bytes32 public constant POOL_ROLE = keccak256("POOL_ROLE");
    bytes32 public constant FEES_ROUTER_ROLE = keccak256("FEES_ROUTER_ROLE");
    bytes32 public constant FACTORY_ROLE = keccak256("FACTORY_ROLE");
    bytes32 public constant OPTICAL_CLAIM_ROLE = keccak256("OPTICAL_CLAIM_ROLE");
    bytes32 public constant OPTICAL_GRANTER_ROLE = keccak256("OPTICAL_GRANTER_ROLE");

    /// @notice Authorize a pool to record fees. Called by Factory on market creation.
    function authorizePool(address pool) external onlyRole(FACTORY_ROLE) {
        _grantRole(POOL_ROLE, pool);
    }

    address public constant DEAD_ADDRESS = 0x000000000000000000000000000000000000dEaD;

    address public protocolConfig;
    address public treasury;
    address public poolRegistry;
    address public eventEmitter;
    address public usdlAddress;

    mapping(address => uint256) private _accumulatedFees;
    uint256 private _protocolFeesPending;

    // Airdrop state
    mapping(address => uint256) private _airdropBalance;
    mapping(address => uint256) private _airdropEpoch;
    mapping(address => mapping(uint256 => uint256)) private _airdropEpochAmount;
    mapping(address => mapping(address => mapping(uint256 => bool))) private _airdropClaimed;

    // LP rewards state
    mapping(address => uint256) private _lpRewardsBalance;

    // Optical surplus state (held on behalf of opticals that divert fees)
    mapping(address => uint256) private _opticalSurplus;

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(
        address _protocolConfig,
        address _treasury,
        address _poolRegistry,
        address _eventEmitter,
        address _usdlAddress,
        address _admin
    ) external initializer {
        if (_admin == address(0)) revert ZeroAddress();
        if (_usdlAddress == address(0)) revert ZeroAddress();
        if (_treasury == address(0)) revert ZeroAddress();

        protocolConfig = _protocolConfig;
        treasury = _treasury;
        poolRegistry = _poolRegistry;
        eventEmitter = _eventEmitter;
        usdlAddress = _usdlAddress;

        _grantRole(DEFAULT_ADMIN_ROLE, _admin);
        _initReentrancyGuard();
    }

    /// @inheritdoc IFeeAccumulator
    function recordFee(address pool, uint256 feeAmount) external override onlyRole(POOL_ROLE) {
        if (feeAmount == 0) revert ZeroAmount();

        // Split fee into protocol and pool portions
        uint256 protocolFeeBps = IProtocolConfig(protocolConfig).protocolFeeBps();
        uint256 protocolCut = (feeAmount * protocolFeeBps) / 10000;
        uint256 poolCut = feeAmount - protocolCut;

        // Accumulate protocol fees
        _protocolFeesPending += protocolCut;

        // Call optical beforeFeeDistribution hook if applicable
        uint256 opticalDivert = 0;
        if (poolRegistry != address(0)) {
            IPoolRegistry.PoolMetadata memory meta = IPoolRegistry(poolRegistry).getPoolMetadata(pool);
            if (meta.optical != address(0)) {
                uint8 flags = IOpticalFee(meta.optical).getFlags();
                if (BitFlag.hasFlag(flags, BitFlag.BEFORE_FEE_DISTRIBUTION)) {
                    uint256 adjustedPoolCut = IOpticalFee(meta.optical).beforeFeeDistribution(pool, poolCut);
                    if (adjustedPoolCut < poolCut) {
                        opticalDivert = poolCut - adjustedPoolCut;
                        _opticalSurplus[pool] += opticalDivert;
                        poolCut = adjustedPoolCut;
                        emit OpticalSurplusRecorded(pool, opticalDivert);
                    }
                }
            }
        }

        // Accumulate pool fees (after optical diversion)
        _accumulatedFees[pool] += poolCut;

        // Transfer protocol cut to treasury
        if (protocolCut > 0) {
            TransferHelper.safeApprove(usdlAddress, treasury, protocolCut);
            ITreasury(treasury).deposit(usdlAddress, protocolCut);
        }

        emit FeeRecorded(pool, feeAmount, protocolCut, poolCut);
    }

    /// @inheritdoc IFeeAccumulator
    function claim(
        address pool,
        address recipient
    ) external override onlyRole(FEES_ROUTER_ROLE) nonReentrant returns (uint256 amount) {
        if (recipient == address(0)) revert ZeroAddress();
        amount = _accumulatedFees[pool];
        if (amount == 0) revert NoFeesAccumulated();

        _accumulatedFees[pool] = 0;
        TransferHelper.safeTransfer(usdlAddress, recipient, amount);

        emit FeesClaimed(pool, recipient, amount);
    }

    /// @inheritdoc IFeeAccumulator
    function burn(
        address pool
    ) external override onlyRole(FEES_ROUTER_ROLE) nonReentrant returns (uint256 amount) {
        amount = _accumulatedFees[pool];
        if (amount == 0) revert NoFeesAccumulated();

        _accumulatedFees[pool] = 0;
        TransferHelper.safeTransfer(usdlAddress, DEAD_ADDRESS, amount);

        emit FeesBurned(pool, amount);
    }

    /// @inheritdoc IFeeAccumulator
    function triggerAirdrop(
        address pool
    ) external override onlyRole(FEES_ROUTER_ROLE) nonReentrant returns (uint256 totalAmount) {
        totalAmount = _accumulatedFees[pool];
        if (totalAmount == 0) revert NoFeesAccumulated();

        _accumulatedFees[pool] = 0;

        // Increment epoch and store snapshot
        uint256 newEpoch = _airdropEpoch[pool] + 1;
        _airdropEpoch[pool] = newEpoch;
        _airdropEpochAmount[pool][newEpoch] = totalAmount;
        _airdropBalance[pool] += totalAmount;

        emit AirdropTriggered(pool, totalAmount, newEpoch);
    }

    /// @inheritdoc IFeeAccumulator
    function claimAirdrop(
        address pool,
        address holder
    ) external override nonReentrant returns (uint256 amount) {
        uint256 currentEpoch = _airdropEpoch[pool];
        if (currentEpoch == 0) revert AirdropNotTriggered();
        if (_airdropClaimed[pool][holder][currentEpoch]) revert AlreadyClaimed();

        uint256 epochAmount = _airdropEpochAmount[pool][currentEpoch];
        if (epochAmount == 0) revert NoFeesAccumulated();

        // Get token address from pool registry
        IPoolRegistry.PoolMetadata memory meta = IPoolRegistry(poolRegistry).getPoolMetadata(pool);
        address tokenAddress = meta.token;

        // Calculate proportional share: holderBalance / totalSupply * epochAmount
        uint256 holderBalance = _getTokenBalance(tokenAddress, holder);
        uint256 totalSupply = _getTokenTotalSupply(tokenAddress);

        if (holderBalance == 0 || totalSupply == 0) revert ZeroAmount();

        amount = (epochAmount * holderBalance) / totalSupply;
        if (amount == 0) revert ZeroAmount();

        _airdropClaimed[pool][holder][currentEpoch] = true;
        _airdropBalance[pool] -= amount;

        TransferHelper.safeTransfer(usdlAddress, holder, amount);

        emit AirdropClaimed(pool, holder, amount, currentEpoch);
    }

    /// @inheritdoc IFeeAccumulator
    function sendLpRewards(
        address pool
    ) external override onlyRole(FEES_ROUTER_ROLE) nonReentrant returns (uint256 amount) {
        amount = _accumulatedFees[pool];
        if (amount == 0) revert NoFeesAccumulated();

        _accumulatedFees[pool] = 0;
        TransferHelper.safeTransfer(usdlAddress, pool, amount);

        emit LpRewardsSent(pool, amount);
    }

    // --- Views ---

    /// @inheritdoc IFeeAccumulator
    function getAccumulatedFees(address pool) external view override returns (uint256) {
        return _accumulatedFees[pool];
    }

    /// @inheritdoc IFeeAccumulator
    function getProtocolFeesPending() external view override returns (uint256) {
        return _protocolFeesPending;
    }

    /// @inheritdoc IFeeAccumulator
    function getAirdropBalance(address pool) external view override returns (uint256) {
        return _airdropBalance[pool];
    }

    /// @inheritdoc IFeeAccumulator
    function getLpRewardsBalance(address pool) external view override returns (uint256) {
        return _lpRewardsBalance[pool];
    }

    /// @inheritdoc IFeeAccumulator
    function getAirdropEpoch(address pool) external view override returns (uint256) {
        return _airdropEpoch[pool];
    }

    /// @inheritdoc IFeeAccumulator
    function hasClaimedAirdrop(
        address pool,
        address holder,
        uint256 epoch
    ) external view override returns (bool) {
        return _airdropClaimed[pool][holder][epoch];
    }

    // --- Internal ---

    function _getTokenBalance(address token, address account) internal view returns (uint256) {
        (bool success, bytes memory data) = token.staticcall(
            abi.encodeWithSignature("balanceOf(address)", account)
        );
        if (!success || data.length < 32) return 0;
        return abi.decode(data, (uint256));
    }

    function _getTokenTotalSupply(address token) internal view returns (uint256) {
        (bool success, bytes memory data) = token.staticcall(
            abi.encodeWithSignature("totalSupply()")
        );
        if (!success || data.length < 32) return 0;
        return abi.decode(data, (uint256));
    }

    /// @notice Authorize an optical contract to claim surplus. Callable by OPTICAL_GRANTER_ROLE (e.g. LaunchpadOpticalFactory).
    function authorizeOptical(address optical) external onlyRole(OPTICAL_GRANTER_ROLE) {
        _grantRole(OPTICAL_CLAIM_ROLE, optical);
    }

    // --- Optical Surplus ---

    /// @inheritdoc IFeeAccumulator
    function recordOpticalSurplus(address pool, uint256 amount) external override onlyRole(POOL_ROLE) {
        if (amount == 0) return;
        _opticalSurplus[pool] += amount;
        emit OpticalSurplusRecorded(pool, amount);
    }

    /// @inheritdoc IFeeAccumulator
    function claimOpticalSurplus(
        address pool,
        uint256 amount,
        address recipient
    ) external override onlyRole(OPTICAL_CLAIM_ROLE) nonReentrant {
        if (recipient == address(0)) revert ZeroAddress();
        if (amount == 0) revert ZeroAmount();
        if (_opticalSurplus[pool] < amount) revert InsufficientSurplus();

        _opticalSurplus[pool] -= amount;
        TransferHelper.safeTransfer(usdlAddress, recipient, amount);

        emit OpticalSurplusClaimed(pool, recipient, amount);
    }

    /// @inheritdoc IFeeAccumulator
    function getOpticalSurplus(address pool) external view override returns (uint256) {
        return _opticalSurplus[pool];
    }

    function _authorizeUpgrade(address) internal override onlyRole(DEFAULT_ADMIN_ROLE) {}
}
