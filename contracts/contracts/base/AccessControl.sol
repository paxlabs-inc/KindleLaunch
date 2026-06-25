// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

/// @title AccessControl
/// @notice Role-based access control with role admin hierarchy
/// @dev Roles are referred to by their bytes32 identifier.
///      DEFAULT_ADMIN_ROLE is the admin for all roles by default.
abstract contract AccessControl {
    bytes32 public constant DEFAULT_ADMIN_ROLE = 0x00;

    struct RoleData {
        mapping(address => bool) hasRole;
        bytes32 adminRole;
    }

    mapping(bytes32 => RoleData) private _roles;

    error MissingRole(address account, bytes32 role);

    event RoleGranted(bytes32 indexed role, address indexed account, address indexed sender);
    event RoleRevoked(bytes32 indexed role, address indexed account, address indexed sender);
    event RoleAdminChanged(bytes32 indexed role, bytes32 indexed previousAdminRole, bytes32 indexed newAdminRole);

    modifier onlyRole(bytes32 role) {
        _checkRole(role, msg.sender);
        _;
    }

    /// @notice Returns true if account has the given role
    function hasRole(bytes32 role, address account) public view returns (bool) {
        return _roles[role].hasRole[account];
    }

    /// @notice Returns the admin role for a given role
    function getRoleAdmin(bytes32 role) public view returns (bytes32) {
        return _roles[role].adminRole;
    }

    /// @notice Grants a role to an account. Caller must have the role's admin role.
    function grantRole(bytes32 role, address account) external onlyRole(getRoleAdmin(role)) {
        _grantRole(role, account);
    }

    /// @notice Revokes a role from an account. Caller must have the role's admin role.
    function revokeRole(bytes32 role, address account) external onlyRole(getRoleAdmin(role)) {
        _revokeRole(role, account);
    }

    /// @notice Account renounces a role from itself
    function renounceRole(bytes32 role, address callerConfirmation) external {
        if (callerConfirmation != msg.sender) revert MissingRole(msg.sender, role);
        _revokeRole(role, msg.sender);
    }

    function _checkRole(bytes32 role, address account) internal view {
        if (!hasRole(role, account)) revert MissingRole(account, role);
    }

    function _grantRole(bytes32 role, address account) internal returns (bool) {
        if (!hasRole(role, account)) {
            _roles[role].hasRole[account] = true;
            emit RoleGranted(role, account, msg.sender);
            return true;
        }
        return false;
    }

    function _revokeRole(bytes32 role, address account) internal returns (bool) {
        if (hasRole(role, account)) {
            _roles[role].hasRole[account] = false;
            emit RoleRevoked(role, account, msg.sender);
            return true;
        }
        return false;
    }

    function _setRoleAdmin(bytes32 role, bytes32 adminRole) internal {
        bytes32 previous = getRoleAdmin(role);
        _roles[role].adminRole = adminRole;
        emit RoleAdminChanged(role, previous, adminRole);
    }
}
