// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../base/Initializable.sol";
import "../base/UUPSUpgradeable.sol";
import "../base/AccessControl.sol";
import "../interfaces/IEventEmitter.sol";

/// @title OpticalRegistry
/// @notice Trust-signaling registry for optical plugins.
/// @dev UUPS proxy. Opticals do NOT require registration to function —
///      but unregistered opticals show as "unverified" to users/frontends.
///      Admin registers/deregisters opticals with metadata.
contract OpticalRegistry is Initializable, UUPSUpgradeable, AccessControl {
    error ZeroAddress();
    error AlreadyRegistered();
    error NotRegistered();

    struct OpticalMetadata {
        string name;
        string description;
        uint8 riskLevel;     // 1-5 scale
        string auditor;      // auditor name or "unaudited"
        uint256 registeredAt;
    }

    event OpticalRegistered(address indexed optical, string name, uint8 riskLevel, uint256 timestamp);
    event OpticalDeregistered(address indexed optical, uint256 timestamp);
    event OpticalMetadataUpdated(address indexed optical, uint256 timestamp);

    mapping(address => bool) private _approved;
    mapping(address => OpticalMetadata) private _metadata;
    address[] private _allOpticals;

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

    /// @notice Register an optical as approved with metadata
    /// @param optical The optical contract address
    /// @param name Human-readable name
    /// @param description Description of the optical's behavior
    /// @param riskLevel Risk level 1-5 (1 = lowest)
    /// @param auditor Auditor name or "unaudited"
    function registerOptical(
        address optical,
        string calldata name,
        string calldata description,
        uint8 riskLevel,
        string calldata auditor
    ) external onlyRole(DEFAULT_ADMIN_ROLE) {
        if (optical == address(0)) revert ZeroAddress();
        if (_approved[optical]) revert AlreadyRegistered();

        _approved[optical] = true;
        _metadata[optical] = OpticalMetadata({
            name: name,
            description: description,
            riskLevel: riskLevel,
            auditor: auditor,
            registeredAt: block.timestamp
        });
        _allOpticals.push(optical);

        emit OpticalRegistered(optical, name, riskLevel, block.timestamp);
    }

    /// @notice Deregister an optical (mark as not approved)
    /// @param optical The optical contract address to deregister
    function deregisterOptical(address optical) external onlyRole(DEFAULT_ADMIN_ROLE) {
        if (!_approved[optical]) revert NotRegistered();
        _approved[optical] = false;

        emit OpticalDeregistered(optical, block.timestamp);
    }

    /// @notice Update metadata for a registered optical
    function updateMetadata(
        address optical,
        string calldata name,
        string calldata description,
        uint8 riskLevel,
        string calldata auditor
    ) external onlyRole(DEFAULT_ADMIN_ROLE) {
        if (!_approved[optical]) revert NotRegistered();

        _metadata[optical] = OpticalMetadata({
            name: name,
            description: description,
            riskLevel: riskLevel,
            auditor: auditor,
            registeredAt: _metadata[optical].registeredAt
        });

        emit OpticalMetadataUpdated(optical, block.timestamp);
    }

    /// @notice Check if an optical is registered and approved
    function isRegistered(address optical) external view returns (bool) {
        return _approved[optical];
    }

    /// @notice Get metadata for an optical
    function getOpticalMetadata(address optical) external view returns (OpticalMetadata memory) {
        return _metadata[optical];
    }

    /// @notice Get all registered opticals (paginated)
    function getAllOpticals(uint256 offset, uint256 limit) external view returns (address[] memory) {
        uint256 total = _allOpticals.length;
        if (offset >= total) {
            return new address[](0);
        }

        uint256 end = offset + limit;
        if (end > total) {
            end = total;
        }

        uint256 resultLength = end - offset;
        address[] memory result = new address[](resultLength);
        for (uint256 i = 0; i < resultLength; i++) {
            result[i] = _allOpticals[offset + i];
        }
        return result;
    }

    /// @notice Get total count of ever-registered opticals
    function getOpticalCount() external view returns (uint256) {
        return _allOpticals.length;
    }

    function _authorizeUpgrade(address) internal override onlyRole(DEFAULT_ADMIN_ROLE) {}
}
