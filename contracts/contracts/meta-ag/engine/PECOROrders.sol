// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.27;

import {IPECOROrders} from "../interfaces/IPECOROrders.sol";
import {IPECORVault} from "../interfaces/IPECORVault.sol";
import {IPriceOracle} from "../interfaces/IPriceOracle.sol";
import {ITransactionTracker} from "../interfaces/ITransactionTracker.sol";
import {Initializable} from "../../base/Initializable.sol";
import {UUPSUpgradeable} from "../../base/UUPSUpgradeable.sol";
import {AccessControl} from "../../base/AccessControl.sol";
import {ReentrancyGuard} from "../../base/ReentrancyGuard.sol";
import {Pausable} from "../../base/Pausable.sol";
import {SidioraMath} from "../../libraries/SidioraMath.sol";

/// @title PECOROrders — Limit / Stop-Loss / Stop-Limit order engine
/// @notice UUPS-upgradeable order engine that escrows user funds in
///         PECORVault v2 at place-time and executes atomically at match-time
///         via keeper-triggered pushes. Split from PECOR.sol for contract
///         size and surface-area separation (spec §7.7).
/// @dev Interface: `contracts/meta-ag/interfaces/IPECOROrders.sol`.
///      Vault must grant OPERATOR_ROLE to this contract before placement.
///
/// Inheritance (spec §7.7):
///   IPECOROrders, Initializable, UUPSUpgradeable, AccessControl,
///   ReentrancyGuard, Pausable
///
/// Roles:
///   - DEFAULT_ADMIN_ROLE → Timelock at deploy (invariant S1)
///   - KEEPER_ROLE        → authorized keepers (rotated via {setKeeper})
///
/// Storage layout (append-only per S12):
///   slot 0:  AccessControl._roles             (mapping)
///   slot 1:  priceOracle                      (address)
///   slot 2:  vault                            (address)
///   slot 3:  transactionTracker               (address)
///   slot 4:  nextOrderId                      (uint256)
///   slot 5:  limitOrders                      (mapping)
///   slot 6:  stopLimitOrders                  (mapping)
///   slot 7:  userLimitOrders                  (mapping)
///   slot 8:  userStopLimitOrders              (mapping)
///   slot 9:  activeLimitOrderIds              (uint256[])
///   slot 10: activeStopLimitOrderIds          (uint256[])
///   slot 11: _limitOrderActiveIndex           (mapping, 1-indexed)
///   slot 12: _stopLimitOrderActiveIndex       (mapping, 1-indexed)
///   slot 13: keepers                          (mapping — mirror of KEEPER_ROLE)
///   slot 14..63: __gap[50]
contract PECOROrders is
    IPECOROrders,
    Initializable,
    UUPSUpgradeable,
    AccessControl,
    ReentrancyGuard,
    Pausable
{

    bytes32 public constant KEEPER_ROLE = keccak256("KEEPER_ROLE");

    uint256 public constant BPS_DENOMINATOR = 10_000;

    error ZeroAddress();
    error ZeroAmount();
    error InvalidPrice();
    error InvalidExpiry();
    error InvalidPriceRange();
    error NotAStablecoin();
    error TokenIsStablecoin();
    error NotOrderOwner();
    error OrderNotPending();
    error OrderNotActivated();
    error OrderExpired();
    error OrderCannotCancel();
    error PriceNotMet();
    error InsufficientLiquidity();

    IPriceOracle public priceOracle;
    IPECORVault public vault;
    ITransactionTracker public transactionTracker;

    /// @notice Monotonically increasing order id. Starts at 1 so that `0`
    ///         reliably means "not a valid order id".
    uint256 public nextOrderId;

    mapping(uint256 => LimitOrder) public limitOrders;
    mapping(uint256 => StopLimitOrder) public stopLimitOrders;
    mapping(address => uint256[]) public userLimitOrders;
    mapping(address => uint256[]) public userStopLimitOrders;

    uint256[] public activeLimitOrderIds;
    uint256[] public activeStopLimitOrderIds;

    /// @dev 1-indexed active-list positions; 0 means "not in active list".
    mapping(uint256 => uint256) private _limitOrderActiveIndex;
    mapping(uint256 => uint256) private _stopLimitOrderActiveIndex;

    /// @notice Mirror of KEEPER_ROLE membership for O(1) view.
    mapping(address => bool) public keepers;

    /// @dev Reserved storage for future upgrades (S12: append-only, 50 * 32 bytes).
    uint256[50] private __gap;

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    /// @inheritdoc IPECOROrders
    function initialize(
        address priceOracle_,
        address vault_,
        address tracker_,
        address admin_
    ) external override initializer {
        if (priceOracle_ == address(0)) revert ZeroAddress();
        if (vault_ == address(0)) revert ZeroAddress();
        if (admin_ == address(0)) revert ZeroAddress();

        _initReentrancyGuard();
        priceOracle = IPriceOracle(priceOracle_);
        vault = IPECORVault(vault_);
        transactionTracker = ITransactionTracker(tracker_);
        nextOrderId = 1;

        _grantRole(DEFAULT_ADMIN_ROLE, admin_);
    }

    function _authorizeUpgrade(address) internal override onlyRole(DEFAULT_ADMIN_ROLE) {}

    /// @inheritdoc IPECOROrders
    /// @dev Rotates KEEPER_ROLE and mirrors the flag in `keepers` for O(1) reads.
    function setKeeper(
        address keeper,
        bool authorized
    ) external override onlyRole(DEFAULT_ADMIN_ROLE) {
        if (keeper == address(0)) revert ZeroAddress();
        keepers[keeper] = authorized;
        if (authorized) {
            _grantRole(KEEPER_ROLE, keeper);
        } else {
            _revokeRole(KEEPER_ROLE, keeper);
        }
        emit KeeperUpdated(keeper, authorized);
    }

    /// @inheritdoc IPECOROrders
    function pause() external override onlyRole(DEFAULT_ADMIN_ROLE) {
        _pause();
    }

    /// @inheritdoc IPECOROrders
    function unpause() external override onlyRole(DEFAULT_ADMIN_ROLE) {
        _unpause();
    }

    /// @inheritdoc IPECOROrders
    function placeLimitBuy(
        address stablecoin,
        address token,
        uint256 stablecoinAmount,
        uint256 targetPrice,
        uint256 expiresAt
    ) external override nonReentrant whenNotPaused returns (uint256 orderId) {
        _validatePair(stablecoin, token);
        if (stablecoinAmount == 0) revert ZeroAmount();
        if (targetPrice == 0) revert InvalidPrice();
        _validateExpiry(expiresAt);

        vault.pullTokens(stablecoin, msg.sender, stablecoinAmount);

        orderId = nextOrderId++;
        limitOrders[orderId] = LimitOrder({
            id: orderId,
            user: msg.sender,
            stablecoin: stablecoin,
            token: token,
            amount: stablecoinAmount,
            targetPrice: targetPrice,
            orderType: OrderType.LIMIT_BUY,
            status: OrderStatus.PENDING,
            createdAt: block.timestamp,
            expiresAt: expiresAt
        });
        userLimitOrders[msg.sender].push(orderId);
        _addToActiveLimitOrders(orderId);

        emit LimitOrderCreated(orderId, msg.sender, OrderType.LIMIT_BUY, stablecoinAmount, targetPrice);
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordLimitOrderPlaced(
                orderId,
                msg.sender,
                token,
                stablecoin,
                stablecoinAmount,
                targetPrice,
                true
            );
        }
    }

    /// @inheritdoc IPECOROrders
    function placeLimitSell(
        address token,
        address stablecoin,
        uint256 tokenAmount,
        uint256 targetPrice,
        uint256 expiresAt
    ) external override nonReentrant whenNotPaused returns (uint256 orderId) {
        _validatePair(stablecoin, token);
        if (tokenAmount == 0) revert ZeroAmount();
        if (targetPrice == 0) revert InvalidPrice();
        _validateExpiry(expiresAt);

        vault.pullTokens(token, msg.sender, tokenAmount);

        orderId = nextOrderId++;
        limitOrders[orderId] = LimitOrder({
            id: orderId,
            user: msg.sender,
            stablecoin: stablecoin,
            token: token,
            amount: tokenAmount,
            targetPrice: targetPrice,
            orderType: OrderType.LIMIT_SELL,
            status: OrderStatus.PENDING,
            createdAt: block.timestamp,
            expiresAt: expiresAt
        });
        userLimitOrders[msg.sender].push(orderId);
        _addToActiveLimitOrders(orderId);

        emit LimitOrderCreated(orderId, msg.sender, OrderType.LIMIT_SELL, tokenAmount, targetPrice);
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordLimitOrderPlaced(
                orderId,
                msg.sender,
                token,
                stablecoin,
                tokenAmount,
                targetPrice,
                false
            );
        }
    }

    /// @inheritdoc IPECOROrders
    function placeStopLoss(
        address token,
        address stablecoin,
        uint256 tokenAmount,
        uint256 triggerPrice,
        uint256 expiresAt
    ) external override nonReentrant whenNotPaused returns (uint256 orderId) {
        _validatePair(stablecoin, token);
        if (tokenAmount == 0) revert ZeroAmount();
        if (triggerPrice == 0) revert InvalidPrice();
        _validateExpiry(expiresAt);

        vault.pullTokens(token, msg.sender, tokenAmount);

        orderId = nextOrderId++;
        limitOrders[orderId] = LimitOrder({
            id: orderId,
            user: msg.sender,
            stablecoin: stablecoin,
            token: token,
            amount: tokenAmount,
            targetPrice: triggerPrice,
            orderType: OrderType.STOP_LOSS,
            status: OrderStatus.PENDING,
            createdAt: block.timestamp,
            expiresAt: expiresAt
        });
        userLimitOrders[msg.sender].push(orderId);
        _addToActiveLimitOrders(orderId);

        emit LimitOrderCreated(orderId, msg.sender, OrderType.STOP_LOSS, tokenAmount, triggerPrice);
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordStopLossPlaced(orderId, msg.sender, token, tokenAmount, triggerPrice);
        }
    }

    /// @inheritdoc IPECOROrders
    function placeStopLimitBuy(
        address stablecoin,
        address token,
        uint256 stablecoinAmount,
        uint256 stopPrice,
        uint256 limitPrice,
        uint256 expiresAt
    ) external override nonReentrant whenNotPaused returns (uint256 orderId) {
        _validatePair(stablecoin, token);
        if (stablecoinAmount == 0) revert ZeroAmount();
        if (stopPrice == 0 || limitPrice == 0) revert InvalidPrice();
        if (limitPrice < stopPrice) revert InvalidPriceRange();
        _validateExpiry(expiresAt);

        vault.pullTokens(stablecoin, msg.sender, stablecoinAmount);

        orderId = nextOrderId++;
        stopLimitOrders[orderId] = StopLimitOrder({
            id: orderId,
            user: msg.sender,
            stablecoin: stablecoin,
            token: token,
            amount: stablecoinAmount,
            stopPrice: stopPrice,
            limitPrice: limitPrice,
            orderType: OrderType.STOP_LIMIT_BUY,
            status: OrderStatus.PENDING,
            createdAt: block.timestamp,
            expiresAt: expiresAt
        });
        userStopLimitOrders[msg.sender].push(orderId);
        _addToActiveStopLimitOrders(orderId);

        emit StopLimitOrderCreated(
            orderId,
            msg.sender,
            OrderType.STOP_LIMIT_BUY,
            stablecoinAmount,
            stopPrice,
            limitPrice
        );
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordStopLimitPlaced(
                orderId,
                msg.sender,
                token,
                stablecoinAmount,
                stopPrice,
                limitPrice,
                true
            );
        }
    }

    /// @inheritdoc IPECOROrders
    function placeStopLimitSell(
        address token,
        address stablecoin,
        uint256 tokenAmount,
        uint256 stopPrice,
        uint256 limitPrice,
        uint256 expiresAt
    ) external override nonReentrant whenNotPaused returns (uint256 orderId) {
        _validatePair(stablecoin, token);
        if (tokenAmount == 0) revert ZeroAmount();
        if (stopPrice == 0 || limitPrice == 0) revert InvalidPrice();
        if (limitPrice > stopPrice) revert InvalidPriceRange();
        _validateExpiry(expiresAt);

        vault.pullTokens(token, msg.sender, tokenAmount);

        orderId = nextOrderId++;
        stopLimitOrders[orderId] = StopLimitOrder({
            id: orderId,
            user: msg.sender,
            stablecoin: stablecoin,
            token: token,
            amount: tokenAmount,
            stopPrice: stopPrice,
            limitPrice: limitPrice,
            orderType: OrderType.STOP_LIMIT_SELL,
            status: OrderStatus.PENDING,
            createdAt: block.timestamp,
            expiresAt: expiresAt
        });
        userStopLimitOrders[msg.sender].push(orderId);
        _addToActiveStopLimitOrders(orderId);

        emit StopLimitOrderCreated(
            orderId,
            msg.sender,
            OrderType.STOP_LIMIT_SELL,
            tokenAmount,
            stopPrice,
            limitPrice
        );
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordStopLimitPlaced(
                orderId,
                msg.sender,
                token,
                tokenAmount,
                stopPrice,
                limitPrice,
                false
            );
        }
    }

    /// @inheritdoc IPECOROrders
    function cancelLimitOrder(uint256 orderId) external override nonReentrant {
        LimitOrder storage order = limitOrders[orderId];
        if (order.user != msg.sender) revert NotOrderOwner();
        if (order.status != OrderStatus.PENDING) revert OrderNotPending();

        order.status = OrderStatus.CANCELLED;
        if (order.orderType == OrderType.LIMIT_BUY) {
            vault.pushTokens(order.stablecoin, msg.sender, order.amount);
        } else {
            vault.pushTokens(order.token, msg.sender, order.amount);
        }
        _removeFromActiveLimitOrders(orderId);

        emit LimitOrderCancelled(orderId);
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordLimitOrderCancelled(orderId, msg.sender);
        }
    }

    /// @inheritdoc IPECOROrders
    function cancelStopLimitOrder(uint256 orderId) external override nonReentrant {
        StopLimitOrder storage order = stopLimitOrders[orderId];
        if (order.user != msg.sender) revert NotOrderOwner();
        if (
            order.status != OrderStatus.PENDING && order.status != OrderStatus.ACTIVATED
        ) revert OrderCannotCancel();

        order.status = OrderStatus.CANCELLED;
        if (order.orderType == OrderType.STOP_LIMIT_BUY) {
            vault.pushTokens(order.stablecoin, msg.sender, order.amount);
        } else {
            vault.pushTokens(order.token, msg.sender, order.amount);
        }
        _removeFromActiveStopLimitOrders(orderId);

        emit StopLimitCancelled(orderId);
    }

    /// @inheritdoc IPECOROrders
    function executeLimitOrder(uint256 orderId) external override nonReentrant onlyRole(KEEPER_ROLE) {
        _executeLimitOrder(orderId);
    }

    /// @inheritdoc IPECOROrders
    function executeStopLimitOrder(
        uint256 orderId
    ) external override nonReentrant onlyRole(KEEPER_ROLE) {
        _executeStopLimitOrder(orderId);
    }

    /// @inheritdoc IPECOROrders
    function checkStopLimitActivation(uint256 orderId) external override onlyRole(KEEPER_ROLE) {
        StopLimitOrder storage order = stopLimitOrders[orderId];
        if (order.status != OrderStatus.PENDING) revert OrderNotPending();
        if (order.expiresAt != 0 && order.expiresAt <= block.timestamp) revert OrderExpired();

        uint256 currentPrice = priceOracle.getPrice(order.token);
        bool shouldActivate = order.orderType == OrderType.STOP_LIMIT_BUY
            ? currentPrice >= order.stopPrice
            : currentPrice <= order.stopPrice;
        if (shouldActivate) {
            order.status = OrderStatus.ACTIVATED;
            emit StopLimitActivated(orderId, currentPrice);
            if (address(transactionTracker) != address(0)) {
                transactionTracker.recordStopLimitActivated(orderId, currentPrice);
            }
        }
    }

    /// @inheritdoc IPECOROrders
    function batchExecuteLimitOrders(
        uint256[] calldata orderIds
    ) external override nonReentrant onlyRole(KEEPER_ROLE) returns (uint256 executedCount) {
        uint256 n = orderIds.length;
        for (uint256 i = 0; i < n; ++i) {
            if (_tryExecuteLimitOrder(orderIds[i])) {
                ++executedCount;
            }
        }
    }

    /// @inheritdoc IPECOROrders
    function batchCheckAndExecuteStopLimits(
        uint256[] calldata orderIds
    )
        external
        override
        nonReentrant
        onlyRole(KEEPER_ROLE)
        returns (uint256 activatedCount, uint256 executedCount)
    {
        uint256 n = orderIds.length;
        for (uint256 i = 0; i < n; ++i) {
            uint256 oid = orderIds[i];
            StopLimitOrder storage order = stopLimitOrders[oid];
            if (order.id == 0) continue;
            if (order.expiresAt != 0 && order.expiresAt <= block.timestamp) continue;

            uint256 currentPrice = priceOracle.getPrice(order.token);
            if (order.status == OrderStatus.PENDING) {
                bool shouldActivate = order.orderType == OrderType.STOP_LIMIT_BUY
                    ? currentPrice >= order.stopPrice
                    : currentPrice <= order.stopPrice;
                if (shouldActivate) {
                    order.status = OrderStatus.ACTIVATED;
                    emit StopLimitActivated(oid, currentPrice);
                    if (address(transactionTracker) != address(0)) {
                        transactionTracker.recordStopLimitActivated(oid, currentPrice);
                    }
                    ++activatedCount;
                }
            }
            if (order.status == OrderStatus.ACTIVATED) {
                if (_tryExecuteStopLimitOrder(oid)) {
                    ++executedCount;
                }
            }
        }
    }

    /// @inheritdoc IPECOROrders
    function getLimitOrder(uint256 orderId) external view override returns (LimitOrder memory) {
        return limitOrders[orderId];
    }

    /// @inheritdoc IPECOROrders
    function getStopLimitOrder(
        uint256 orderId
    ) external view override returns (StopLimitOrder memory) {
        return stopLimitOrders[orderId];
    }

    /// @inheritdoc IPECOROrders
    function getUserLimitOrders(
        address user
    ) external view override returns (uint256[] memory) {
        return userLimitOrders[user];
    }

    /// @inheritdoc IPECOROrders
    function getUserStopLimitOrders(
        address user
    ) external view override returns (uint256[] memory) {
        return userStopLimitOrders[user];
    }

    function getActiveLimitOrderCount() external view returns (uint256) {
        return activeLimitOrderIds.length;
    }

    function getActiveStopLimitOrderCount() external view returns (uint256) {
        return activeStopLimitOrderIds.length;
    }

    /// @inheritdoc IPECOROrders
    function canExecuteLimitOrder(
        uint256 orderId
    ) external view override returns (bool, string memory) {
        LimitOrder memory order = limitOrders[orderId];
        if (order.status != OrderStatus.PENDING) return (false, "Order not pending");
        if (order.expiresAt != 0 && order.expiresAt <= block.timestamp) return (false, "Order expired");
        uint256 currentPrice = priceOracle.getPrice(order.token);
        if (order.orderType == OrderType.LIMIT_BUY && currentPrice > order.targetPrice) {
            return (false, "Price above target");
        }
        if (order.orderType == OrderType.LIMIT_SELL && currentPrice < order.targetPrice) {
            return (false, "Price below target");
        }
        if (order.orderType == OrderType.STOP_LOSS && currentPrice > order.targetPrice) {
            return (false, "Price above trigger");
        }
        return (true, "Executable");
    }

    /// @inheritdoc IPECOROrders
    function getExecutableLimitOrders(
        uint256 maxCount
    ) external view override returns (uint256[] memory orderIds) {
        uint256[] memory temp = new uint256[](maxCount);
        uint256 found;
        uint256 n = activeLimitOrderIds.length;
        for (uint256 i = 0; i < n && found < maxCount; ++i) {
            uint256 oid = activeLimitOrderIds[i];
            LimitOrder memory o = limitOrders[oid];
            if (o.status != OrderStatus.PENDING) continue;
            if (o.expiresAt != 0 && o.expiresAt <= block.timestamp) continue;

            uint256 p = priceOracle.getPrice(o.token);
            bool exec = (o.orderType == OrderType.LIMIT_BUY && p <= o.targetPrice) ||
                (o.orderType == OrderType.LIMIT_SELL && p >= o.targetPrice) ||
                (o.orderType == OrderType.STOP_LOSS && p <= o.targetPrice);
            if (exec) {
                temp[found++] = oid;
            }
        }
        orderIds = new uint256[](found);
        for (uint256 i = 0; i < found; ++i) {
            orderIds[i] = temp[i];
        }
    }

    /// @inheritdoc IPECOROrders
    function getActivatableStopLimits(
        uint256 maxCount
    ) external view override returns (uint256[] memory orderIds) {
        uint256[] memory temp = new uint256[](maxCount);
        uint256 found;
        uint256 n = activeStopLimitOrderIds.length;
        for (uint256 i = 0; i < n && found < maxCount; ++i) {
            uint256 oid = activeStopLimitOrderIds[i];
            StopLimitOrder memory o = stopLimitOrders[oid];
            if (o.status != OrderStatus.PENDING) continue;
            if (o.expiresAt != 0 && o.expiresAt <= block.timestamp) continue;

            uint256 p = priceOracle.getPrice(o.token);
            bool act = (o.orderType == OrderType.STOP_LIMIT_BUY && p >= o.stopPrice) ||
                (o.orderType == OrderType.STOP_LIMIT_SELL && p <= o.stopPrice);
            if (act) {
                temp[found++] = oid;
            }
        }
        orderIds = new uint256[](found);
        for (uint256 i = 0; i < found; ++i) {
            orderIds[i] = temp[i];
        }
    }

    /// @inheritdoc IPECOROrders
    function getExecutableStopLimits(
        uint256 maxCount
    ) external view override returns (uint256[] memory orderIds) {
        uint256[] memory temp = new uint256[](maxCount);
        uint256 found;
        uint256 n = activeStopLimitOrderIds.length;
        for (uint256 i = 0; i < n && found < maxCount; ++i) {
            uint256 oid = activeStopLimitOrderIds[i];
            StopLimitOrder memory o = stopLimitOrders[oid];
            if (o.status != OrderStatus.ACTIVATED) continue;
            if (o.expiresAt != 0 && o.expiresAt <= block.timestamp) continue;

            uint256 p = priceOracle.getPrice(o.token);
            bool exec = (o.orderType == OrderType.STOP_LIMIT_BUY && p <= o.limitPrice) ||
                (o.orderType == OrderType.STOP_LIMIT_SELL && p >= o.limitPrice);
            if (exec) {
                temp[found++] = oid;
            }
        }
        orderIds = new uint256[](found);
        for (uint256 i = 0; i < found; ++i) {
            orderIds[i] = temp[i];
        }
    }

    function _executeLimitOrder(uint256 orderId) internal {
        LimitOrder storage order = limitOrders[orderId];
        if (order.status != OrderStatus.PENDING) revert OrderNotPending();
        if (order.expiresAt != 0 && order.expiresAt <= block.timestamp) revert OrderExpired();

        uint256 currentPrice = priceOracle.getPrice(order.token);
        uint256 stablePrice = priceOracle.getPrice(order.stablecoin);

        if (order.orderType == OrderType.LIMIT_BUY) {
            if (currentPrice > order.targetPrice) revert PriceNotMet();
            uint256 tokenAmount = _calcOutput(order.stablecoin, order.token, order.amount, stablePrice, currentPrice);
            if (!vault.hasLiquidity(order.token, tokenAmount)) revert InsufficientLiquidity();
            vault.pushTokens(order.token, order.user, tokenAmount);
        } else if (order.orderType == OrderType.LIMIT_SELL) {
            if (currentPrice < order.targetPrice) revert PriceNotMet();
            uint256 stableAmount = _calcOutput(order.token, order.stablecoin, order.amount, currentPrice, stablePrice);
            if (!vault.hasLiquidity(order.stablecoin, stableAmount)) revert InsufficientLiquidity();
            vault.pushTokens(order.stablecoin, order.user, stableAmount);
        } else if (order.orderType == OrderType.STOP_LOSS) {
            if (currentPrice > order.targetPrice) revert PriceNotMet();
            uint256 stableAmount = _calcOutput(order.token, order.stablecoin, order.amount, currentPrice, stablePrice);
            if (!vault.hasLiquidity(order.stablecoin, stableAmount)) revert InsufficientLiquidity();
            vault.pushTokens(order.stablecoin, order.user, stableAmount);
            if (address(transactionTracker) != address(0)) {
                transactionTracker.recordStopLossTriggered(
                    orderId,
                    order.user,
                    order.targetPrice,
                    currentPrice,
                    stableAmount
                );
            }
        }
        order.status = OrderStatus.EXECUTED;
        _removeFromActiveLimitOrders(orderId);

        emit LimitOrderExecuted(orderId, currentPrice);
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordLimitOrderExecuted(orderId, order.user, currentPrice);
        }
    }

    function _executeStopLimitOrder(uint256 orderId) internal {
        StopLimitOrder storage order = stopLimitOrders[orderId];
        if (order.status != OrderStatus.ACTIVATED) revert OrderNotActivated();
        if (order.expiresAt != 0 && order.expiresAt <= block.timestamp) revert OrderExpired();

        uint256 currentPrice = priceOracle.getPrice(order.token);
        uint256 stablePrice = priceOracle.getPrice(order.stablecoin);

        if (order.orderType == OrderType.STOP_LIMIT_BUY) {
            if (currentPrice > order.limitPrice) revert PriceNotMet();
            uint256 tokenAmount = _calcOutput(order.stablecoin, order.token, order.amount, stablePrice, currentPrice);
            if (!vault.hasLiquidity(order.token, tokenAmount)) revert InsufficientLiquidity();
            vault.pushTokens(order.token, order.user, tokenAmount);
        } else {
            if (currentPrice < order.limitPrice) revert PriceNotMet();
            uint256 stableAmount = _calcOutput(order.token, order.stablecoin, order.amount, currentPrice, stablePrice);
            if (!vault.hasLiquidity(order.stablecoin, stableAmount)) revert InsufficientLiquidity();
            vault.pushTokens(order.stablecoin, order.user, stableAmount);
        }
        order.status = OrderStatus.EXECUTED;
        _removeFromActiveStopLimitOrders(orderId);

        emit StopLimitExecuted(orderId, currentPrice);
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordStopLimitExecuted(orderId, order.user, currentPrice);
        }
    }

    /// @dev Non-reverting best-effort execution; returns false on any skip/fail.
    function _tryExecuteLimitOrder(uint256 orderId) internal returns (bool) {
        LimitOrder storage order = limitOrders[orderId];
        if (order.status != OrderStatus.PENDING) return false;
        if (order.expiresAt != 0 && order.expiresAt <= block.timestamp) return false;

        uint256 cp = priceOracle.getPrice(order.token);
        uint256 sp = priceOracle.getPrice(order.stablecoin);

        if (order.orderType == OrderType.LIMIT_BUY) {
            if (cp > order.targetPrice) return false;
            uint256 amt = _calcOutput(order.stablecoin, order.token, order.amount, sp, cp);
            if (!vault.hasLiquidity(order.token, amt)) return false;
            vault.pushTokens(order.token, order.user, amt);
        } else if (order.orderType == OrderType.LIMIT_SELL) {
            if (cp < order.targetPrice) return false;
            uint256 amt = _calcOutput(order.token, order.stablecoin, order.amount, cp, sp);
            if (!vault.hasLiquidity(order.stablecoin, amt)) return false;
            vault.pushTokens(order.stablecoin, order.user, amt);
        } else if (order.orderType == OrderType.STOP_LOSS) {
            if (cp > order.targetPrice) return false;
            uint256 amt = _calcOutput(order.token, order.stablecoin, order.amount, cp, sp);
            if (!vault.hasLiquidity(order.stablecoin, amt)) return false;
            vault.pushTokens(order.stablecoin, order.user, amt);
            if (address(transactionTracker) != address(0)) {
                transactionTracker.recordStopLossTriggered(
                    orderId,
                    order.user,
                    order.targetPrice,
                    cp,
                    amt
                );
            }
        }
        order.status = OrderStatus.EXECUTED;
        _removeFromActiveLimitOrders(orderId);

        emit LimitOrderExecuted(orderId, cp);
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordLimitOrderExecuted(orderId, order.user, cp);
        }
        return true;
    }

    function _tryExecuteStopLimitOrder(uint256 orderId) internal returns (bool) {
        StopLimitOrder storage order = stopLimitOrders[orderId];
        if (order.status != OrderStatus.ACTIVATED) return false;
        if (order.expiresAt != 0 && order.expiresAt <= block.timestamp) return false;

        uint256 cp = priceOracle.getPrice(order.token);
        uint256 sp = priceOracle.getPrice(order.stablecoin);

        if (order.orderType == OrderType.STOP_LIMIT_BUY) {
            if (cp > order.limitPrice) return false;
            uint256 amt = _calcOutput(order.stablecoin, order.token, order.amount, sp, cp);
            if (!vault.hasLiquidity(order.token, amt)) return false;
            vault.pushTokens(order.token, order.user, amt);
        } else {
            if (cp < order.limitPrice) return false;
            uint256 amt = _calcOutput(order.token, order.stablecoin, order.amount, cp, sp);
            if (!vault.hasLiquidity(order.stablecoin, amt)) return false;
            vault.pushTokens(order.stablecoin, order.user, amt);
        }
        order.status = OrderStatus.EXECUTED;
        _removeFromActiveStopLimitOrders(orderId);

        emit StopLimitExecuted(orderId, cp);
        if (address(transactionTracker) != address(0)) {
            transactionTracker.recordStopLimitExecuted(orderId, order.user, cp);
        }
        return true;
    }

    function _calcOutput(
        address tokenIn,
        address tokenOut,
        uint256 amountIn,
        uint256 priceIn,
        uint256 priceOut
    ) internal view returns (uint256) {
        uint8 dIn = vault.getTokenDecimals(tokenIn);
        uint8 dOut = vault.getTokenDecimals(tokenOut);
        uint256 num = SidioraMath.mulDiv(amountIn, priceIn, 1);
        if (dOut > dIn) {
            num = num * (10 ** (uint256(dOut) - uint256(dIn)));
        }
        uint256 den = priceOut;
        if (dIn > dOut) {
            den = den * (10 ** (uint256(dIn) - uint256(dOut)));
        }
        return num / den;
    }

    function _validatePair(address stablecoin, address token) internal view {
        if (!vault.isStablecoin(stablecoin)) revert NotAStablecoin();
        if (vault.isStablecoin(token)) revert TokenIsStablecoin();
    }

    function _validateExpiry(uint256 expiresAt) internal view {
        if (expiresAt != 0 && expiresAt <= block.timestamp) revert InvalidExpiry();
    }

    function _addToActiveLimitOrders(uint256 orderId) internal {
        activeLimitOrderIds.push(orderId);
        _limitOrderActiveIndex[orderId] = activeLimitOrderIds.length;
    }

    function _addToActiveStopLimitOrders(uint256 orderId) internal {
        activeStopLimitOrderIds.push(orderId);
        _stopLimitOrderActiveIndex[orderId] = activeStopLimitOrderIds.length;
    }

    function _removeFromActiveLimitOrders(uint256 orderId) internal {
        uint256 idx = _limitOrderActiveIndex[orderId];
        if (idx == 0) return;
        uint256 i = idx - 1;
        uint256 last = activeLimitOrderIds.length - 1;
        if (i != last) {
            uint256 lastId = activeLimitOrderIds[last];
            activeLimitOrderIds[i] = lastId;
            _limitOrderActiveIndex[lastId] = idx;
        }
        activeLimitOrderIds.pop();
        delete _limitOrderActiveIndex[orderId];
    }

    function _removeFromActiveStopLimitOrders(uint256 orderId) internal {
        uint256 idx = _stopLimitOrderActiveIndex[orderId];
        if (idx == 0) return;
        uint256 i = idx - 1;
        uint256 last = activeStopLimitOrderIds.length - 1;
        if (i != last) {
            uint256 lastId = activeStopLimitOrderIds[last];
            activeStopLimitOrderIds[i] = lastId;
            _stopLimitOrderActiveIndex[lastId] = idx;
        }
        activeStopLimitOrderIds.pop();
        delete _stopLimitOrderActiveIndex[orderId];
    }
}

/*
 * ══════════════════════════════════════════════════════════════════════════════════
 *
 *              Paxlabs HyperPax-OS-Protocol LICENSE (HyperPax-OS-Protocol)
 *                 Copyright © 2026 Paxlabs Inc. All rights reserved.
 *           
 *
 * ══════════════════════════════════════════════════════════════════════════════════
 *
 *   HyperPax-OS-Protocol License — Summary (non-binding): You may read, use, deploy, 
 *   and integrate HyperPax-OS-Protocol. If you Modify and distribute/deploy 
 *   the Modified version, you must release your changes under this same license.
 *   NO Commercial License is required until you cross a Commercial Trigger 
 *   (e.g., Charged Fees > US$100,000 in any rolling 12-month period or in any 
 *   single calendar month, or Liquidity Under Control > US$10,000,000).
 *   This summary is for convenience only.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   1) DEFINITION
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   1.1  "Licensed Work" means the HyperPax-OS-Protocol stack as released in this repository,
 *        including: (a) the core HyperPax-OS-Protocol execution engine, instruction/handler
 *        interfaces, example instruction libraries published by Paxlabs, SDK stubs, schemas,
 *        configuration, tests, tooling, bytecode/ABIs, and deployment scripts; (b) all
 *        documentation and technical specifications published by Paxlabs; and (c) all updates,
 *        patches, and new versions of the foregoing that Paxlabs publishes under this license.
 *
 *   1.2  "Charged Fees" means all monetary or in-kind value (fiat, crypto, tokens, credits,
 *        rebates, or other consideration) that You or Your Affiliates directly or indirectly
 *        receive or accrue in connection with operating, offering, or providing access to any
 *        product or service that is powered by, routed through, or materially enabled by the
 *        Licensed Work, including without limitation: (a) swap/trade/execution fees, positive-
 *        slippage capture, spreads, mark-ups, retained priority/tips; (b) maker/taker rebates,
 *        order-flow payments, routing/referral/affiliate fees, MEV/builder/bundle payments and
 *        other extractable-value shares; (c) subscription, seat, usage, API, or platform fees
 *        attributable to the Licensed Work; (d) performance/incentive/carried-interest fees,
 *        revenue shares, or similar participation; and (e) token grants, rewards, airdrops,
 *        distributions, or rebates received by or for You or Your Affiliates in consideration
 *        of or tied to such operations. Charged Fees are measured on a gross-receipts basis at
 *        fair-market value in USD when received or accrued, include amounts received by
 *        agents/designees or wallets You control, and must be reasonably allocated for bundles.
 *        Anti-avoidance: relabeling, splitting, routing through Affiliates/Related Parties,
 *        offsetting, or deferring does not exclude amounts; Affiliates/common control are
 *        aggregated; the Control or Benefit Principle applies.
 *
 *   1.3  "Commercial License" means a separate written agreement between Paxlabs and You
 *        (and/or Your Affiliates), that grants You the right to engage in Commercial Use of
 *        the Licensed Work subject to negotiated terms, conditions, and fees.
 *
 *   1.4  "Control or Benefit Principle." Triggers and obligations apply where you or your
 *        Affiliates control the relevant activity or benefit economically from it (directly
 *        or through agents/DAOs under your direction).
 *
 *   1.5  "Rolling Year" means any period of twelve (12) consecutive months measured on a
 *        rolling basis.
 *
 *   1.6  "Liquidity Under Control (LUC)" means the aggregate fair-market USD value of real,
 *        non-synthetic, non-levered, withdrawable assets that Your (or Your Affiliates'/
 *        agents') products, services, or code can instruct or cause to be moved or committed
 *        via the Licensed Work (e.g., wallet balances under automated control, committed
 *        liquidity, or programmatic authorization) at the time assessed.
 *
 *   1.7  "Modify" (and "Modified Work") means to change, fork, translate, extend, or create a
 *        derivative work of the Licensed Work, including: (a) altering source or bytecode;
 *        (b) creating plug-ins/modules/instruction programs that run in the same program/
 *        runtime or EVM address space (e.g., static/dynamic linking, delegatecall/proxy
 *        patterns); or (c) bundling the Licensed Work and additions as a single product.
 *
 *   1.8  "You" (and "Your") means the individual or legal entity exercising rights under this
 *        license, and its Affiliates. "Affiliates" are entities controlling, controlled by, or
 *        under common control with a party, directly or indirectly.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   2) GRANT
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   2.1  Source and Object Use. Subject to Sections 3–11, Paxlabs grants You a worldwide,
 *        non-exclusive license to use, copy, distribute unmodified source/object forms of the
 *        Licensed Work.
 *
 *   2.2  Pure Caller Use (Integration-Only / Non-Modifying). Pure Caller Use means building or
 *        operating products or services that interact with the Licensed Work solely by forming
 *        calldata, submitting transactions, or reading state through published ABIs, APIs, or
 *        RPC endpoints, without distributing any Modified Work. Pure Caller Use is permitted
 *        under this License and does not, by itself, trigger any payment obligations or
 *        Commercial License. However, if in connection with Pure Caller Use You or Your
 *        Affiliates (a) charge or retain any fees, spreads, rebates, incentives, or other
 *        consideration; (b) meet any Trigger in Section 5.2; such use constitutes Commercial
 *        Use and requires obtaining a Commercial License from Paxlabs. Even where Pure Caller
 *        Use is not met, see §5.3 for the current enforcement waiver applicable to Volume
 *        Activities.
 *
 *   2.3  Audit/Research Safe Harbor. Security auditors and researchers may compile, test, and
 *        report on the Licensed Work in the course of good-faith security research.
 *
 *   2.4  For any distribution, public display, public performance, publication, reporting,
 *        disclosure, or other public communication of any portion of the Licensed Work or any
 *        analysis, results, or outputs derived from the Licensed Work, You must preserve all
 *        existing copyright, license, and attribution notices included in the Licensed Work
 *        and must include a reasonable attribution identifying the source as
 *        "HyperPaxeer — © Paxlabs Inc 2026" (or any successor notice included in the
 *        Licensed Work).
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   3) COPYLEFT FOR MODIFICATIONS & EXTENSIONS
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   3.1  If you Modify or distribute any portion of the Licensed Work, you must:
 *        A. Publish under this same license (LicenseRef-Paxlabs-HyperPax-OS-Protocol license),
 *           at no charge, complete corresponding source of any portions of Your work that
 *           modify, extend, incorporate, or otherwise rely on the Licensed Work;
 *        B. Preserve existing copyright, license, and third-party notices;
 *        C. Add prominent attribution: "Powered by HyperPax-OS-Protocol — © Paxlabs Inc 2026"
 *           in repository README and UI where applicable;
 *        D. Clearly mark changes and date of change;
 *        E. Provide build and deployment instructions sufficient for reproducibility.
 *
 *   3.2  This copyleft covers all forms of Modification, combination, or use of the Licensed
 *        Work in or with other code, products, or systems.
 *
 *   3.3  The obligations in §§3.1 A-B apply only to components that are derivative of the
 *        Licensed Work. Independent code that simply calls, interfaces with, or is distributed
 *        alongside the Licensed Work is not subject to this requirement.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   4) NON-COMMERCIAL FREE USE
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   Non-commercial use (including experimentation, prototyping, hackathons, research, community
 *   pilots) is free of charge, subject to Section 3 for any Modifications; and provided that
 *   such use does not constitute or involve any activity described in Section 5.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   5) COMMERCIAL TRIGGER
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   5.1  Commercial Use: Any Commercial Use of the Licensed Work requires a Commercial License
 *        from Paxlabs, unless otherwise expressly stated. Commercial Use means any use of the
 *        Licensed Work that provides, enables, or is integrated into any product, service,
 *        system, workflow, or operation from which You or Your Affiliates derive, or reasonably
 *        expect to derive, monetary or in-kind commercial value, directly or indirectly,
 *        including through Charged Fees or other consideration.
 *
 *   5.2  Without limiting §5.1, You (and Your Affiliates) must obtain a Commercial License from
 *        Paxlabs if any of the following occur:
 *        A. Aggregated Fees Trigger: Your aggregated Charged Fees attributable to usage of the
 *           Licensed Work exceed USD 100,000 in any Rolling Year.
 *        B. LUC Trigger: Your LUC exceeds USD 10,000,000 at any time.
 *        C. Operator/Liquidity Provider Direct-Use. You (or Your Affiliate) operate instruction
 *           programs or services (e.g., deploying, offering, or running products or services
 *           powered by, routed through, or materially enabled by the Licensed Work) or acting
 *           as a Liquidity Provider that directly exercise the Licensed Work (bypassing Paxeer
 *           Network/Paxlabs or other permitted/licensed interfaces) to capture fees or value
 *           and, in doing so, satisfy Triggers A or B above.
 *        You must aggregate commonly-controlled/Affiliated entities; no disaggregation, white-
 *        labeling, or similar structuring to avoid a Trigger is permitted. The Control or
 *        Benefit Principle applies.
 *
 *   5.3  Notwithstanding §§ 5.1 - 5.2, Paxlabs presently waives enforcement of the Commercial
 *        Triggers for parties whose activities consist primarily of routing order flow,
 *        aggregation, arbitrage, or market-making through the Licensed Work ("Volume
 *        Activities"), including where such parties (i) trade with their own or third-party
 *        capital and/or (ii) charge or retain fees, spreads, rebates, or other compensation.
 *        This waiver is not a license, creates no reliance rights, and is revocable by Paxlabs
 *        at any time in its sole discretion, including with respect to existing users, by
 *        (a) public notice in the project repository or (b) direct notice. Upon notice of
 *        revocation, you must within ten (10) days cease the Volume Activities or obtain a
 *        Commercial License; continued use thereafter constitutes unauthorized Commercial Use.
 *        This waiver does not excuse past breaches unrelated to this subsection.
 *
 *   5.4  Crossing any Trigger or any other Commercial Use requires you to contact Paxlabs
 *        within 15 days at license@Paxlabs.com to execute Commercial License. Commercial
 *        License terms are confidential and may change.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   6) AUDIT
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   Once per year, Paxlabs may request an independent revenue/LUC audit (under NDA) at
 *   Paxlabs's expense; if under-reporting exceeds 5%, You reimburse reasonable audit costs in
 *   addition to other remedies. Paxlabs may also request an additional attestation "for cause"
 *   (objective indications of a Trigger). You must reasonably cooperate with any such
 *   attestation, including providing accurate records, logs, and other information reasonably
 *   necessary.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   7) ADDITIONAL INTELLECTUAL PROPERTY TERMS
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   7.1  Patents. Paxlabs grants a limited, non-exclusive, worldwide license under Paxlabs's
 *        patent claims that read on the Licensed Work solely to the extent necessary to
 *        exercise the rights expressly granted to You under this License. Nothing in this
 *        License grants You any right to patent, claim, or seek protection for (a) the
 *        Licensed Work (whether modified or unmodified), (b) any Modification of the Licensed
 *        Work, or (c) any work or system that incorporates, combines with, or depends on the
 *        Licensed Work. This patent license terminates if you (or your Affiliates) stop using
 *        the Licensed Work or assert any patent claim against Paxlabs or compliant users of
 *        the Licensed Work. No implied patent license is granted beyond this clause.
 *
 *   7.2  Trademarks & Branding. Trademarks. No rights are granted to use any Paxlabs/
 *        HyperPax-OS-Protocol/Paxeer Network names, logos, or trademarks, or any "Powered by
 *        HyperPaxeer" or similar designation, except solely to make truthful statements of
 *        compatibility or integration. Any use must comply with Paxlabs's brand guidelines and
 *        may require separate written permission.
 *
 *   7.3  Except as expressly granted, no other rights (by implication, estoppel, or otherwise)
 *        are granted, copyrights, patents, trade secrets, trademarks, or other IP.
 *
 *   7.4  You must not suggest Paxlabs endorses or certifies Your product absent a written
 *        agreement.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   8) WARRANTY & LIABILITY
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   THE LICENSED WORK IS PROVIDED BY PAXLABS "AS IS" AND "AS AVAILABLE," WITHOUT WARRANTIES OF
 *   ANY KIND, EXPRESS OR IMPLIED, INCLUDING MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE,
 *   NON-INFRINGEMENT, AND THAT OPERATION WILL BE UNINTERRUPTED OR ERROR-FREE. TO THE MAXIMUM
 *   EXTENT PERMITTED BY LAW, PAXLABS, ITS AFFILIATES AND CONTRIBUTORS ARE NOT LIABLE FOR
 *   INDIRECT, INCIDENTAL, SPECIAL, CONSEQUENTIAL, EXEMPLARY, OR PUNITIVE DAMAGES, OR LOST
 *   PROFITS/REVENUE/GOODWILL, ARISING FROM OR RELATED TO THIS LICENSE OR THE LICENSED WORK,
 *   EVEN IF ADVISED OF THE POSSIBILITY.
 *
 *   Nothing in this Section limits Paxlabs's ability to seek injunctive relief without bond in
 *   addition to other remedies or to enforce Your obligations under this License.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   9) TERMINATION
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   Material breach, including any breach of §§ 2-9, not cured within 15 days of notice
 *   terminates this License. Prior compliant distributions survive. Sections 2.4, 3, 8, 10,
 *   11.3 survive termination.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   10) GOVERNING LAW; VENUE; INJUNCTIONS
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   This License is governed by the laws of the State of New York, excluding conflict rules.
 *   The parties submit to the exclusive jurisdiction and venue of the state and federal courts
 *   located in New York County, New York (SDNY). Each party consents to injunctive relief
 *   (including specific performance) for actual or threatened breach.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   11) NOTICES; ASSIGNMENT; ENTIRE AGREEMENT
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   11.1 Notices. Legal or any other notices to Paxlabs: legal@Paxlabs.com (with subject
 *        "HyperPaxeer Notice").
 *
 *   11.2 Third-Party Components. Portions of the Licensed Work may incorporate, bundle, or
 *        reference third-party components governed by their own licenses. You must comply with
 *        those third-party terms; nothing in this License limits rights granted by those
 *        licenses. Preserve all third-party copyright and license notices. A list of such
 *        components and licenses is provided in THIRD_PARTY_NOTICES (and/or in file headers)
 *        and may be updated from time to time.
 *
 *   11.3 Assignment. You may not assign this License (by law or otherwise) without Paxlabs's
 *        prior written consent; any unauthorized assignment is void. Paxlabs may assign freely.
 *
 *   11.4 Entire Agreement. This License is the entire agreement for the Licensed Work and
 *        supersedes prior understandings. If any provision is unenforceable, it will be
 *        modified to the minimum extent necessary to be enforceable; the remainder stays in
 *        effect. No waiver is effective unless in writing.
 *
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *   12) VERSIONING
 * ───────────────────────────────────────────────────────────────────────────────────────────────
 *
 *   Paxlabs may publish new or updated versions of this License from time to time. Each
 *   release of the Licensed Work is governed by the license version identified in the
 *   repository for that release. Paxlabs may also re-release the Licensed Work, or any
 *   portion of it, under different license terms in future releases.
 *
 * ═══════════════════════════════════════════════════════════════════════════════════════════════
 *   END OF LICENSE — Contact: license@Paxlabs.com  |  legal@Paxlabs.com
 * ═══════════════════════════════════════════════════════════════════════════════════════════════
 */
