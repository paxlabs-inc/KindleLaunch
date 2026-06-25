// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../base/Initializable.sol";
import "../base/UUPSUpgradeable.sol";
import "../base/AccessControl.sol";
import "../interfaces/ITreasury.sol";
import "../libraries/TransferHelper.sol";

/// @title Treasury
/// @notice Receives and holds protocol fee revenue. Disbursements via governance.
/// @dev UUPS proxy. Only DEPOSITOR_ROLE can deposit. Only admin can withdraw.
contract Treasury is ITreasury, Initializable, UUPSUpgradeable, AccessControl {
    bytes32 public constant DEPOSITOR_ROLE = keccak256("DEPOSITOR_ROLE");

    mapping(address => uint256) private _balances;

    address public eventEmitter;

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(address _eventEmitter, address _admin) external initializer {
        if (_admin == address(0)) revert ZeroAddress();
        eventEmitter = _eventEmitter;
        _grantRole(DEFAULT_ADMIN_ROLE, _admin);
    }

    /// @notice Deposit tokens into the treasury
    /// @dev Caller must have DEPOSITOR_ROLE. Tokens must be pre-approved.
    function deposit(address token, uint256 amount) external override onlyRole(DEPOSITOR_ROLE) {
        if (amount == 0) revert ZeroAmount();
        TransferHelper.safeTransferFrom(token, msg.sender, address(this), amount);
        _balances[token] += amount;
        emit Deposited(token, msg.sender, amount);
    }

    /// @notice Withdraw tokens from the treasury
    /// @dev Only admin (governance/timelock) can withdraw.
    function withdraw(
        address token,
        address to,
        uint256 amount
    ) external override onlyRole(DEFAULT_ADMIN_ROLE) {
        if (to == address(0)) revert ZeroAddress();
        if (amount == 0) revert ZeroAmount();
        if (_balances[token] < amount) revert InsufficientBalance();

        _balances[token] -= amount;
        TransferHelper.safeTransfer(token, to, amount);
        emit Withdrawn(token, to, amount);
    }

    /// @notice Returns the treasury balance for a given token
    function getBalance(address token) external view override returns (uint256) {
        return _balances[token];
    }

    function _authorizeUpgrade(address) internal override onlyRole(DEFAULT_ADMIN_ROLE) {}
}
