// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../interfaces/IEventEmitter.sol";

/// @notice Simplified mock EventEmitter for testing contracts that emit events through it
contract MockEventEmitter {
    // Track calls for verification
    bytes32 public lastConfigKey;
    uint256 public lastConfigOldValue;
    uint256 public lastConfigNewValue;
    uint256 public configUpdateCount;

    mapping(address => bool) public authorizedEmitters;

    event ConfigUpdated(bytes32 indexed key, uint256 oldValue, uint256 newValue, uint256 timestamp, uint256 blockNumber);
    event MarketCreated(bytes32 indexed poolId, address indexed token, address indexed creator, address pool, address optical, uint256 timestamp, uint256 blockNumber);
    event Swap(bytes32 indexed poolId, address indexed sender, bool isBuy, uint256 amountIn, uint256 amountOut, uint256 fee, uint256 price, uint256 timestamp, uint256 blockNumber);
    event FeeRecorded(bytes32 indexed poolId, uint256 feeAmount, uint256 protocolCut, uint256 poolCut, uint256 timestamp, uint256 blockNumber);
    event FeeDistributed(bytes32 indexed poolId, uint256 nftId, uint8 strategy, uint256 amount, address recipient, uint256 timestamp, uint256 blockNumber);
    event FeeStrategyChanged(bytes32 indexed poolId, uint256 nftId, uint8 oldStrategy, uint8 newStrategy, uint256 timestamp, uint256 blockNumber);
    event PoolStateUpdated(bytes32 indexed poolId, uint256 virtualReserve, uint256 realReserve, uint256 tokenReserve, uint256 price, uint256 timestamp, uint256 blockNumber);

    function setAuthorizedEmitter(address emitter, bool authorized) external {
        authorizedEmitters[emitter] = authorized;
    }

    function isAuthorizedEmitter(address emitter) external view returns (bool) {
        return authorizedEmitters[emitter];
    }

    function emitConfigUpdated(bytes32 key, uint256 oldValue, uint256 newValue) external {
        lastConfigKey = key;
        lastConfigOldValue = oldValue;
        lastConfigNewValue = newValue;
        configUpdateCount++;
        emit ConfigUpdated(key, oldValue, newValue, block.timestamp, block.number);
    }

    function emitMarketCreated(bytes32 poolId, address token, address creator, address pool, address optical) external {
        emit MarketCreated(poolId, token, creator, pool, optical, block.timestamp, block.number);
    }

    function emitSwap(bytes32 poolId, address sender, bool isBuy, uint256 amountIn, uint256 amountOut, uint256 fee, uint256 price) external {
        emit Swap(poolId, sender, isBuy, amountIn, amountOut, fee, price, block.timestamp, block.number);
    }

    function emitFeeRecorded(bytes32 poolId, uint256 feeAmount, uint256 protocolCut, uint256 poolCut) external {
        emit FeeRecorded(poolId, feeAmount, protocolCut, poolCut, block.timestamp, block.number);
    }

    function emitFeeDistributed(bytes32 poolId, uint256 nftId, uint8 strategy, uint256 amount, address recipient) external {
        emit FeeDistributed(poolId, nftId, strategy, amount, recipient, block.timestamp, block.number);
    }

    function emitFeeStrategyChanged(bytes32 poolId, uint256 nftId, uint8 oldStrategy, uint8 newStrategy) external {
        emit FeeStrategyChanged(poolId, nftId, oldStrategy, newStrategy, block.timestamp, block.number);
    }

    function emitPoolStateUpdated(bytes32 poolId, uint256 virtualReserve, uint256 realReserve, uint256 tokenReserve, uint256 price) external {
        emit PoolStateUpdated(poolId, virtualReserve, realReserve, tokenReserve, price, block.timestamp, block.number);
    }
}
