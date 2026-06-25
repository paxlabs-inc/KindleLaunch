// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "../base/Initializable.sol";
import "../base/UUPSUpgradeable.sol";
import "../base/AccessControl.sol";

/// @title GovernanceModule
/// @notice Decentralized decision-making for protocol upgrades and parameter changes
/// @dev Dual mode: Admin bypass (initial) + full SID governance (after transition).
///      Proposals that pass are queued to the Timelock for delayed execution.
contract GovernanceModule is Initializable, UUPSUpgradeable, AccessControl {
    bytes32 public constant ADMIN_ROLE = keccak256("ADMIN_ROLE");

    enum ProposalState { Pending, Active, Defeated, Succeeded, Queued, Executed, Cancelled }

    struct Proposal {
        address proposer;
        address[] targets;
        uint256[] values;
        bytes[] calldatas;
        string description;
        uint256 startBlock;
        uint256 endBlock;
        uint256 forVotes;
        uint256 againstVotes;
        bool executed;
        bool cancelled;
    }

    address public votingToken; // SID token
    address public timelock;

    uint256 public proposalThreshold; // min SID balance to propose
    uint256 public votingPeriod; // blocks
    uint256 public quorumVotes; // min votes for quorum

    uint256 public proposalCount;
    mapping(uint256 => Proposal) public proposals;
    mapping(uint256 => mapping(address => bool)) public hasVoted;
    mapping(uint256 => mapping(address => uint256)) public voteSnapshot; // snapshot of voter balance

    bool public adminModeActive; // true = admin can bypass governance

    error BelowProposalThreshold();
    error AlreadyVoted();
    error VotingClosed();
    error QuorumNotMet();
    error ProposalNotPassed();
    error ProposalAlreadyExecuted();
    error ProposalIsCancelled();
    error InvalidProposal();
    error Unauthorized();
    error AdminModeNotActive();
    error ArrayLengthMismatch();

    event ProposalCreated(uint256 indexed proposalId, address indexed proposer, string description);
    event VoteCast(uint256 indexed proposalId, address indexed voter, bool support, uint256 weight);
    event ProposalExecuted(uint256 indexed proposalId);
    event ProposalCancelled(uint256 indexed proposalId);
    event AdminModeDeactivated();

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(
        address _votingToken,
        address _timelock,
        address _admin,
        uint256 _proposalThreshold,
        uint256 _votingPeriod,
        uint256 _quorumVotes
    ) external initializer {
        votingToken = _votingToken;
        timelock = _timelock;
        proposalThreshold = _proposalThreshold;
        votingPeriod = _votingPeriod;
        quorumVotes = _quorumVotes;
        adminModeActive = true;

        _grantRole(DEFAULT_ADMIN_ROLE, _admin);
        _grantRole(ADMIN_ROLE, _admin);
    }

    /// @notice Create a proposal (requires SID balance >= proposalThreshold)
    function propose(
        address[] calldata targets,
        uint256[] calldata values,
        bytes[] calldata calldatas,
        string calldata description
    ) external returns (uint256 proposalId) {
        if (targets.length == 0) revert InvalidProposal();
        if (targets.length != values.length || targets.length != calldatas.length) revert ArrayLengthMismatch();

        uint256 balance = _getVotingPower(msg.sender);
        if (balance < proposalThreshold) revert BelowProposalThreshold();

        proposalId = ++proposalCount;
        Proposal storage p = proposals[proposalId];
        p.proposer = msg.sender;
        p.targets = targets;
        p.values = values;
        p.calldatas = calldatas;
        p.description = description;
        p.startBlock = block.number + 1;
        p.endBlock = block.number + 1 + votingPeriod;

        emit ProposalCreated(proposalId, msg.sender, description);
    }

    /// @notice Vote on a proposal
    function castVote(uint256 proposalId, bool support) external {
        Proposal storage p = proposals[proposalId];
        if (block.number < p.startBlock || block.number > p.endBlock) revert VotingClosed();
        if (p.cancelled) revert ProposalIsCancelled();
        if (hasVoted[proposalId][msg.sender]) revert AlreadyVoted();

        uint256 weight = _getVotingPower(msg.sender);
        hasVoted[proposalId][msg.sender] = true;
        voteSnapshot[proposalId][msg.sender] = weight;

        if (support) {
            p.forVotes += weight;
        } else {
            p.againstVotes += weight;
        }

        emit VoteCast(proposalId, msg.sender, support, weight);
    }

    /// @notice Execute a passed proposal (queues it through Timelock)
    function execute(uint256 proposalId, uint256 eta) external {
        Proposal storage p = proposals[proposalId];
        if (p.executed) revert ProposalAlreadyExecuted();
        if (p.cancelled) revert ProposalIsCancelled();
        if (block.number <= p.endBlock) revert VotingClosed();
        if (p.forVotes < quorumVotes) revert QuorumNotMet();
        if (p.forVotes <= p.againstVotes) revert ProposalNotPassed();

        p.executed = true;

        // Queue each action through Timelock
        for (uint256 i = 0; i < p.targets.length; i++) {
            ITimelockQueue(timelock).queueTransaction(
                p.targets[i], p.values[i], p.calldatas[i], eta
            );
        }

        emit ProposalExecuted(proposalId);
    }

    /// @notice Cancel a proposal (only proposer or admin)
    function cancel(uint256 proposalId) external {
        Proposal storage p = proposals[proposalId];
        if (msg.sender != p.proposer && !hasRole(ADMIN_ROLE, msg.sender)) revert Unauthorized();
        if (p.executed) revert ProposalAlreadyExecuted();

        p.cancelled = true;
        emit ProposalCancelled(proposalId);
    }

    /// @notice Admin bypasses governance and queues directly to Timelock
    /// @dev Only works while adminModeActive is true
    function adminExecute(
        address target,
        uint256 value,
        bytes calldata data,
        uint256 eta
    ) external onlyRole(ADMIN_ROLE) {
        if (!adminModeActive) revert AdminModeNotActive();
        ITimelockQueue(timelock).queueTransaction(target, value, data, eta);
    }

    /// @notice Permanently deactivate admin mode (irreversible)
    function deactivateAdminMode() external onlyRole(ADMIN_ROLE) {
        adminModeActive = false;
        emit AdminModeDeactivated();
    }

    /// @notice Get the state of a proposal
    function getProposalState(uint256 proposalId) external view returns (ProposalState) {
        Proposal storage p = proposals[proposalId];
        if (p.cancelled) return ProposalState.Cancelled;
        if (p.executed) return ProposalState.Executed;
        if (block.number < p.startBlock) return ProposalState.Pending;
        if (block.number <= p.endBlock) return ProposalState.Active;
        if (p.forVotes < quorumVotes || p.forVotes <= p.againstVotes) return ProposalState.Defeated;
        return ProposalState.Succeeded;
    }

    function _getVotingPower(address account) internal view returns (uint256) {
        (bool success, bytes memory data) = votingToken.staticcall(
            abi.encodeWithSelector(0x70a08231, account) // balanceOf(address)
        );
        if (!success || data.length < 32) return 0;
        return abi.decode(data, (uint256));
    }

    function _authorizeUpgrade(address) internal override onlyRole(DEFAULT_ADMIN_ROLE) {}
}

interface ITimelockQueue {
    function queueTransaction(address target, uint256 value, bytes calldata data, uint256 eta) external returns (bytes32);
}
