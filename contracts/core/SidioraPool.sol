// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../base/Initializable.sol";
import "../base/ReentrancyGuard.sol";
import "../base/Pausable.sol";
import "../interfaces/ISidioraPool.sol";
import "../interfaces/IProtocolConfig.sol";
import "../interfaces/IFeeAccumulator.sol";
import "../interfaces/IEventEmitter.sol";
import "../libraries/ReserveLib.sol";
import "../libraries/FeeLib.sol";
import "../libraries/BitFlag.sol";
import "../libraries/TransferHelper.sol";

/// @title IOpticalMinimal
/// @dev Minimal interface for optical hooks used by pool
interface IOpticalMinimal {
    function beforeSwap(address pool, address sender, bool isBuy, uint256 amountIn)
        external returns (bool proceed, int256 amountDelta);
    function afterSwap(address pool, address sender, bool isBuy, uint256 amountIn, uint256 amountOut)
        external returns (bytes4);
    function getFlags() external view returns (uint8);
}

/// @title SidioraPool
/// @notice Core AMM engine. Constant product with virtual reserves and dynamic fees.
/// @dev Beacon proxy instances. Deliberately focused on AMM math + state transitions only.
///
///      FEE MODEL: Fees are always paid in the INPUT token.
///        BUY  (USDL → Token): fee in USDL → sent to FeeAccumulator for strategy distribution
///        SELL (Token → USDL): fee in Token → stays in pool, deepens token-side liquidity
///
///      VIRTUAL RESERVE FLOOR: virtualUsdlReserve (10,000 USDL) is pricing-only.
///        realUsdlBalance can never go below 0. The pool cannot pay out virtual USDL.
///        On sells, amountOut is bounded by realUsdlBalance (defense-in-depth).
contract SidioraPool is ISidioraPool, Initializable, ReentrancyGuard, Pausable {
    error VirtualFloorBreached();

    address public override tokenAddress;
    address public usdlAddress;
    address public override opticalAddress;
    address public feeAccumulator;
    address public eventEmitter;
    address public protocolConfig;
    address public guardian;

    uint256 public virtualUsdlReserve;
    uint256 public realUsdlBalance;
    uint256 public tokenReserve;
    uint256 public override creationTimestamp;
    uint256 public override cumulativeVolume;

    uint256[8] public priceSnapshots;
    uint256 public snapshotIndex;
    uint256 public snapshotCount;

    uint256 public accumulatedUsdlFees;
    uint256 public accumulatedTokenFees;

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(
        address _tokenAddress,
        address _usdlAddress,
        address _opticalAddress,
        address _feeAccumulator,
        address _eventEmitter,
        address _protocolConfig,
        address _guardian,
        uint256 _virtualUsdlReserve,
        uint256 _tokenReserve
    ) external override initializer {
        if (_tokenAddress == address(0)) revert ZeroAddress();
        if (_usdlAddress == address(0)) revert ZeroAddress();

        tokenAddress = _tokenAddress;
        usdlAddress = _usdlAddress;
        opticalAddress = _opticalAddress;
        feeAccumulator = _feeAccumulator;
        eventEmitter = _eventEmitter;
        protocolConfig = _protocolConfig;
        guardian = _guardian;

        virtualUsdlReserve = _virtualUsdlReserve;
        tokenReserve = _tokenReserve;
        realUsdlBalance = 0;
        creationTimestamp = block.timestamp;

        _initReentrancyGuard();
    }

    // ============ SWAP ============

    function swap(
        uint256 amountIn,
        uint256 minAmountOut,
        bool isBuy,
        address recipient,
        uint256 deadline
    ) external override nonReentrant whenNotPaused returns (uint256 amountOut) {
        if (block.timestamp > deadline) revert DeadlineExpired();
        if (amountIn == 0) revert InsufficientInput();
        if (recipient == address(0)) revert ZeroAddress();

        // Calculate dynamic fee
        uint256 feeBps = _calculateFee();

        // Optical: beforeSwap
        if (opticalAddress != address(0)) {
            uint8 flags = IOpticalMinimal(opticalAddress).getFlags();
            if (BitFlag.hasFlag(flags, BitFlag.BEFORE_SWAP)) {
                (bool proceed, int256 amountDelta) =
                    IOpticalMinimal(opticalAddress).beforeSwap(address(this), recipient, isBuy, amountIn);
                if (!proceed) revert InsufficientInput();
                if (amountDelta > 0) {
                    amountIn += uint256(amountDelta);
                } else if (amountDelta < 0) {
                    uint256 reduction = uint256(-amountDelta);
                    if (reduction >= amountIn) revert InsufficientInput();
                    amountIn -= reduction;
                }
            }
        }

        // Calculate fee in the INPUT token
        uint256 feeAmount = (amountIn * feeBps) / 10000;
        uint256 amountInAfterFee = amountIn - feeAmount;

        uint256 effectiveUsdl = virtualUsdlReserve + realUsdlBalance;

        if (isBuy) {
            // ── BUY: USDL → Token ──
            // Fee is in USDL. Only amountInAfterFee enters reserves.
            // feeAmount USDL is sent to FeeAccumulator for strategy distribution.
            amountOut = ReserveLib.getAmountOut(effectiveUsdl, tokenReserve, amountInAfterFee);
            if (amountOut > tokenReserve) revert InsufficientLiquidity();

            realUsdlBalance += amountInAfterFee;
            tokenReserve -= amountOut;

            // Transfer tokens to buyer
            TransferHelper.safeTransfer(tokenAddress, recipient, amountOut);

            // Send USDL fee to FeeAccumulator
            if (feeAmount > 0 && feeAccumulator != address(0)) {
                accumulatedUsdlFees += feeAmount;
                TransferHelper.safeTransfer(usdlAddress, feeAccumulator, feeAmount);
                IFeeAccumulator(feeAccumulator).recordFee(address(this), feeAmount);
            }
        } else {
            // ── SELL: Token → USDL ──
            // Fee is in Token. Fee tokens stay in pool (deepen token-side liquidity).
            // Only amountInAfterFee enters AMM math for pricing.
            amountOut = ReserveLib.getAmountOut(tokenReserve, effectiveUsdl, amountInAfterFee);

            // VIRTUAL FLOOR: Cannot pay out virtual USDL. Only real USDL is withdrawable.
            if (amountOut > realUsdlBalance) revert VirtualFloorBreached();

            // Full amountIn added to tokenReserve (fee tokens deepen liquidity)
            tokenReserve += amountIn;
            realUsdlBalance -= amountOut;

            // Transfer USDL to seller
            TransferHelper.safeTransfer(usdlAddress, recipient, amountOut);

            // Track token fees (they stay in pool, no external transfer)
            if (feeAmount > 0) {
                accumulatedTokenFees += feeAmount;
            }
        }

        if (amountOut < minAmountOut) revert SlippageExceeded();

        // Update volume and price snapshots
        cumulativeVolume += amountIn;
        _updatePriceSnapshot();

        // Optical: afterSwap
        if (opticalAddress != address(0)) {
            uint8 flags = IOpticalMinimal(opticalAddress).getFlags();
            if (BitFlag.hasFlag(flags, BitFlag.AFTER_SWAP)) {
                IOpticalMinimal(opticalAddress).afterSwap(address(this), recipient, isBuy, amountIn, amountOut);
            }
        }

        // Emit event
        if (eventEmitter != address(0)) {
            uint256 currentPrice = _currentPrice();
            bytes32 poolId = bytes32(uint256(uint160(address(this))));
            IEventEmitter(eventEmitter).emitSwap(poolId, msg.sender, isBuy, amountIn, amountOut, feeAmount, currentPrice);
        }
    }

    // ============ SYNC RESERVES ============

    function syncReserves() external override returns (uint256 usdlBal, uint256 tokenBal) {
        usdlBal = _getBalance(usdlAddress);
        tokenBal = _getBalance(tokenAddress);

        realUsdlBalance = usdlBal;
        tokenReserve = tokenBal;

        if (eventEmitter != address(0)) {
            bytes32 poolId = bytes32(uint256(uint160(address(this))));
            IEventEmitter(eventEmitter).emitPoolStateUpdated(
                poolId, virtualUsdlReserve, realUsdlBalance, tokenReserve, _currentPrice()
            );
        }
    }

    // ============ PAUSE ============

    function pause() external {
        if (msg.sender != guardian) revert ZeroAddress();
        _pause();
    }

    function unpause() external {
        if (msg.sender != guardian) revert ZeroAddress();
        _unpause();
    }

    function paused() external view returns (bool) {
        return _paused();
    }

    // ============ VIEWS ============

    function getReserves() external view override returns (
        uint256 virtualUsdl,
        uint256 realUsdl,
        uint256 tokenRes
    ) {
        virtualUsdl = virtualUsdlReserve;
        realUsdl = realUsdlBalance;
        tokenRes = tokenReserve;
    }

    function getEffectiveReserves() external view override returns (
        uint256 effectiveUsdl,
        uint256 tokenRes
    ) {
        effectiveUsdl = virtualUsdlReserve + realUsdlBalance;
        tokenRes = tokenReserve;
    }

    function getPrice() external view override returns (uint256) {
        return _currentPrice();
    }

    function getPoolInfo() external view override returns (PoolInfo memory) {
        return PoolInfo({
            tokenAddress: tokenAddress,
            opticalAddress: opticalAddress,
            virtualUsdlReserve: virtualUsdlReserve,
            realUsdlBalance: realUsdlBalance,
            tokenReserve: tokenReserve,
            creationTimestamp: creationTimestamp,
            cumulativeVolume: cumulativeVolume
        });
    }

    function getPriceSnapshots() external view override returns (uint256[8] memory) {
        return priceSnapshots;
    }

    // ============ INTERNAL ============

    function _calculateFee() internal view returns (uint256) {
        IProtocolConfig config = IProtocolConfig(protocolConfig);
        uint256 poolAge = block.timestamp - creationTimestamp;

        // Calculate volatility from snapshots
        uint256 volatility = FeeLib.calculateVolatility(priceSnapshots, snapshotCount);

        // Concentration: 0 for now (calculated off-chain or by Quoter in production)
        uint256 topHolderBps = 0;

        return FeeLib.calculateDynamicFee(
            config.baseFeeBps(),
            config.minFeeBps(),
            config.maxFeeBps(),
            config.feeDecayRate(),
            config.volatilityWeight(),
            config.concentrationWeight(),
            poolAge,
            volatility,
            topHolderBps
        );
    }

    function _currentPrice() internal view returns (uint256) {
        if (tokenReserve == 0) return 0;
        uint256 effectiveUsdl = virtualUsdlReserve + realUsdlBalance;
        return ReserveLib.getPrice(effectiveUsdl, tokenReserve);
    }

    function _updatePriceSnapshot() internal {
        uint256 price = _currentPrice();
        priceSnapshots[snapshotIndex] = price;
        snapshotIndex = (snapshotIndex + 1) % 8;
        if (snapshotCount < 8) {
            snapshotCount++;
        }
    }

    function _getBalance(address token) internal view returns (uint256) {
        (bool success, bytes memory data) = token.staticcall(
            abi.encodeWithSignature("balanceOf(address)", address(this))
        );
        if (!success || data.length < 32) return 0;
        return abi.decode(data, (uint256));
    }
}
