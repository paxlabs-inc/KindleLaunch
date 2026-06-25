// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../base/ERC20Base.sol";

/// @title SidioraERC20
/// @notice Minimal immutable ERC20 token deployed via CREATE2 by the Factory
/// @dev Total supply minted once in constructor to recipient. No mint/burn after creation.
contract SidioraERC20 is ERC20Base {
    /// @param _name Token name
    /// @param _symbol Token symbol
    /// @param _totalSupply Total supply to mint (6 decimals)
    /// @param _recipient Address to receive the entire supply (the pool)
    constructor(
        string memory _name,
        string memory _symbol,
        uint256 _totalSupply,
        address _recipient
    ) ERC20Base(_name, _symbol, 6) {
        if (_recipient == address(0)) revert ZeroAddress();
        _mint(_recipient, _totalSupply);
    }
}
