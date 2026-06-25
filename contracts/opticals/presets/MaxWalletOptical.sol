// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../BaseOptical.sol";
import "../../libraries/BitFlag.sol";

/// @title MaxWalletOptical
/// @notice Enforces maximum token holding percentage per wallet after buy swaps.
/// @dev Uses afterSwap hook. Immutable once deployed.
///      Pool address is exempt (it holds the liquidity).
///      Only checks on buys — sells naturally reduce holdings.
contract MaxWalletOptical is BaseOptical {
    error MaxWalletExceeded();

    /// @notice Maximum wallet holding in basis points of total supply (e.g., 200 = 2%)
    uint256 public immutable maxWalletBps;

    /// @notice Addresses exempt from max wallet check
    mapping(address => bool) public exempt;

    /// @param _poolRegistry PoolRegistry address for pool validation
    /// @param _owner Deployer/owner address
    /// @param _maxWalletBps Maximum holding in bps of total supply
    constructor(
        address _poolRegistry,
        address _owner,
        uint256 _maxWalletBps
    ) BaseOptical(_poolRegistry, _owner) {
        maxWalletBps = _maxWalletBps;
    }

    /// @notice Set an address as exempt from max wallet check
    /// @dev Only owner can set exemptions (e.g., pool address, treasury)
    function setExempt(address account, bool isExempt) external onlyOwner {
        exempt[account] = isExempt;
    }

    /// @inheritdoc IOptical
    function getFlags() external pure override returns (uint8) {
        return BitFlag.AFTER_SWAP;
    }

    /// @inheritdoc IOptical
    function afterSwap(
        address pool,
        address sender,
        bool isBuy,
        uint256, /* amountIn */
        uint256 /* amountOut */
    ) external override returns (bytes4) {
        // Only check buys (sells reduce holdings)
        if (!isBuy) {
            return IOptical.afterSwap.selector;
        }

        // Skip if sender is exempt
        if (exempt[sender] || exempt[pool]) {
            return IOptical.afterSwap.selector;
        }

        // Read token address and check sender's balance
        (bool success, bytes memory data) = pool.staticcall(
            abi.encodeWithSignature("tokenAddress()")
        );
        if (!success || data.length < 32) {
            return IOptical.afterSwap.selector;
        }
        address tokenAddr = abi.decode(data, (address));

        // Get sender's token balance
        (success, data) = tokenAddr.staticcall(
            abi.encodeWithSignature("balanceOf(address)", sender)
        );
        if (!success || data.length < 32) {
            return IOptical.afterSwap.selector;
        }
        uint256 senderBalance = abi.decode(data, (uint256));

        // Get total supply
        (success, data) = tokenAddr.staticcall(
            abi.encodeWithSignature("totalSupply()")
        );
        if (!success || data.length < 32) {
            return IOptical.afterSwap.selector;
        }
        uint256 totalSupply = abi.decode(data, (uint256));

        // Check: senderBalance <= totalSupply * maxWalletBps / 10000
        uint256 maxWalletAmount = (totalSupply * maxWalletBps) / 10000;
        if (senderBalance > maxWalletAmount) {
            revert MaxWalletExceeded();
        }

        return IOptical.afterSwap.selector;
    }
}
