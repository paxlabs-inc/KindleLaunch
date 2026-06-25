// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

/// @title Timelock
/// @notice Trust anchor. Enforces delay on all governance-approved transactions.
/// @dev IMMUTABLE — not upgradeable. This is the security root.
contract Timelock {
    uint256 public immutable minDelay;
    address public proposer;
    address public guardian;

    mapping(bytes32 => bool) public queuedTransactions;

    error DelayNotMet();
    error TransactionNotQueued();
    error TransactionAlreadyQueued();
    error Unauthorized();
    error ExecutionFailed();
    error ZeroAddress();
    error InvalidDelay();

    event TransactionQueued(
        bytes32 indexed txHash,
        address indexed target,
        uint256 value,
        bytes data,
        uint256 eta
    );
    event TransactionExecuted(
        bytes32 indexed txHash,
        address indexed target,
        uint256 value,
        bytes data,
        uint256 eta
    );
    event TransactionCancelled(bytes32 indexed txHash);
    event ProposerChanged(address indexed oldProposer, address indexed newProposer);
    event GuardianChanged(address indexed oldGuardian, address indexed newGuardian);

    modifier onlyProposer() {
        if (msg.sender != proposer) revert Unauthorized();
        _;
    }

    modifier onlyGuardian() {
        if (msg.sender != guardian) revert Unauthorized();
        _;
    }

    constructor(uint256 _minDelay, address _proposer, address _guardian) {
        if (_minDelay == 0) revert InvalidDelay();
        if (_proposer == address(0)) revert ZeroAddress();
        if (_guardian == address(0)) revert ZeroAddress();

        minDelay = _minDelay;
        proposer = _proposer;
        guardian = _guardian;
    }

    /// @notice Queue a transaction for future execution
    function queueTransaction(
        address target,
        uint256 value,
        bytes calldata data,
        uint256 eta
    ) external onlyProposer returns (bytes32 txHash) {
        if (eta < block.timestamp + minDelay) revert DelayNotMet();

        txHash = _getTxHash(target, value, data, eta);
        if (queuedTransactions[txHash]) revert TransactionAlreadyQueued();

        queuedTransactions[txHash] = true;
        emit TransactionQueued(txHash, target, value, data, eta);
    }

    /// @notice Execute a queued transaction after its delay has passed
    function executeTransaction(
        address target,
        uint256 value,
        bytes calldata data,
        uint256 eta
    ) external returns (bytes memory result) {
        bytes32 txHash = _getTxHash(target, value, data, eta);
        if (!queuedTransactions[txHash]) revert TransactionNotQueued();
        if (block.timestamp < eta) revert DelayNotMet();

        queuedTransactions[txHash] = false;

        (bool success, bytes memory returnData) = target.call{value: value}(data);
        if (!success) revert ExecutionFailed();

        emit TransactionExecuted(txHash, target, value, data, eta);
        return returnData;
    }

    /// @notice Cancel a queued transaction (guardian only)
    function cancelTransaction(
        address target,
        uint256 value,
        bytes calldata data,
        uint256 eta
    ) external onlyGuardian {
        bytes32 txHash = _getTxHash(target, value, data, eta);
        if (!queuedTransactions[txHash]) revert TransactionNotQueued();

        queuedTransactions[txHash] = false;
        emit TransactionCancelled(txHash);
    }

    /// @notice Change the proposer address (only current proposer can change)
    function setProposer(address newProposer) external onlyProposer {
        if (newProposer == address(0)) revert ZeroAddress();
        address old = proposer;
        proposer = newProposer;
        emit ProposerChanged(old, newProposer);
    }

    /// @notice Change the guardian address (only current guardian can change)
    function setGuardian(address newGuardian) external onlyGuardian {
        if (newGuardian == address(0)) revert ZeroAddress();
        address old = guardian;
        guardian = newGuardian;
        emit GuardianChanged(old, newGuardian);
    }

    function getMinDelay() external view returns (uint256) {
        return minDelay;
    }

    function _getTxHash(
        address target,
        uint256 value,
        bytes calldata data,
        uint256 eta
    ) internal pure returns (bytes32) {
        return keccak256(abi.encode(target, value, data, eta));
    }

    receive() external payable {}
}
