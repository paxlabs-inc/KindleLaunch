// SPDX-License-Identifier: LicenseRef-Paxlabs-HyperPax-OS-Protocol
pragma solidity ^0.8.24;

import "./SidioraMath.sol";

/// @title FeeLib
/// @notice Dynamic fee calculation for Sidiora pools
/// @dev feeBps = baseFee + ageFactor + volatilityFactor + concentrationFactor
///      Clamped to [minFeeBps, maxFeeBps]
library FeeLib {
    /// @notice Calculates the dynamic fee for a swap
    /// @param baseFee Base fee in basis points
    /// @param minFee Minimum fee in basis points
    /// @param maxFee Maximum fee in basis points
    /// @param feeDecayRate Controls how fast age factor decays
    /// @param volatilityWeight Weight of volatility component
    /// @param concentrationWeight Weight of concentration component
    /// @param poolAgeSeconds Age of pool in seconds
    /// @param volatility Price standard deviation (scaled by 1e18)
    /// @param topHolderBps Top holder's percentage in basis points (0-10000)
    /// @return feeBps The calculated fee in basis points
    function calculateDynamicFee(
        uint256 baseFee,
        uint256 minFee,
        uint256 maxFee,
        uint256 feeDecayRate,
        uint256 volatilityWeight,
        uint256 concentrationWeight,
        uint256 poolAgeSeconds,
        uint256 volatility,
        uint256 topHolderBps
    ) internal pure returns (uint256 feeBps) {
        uint256 age = calculateAgeFactor(feeDecayRate, poolAgeSeconds);
        uint256 vol = calculateVolatilityFactor(volatilityWeight, volatility);
        uint256 conc = calculateConcentrationFactor(concentrationWeight, topHolderBps);

        feeBps = baseFee + age + vol + conc;

        // Clamp to [minFee, maxFee]
        if (feeBps < minFee) {
            feeBps = minFee;
        } else if (feeBps > maxFee) {
            feeBps = maxFee;
        }
    }

    /// @notice Age factor: higher fee for younger pools, decays over time
    /// @dev ageFactor = feeDecayRate / (1 + poolAgeInHours)
    /// @param feeDecayRate Decay rate parameter (in bps)
    /// @param poolAgeSeconds Pool age in seconds
    /// @return factor Age-based fee component in basis points
    function calculateAgeFactor(
        uint256 feeDecayRate,
        uint256 poolAgeSeconds
    ) internal pure returns (uint256 factor) {
        uint256 poolAgeHours = poolAgeSeconds / 3600;
        factor = feeDecayRate / (1 + poolAgeHours);
    }

    /// @notice Volatility factor: higher fee when price swings are large
    /// @dev volatilityFactor = volatilityWeight * volatility / 1e6
    /// @param volatilityWeight Weight parameter
    /// @param volatility Price std dev scaled by 1e6
    /// @return factor Volatility-based fee component in basis points
    function calculateVolatilityFactor(
        uint256 volatilityWeight,
        uint256 volatility
    ) internal pure returns (uint256 factor) {
        if (volatility == 0) return 0;
        factor = SidioraMath.mulDiv(volatilityWeight, volatility, 1e6);
    }

    /// @notice Concentration factor: higher fee when whale dominates
    /// @dev concentrationFactor = concentrationWeight * topHolderBps / 10000
    /// @param concentrationWeight Weight parameter
    /// @param topHolderBps Top holder's share in basis points
    /// @return factor Concentration-based fee component in basis points
    function calculateConcentrationFactor(
        uint256 concentrationWeight,
        uint256 topHolderBps
    ) internal pure returns (uint256 factor) {
        if (topHolderBps == 0) return 0;
        factor = (concentrationWeight * topHolderBps) / 10000;
    }

    /// @notice Calculates price volatility from a snapshot buffer
    /// @dev Computes standard deviation of price differences
    /// @param snapshots Array of price snapshots (up to 8)
    /// @param count Number of valid snapshots
    /// @return volatility Standard deviation scaled by 1e6
    function calculateVolatility(
        uint256[8] memory snapshots,
        uint256 count
    ) internal pure returns (uint256 volatility) {
        if (count < 2) return 0;

        // Calculate mean of absolute price changes
        uint256 sumChanges = 0;
        uint256 changes = 0;
        for (uint256 i = 1; i < count; i++) {
            if (snapshots[i] > snapshots[i - 1]) {
                sumChanges += snapshots[i] - snapshots[i - 1];
            } else {
                sumChanges += snapshots[i - 1] - snapshots[i];
            }
            changes++;
        }
        if (changes == 0) return 0;

        uint256 meanChange = sumChanges / changes;

        // Calculate variance (mean of squared deviations from mean)
        uint256 sumSquaredDev = 0;
        for (uint256 i = 1; i < count; i++) {
            uint256 change;
            if (snapshots[i] > snapshots[i - 1]) {
                change = snapshots[i] - snapshots[i - 1];
            } else {
                change = snapshots[i - 1] - snapshots[i];
            }
            if (change > meanChange) {
                uint256 dev = change - meanChange;
                sumSquaredDev += SidioraMath.mulDiv(dev, dev, 1e6);
            } else {
                uint256 dev = meanChange - change;
                sumSquaredDev += SidioraMath.mulDiv(dev, dev, 1e6);
            }
        }

        uint256 variance = sumSquaredDev / changes;
        volatility = SidioraMath.sqrt(variance * 1e6);
    }
}
