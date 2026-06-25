// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../base/ERC721Enumerable.sol";
import "../base/Initializable.sol";
import "../base/UUPSUpgradeable.sol";
import "../base/AccessControl.sol";
import "../interfaces/ISidioraNFT.sol";
import "../interfaces/IEventEmitter.sol";

/// @title SidioraNFT
/// @notice ERC721 representing fee rights for a pool. One NFT per pool.
/// @dev UUPS proxy singleton. Factory mints via MINTER_ROLE.
contract SidioraNFT is ISidioraNFT, ERC721Enumerable, Initializable, UUPSUpgradeable, AccessControl {
    bytes32 public constant MINTER_ROLE = keccak256("MINTER_ROLE");
    bytes32 public constant STRATEGY_SETTER_ROLE = keccak256("STRATEGY_SETTER_ROLE");

    uint256 public override nextTokenId;

    mapping(uint256 => uint8) private _feeStrategy;
    mapping(uint256 => address) private _poolAddress;

    address public eventEmitter;

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() ERC721Base("", "") {
        _disableInitializers();
    }

    function initialize(
        string calldata _name,
        string calldata _symbol,
        address _eventEmitter,
        address _admin
    ) external initializer {
        if (_admin == address(0)) revert ZeroAddress();
        name = _name;
        symbol = _symbol;
        eventEmitter = _eventEmitter;
        nextTokenId = 1; // Start from 1
        _grantRole(DEFAULT_ADMIN_ROLE, _admin);
    }

    /// @inheritdoc ISidioraNFT
    function mint(
        address to,
        address pool,
        uint8 strategy
    ) external override onlyRole(MINTER_ROLE) returns (uint256 tokenId) {
        if (to == address(0)) revert ZeroAddress();
        if (pool == address(0)) revert ZeroAddress();
        if (strategy > 3) revert InvalidStrategy();

        tokenId = nextTokenId++;
        _mint(to, tokenId);

        _feeStrategy[tokenId] = strategy;
        _poolAddress[tokenId] = pool;

        emit PoolNFTMinted(tokenId, to, pool);
    }

    /// @inheritdoc ISidioraNFT
    function getFeeStrategy(uint256 tokenId) external view override returns (uint8) {
        if (!_exists(tokenId)) revert TokenNotFound();
        return _feeStrategy[tokenId];
    }

    /// @inheritdoc ISidioraNFT
    function setFeeStrategy(uint256 tokenId, uint8 newStrategy) external override {
        if (!_exists(tokenId)) revert TokenNotFound();
        if (newStrategy > 3) revert InvalidStrategy();

        // Only token owner, approved, or STRATEGY_SETTER_ROLE can change
        if (!_isApprovedOrOwner(msg.sender, tokenId) && !hasRole(STRATEGY_SETTER_ROLE, msg.sender)) {
            revert NotApproved();
        }

        uint8 oldStrategy = _feeStrategy[tokenId];
        _feeStrategy[tokenId] = newStrategy;

        emit FeeStrategyChanged(tokenId, oldStrategy, newStrategy);
    }

    /// @inheritdoc ISidioraNFT
    function getPoolAddress(uint256 tokenId) external view override returns (address) {
        if (!_exists(tokenId)) revert TokenNotFound();
        return _poolAddress[tokenId];
    }

    /// @notice ERC165 override
    function supportsInterface(bytes4 interfaceId) public view virtual override returns (bool) {
        return ERC721Enumerable.supportsInterface(interfaceId);
    }

    function _authorizeUpgrade(address) internal override onlyRole(DEFAULT_ADMIN_ROLE) {}
}
