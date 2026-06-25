//! Auto-generated ABI for IDataFeedAdapter
//!
//! Usage with alloy:
//!   alloy::sol! { IDataFeedAdapter, "i_data_feed_adapter.json" }
//!
//! Or parse at runtime:
//!   let abi: alloy_json_abi::JsonAbi = serde_json::from_str(I_DATA_FEED_ADAPTER_ABI).unwrap();

/// Raw ABI JSON for IDataFeedAdapter
pub const I_DATA_FEED_ADAPTER_ABI: &str = r##"
[
    {
        "anonymous": false,
        "inputs": [
            {
                "indexed": true,
                "internalType": "bytes32",
                "name": "sourceId",
                "type": "bytes32"
            },
            {
                "indexed": true,
                "internalType": "address",
                "name": "token",
                "type": "address"
            },
            {
                "indexed": false,
                "internalType": "uint256",
                "name": "price",
                "type": "uint256"
            },
            {
                "indexed": false,
                "internalType": "uint256",
                "name": "confidence",
                "type": "uint256"
            }
        ],
        "name": "AdapterPriceServed",
        "type": "event"
    },
    {
        "anonymous": false,
        "inputs": [
            {
                "indexed": true,
                "internalType": "bytes32",
                "name": "sourceId",
                "type": "bytes32"
            },
            {
                "indexed": true,
                "internalType": "address",
                "name": "adapter",
                "type": "address"
            }
        ],
        "name": "AdapterRegistered",
        "type": "event"
    },
    {
        "inputs": [],
        "name": "adapterName",
        "outputs": [
            {
                "internalType": "string",
                "name": "name",
                "type": "string"
            }
        ],
        "stateMutability": "pure",
        "type": "function"
    },
    {
        "inputs": [
            {
                "internalType": "address",
                "name": "token",
                "type": "address"
            }
        ],
        "name": "getFeedPrice",
        "outputs": [
            {
                "components": [
                    {
                        "internalType": "uint256",
                        "name": "price",
                        "type": "uint256"
                    },
                    {
                        "internalType": "uint256",
                        "name": "timestamp",
                        "type": "uint256"
                    },
                    {
                        "internalType": "uint256",
                        "name": "confidence",
                        "type": "uint256"
                    },
                    {
                        "internalType": "bytes32",
                        "name": "sourceId",
                        "type": "bytes32"
                    }
                ],
                "internalType": "struct IDataFeedAdapter.FeedPrice",
                "name": "feed",
                "type": "tuple"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [
            {
                "internalType": "address[]",
                "name": "tokens",
                "type": "address[]"
            }
        ],
        "name": "getFeedPrices",
        "outputs": [
            {
                "components": [
                    {
                        "internalType": "uint256",
                        "name": "price",
                        "type": "uint256"
                    },
                    {
                        "internalType": "uint256",
                        "name": "timestamp",
                        "type": "uint256"
                    },
                    {
                        "internalType": "uint256",
                        "name": "confidence",
                        "type": "uint256"
                    },
                    {
                        "internalType": "bytes32",
                        "name": "sourceId",
                        "type": "bytes32"
                    }
                ],
                "internalType": "struct IDataFeedAdapter.FeedPrice[]",
                "name": "feeds",
                "type": "tuple[]"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [],
        "name": "getSupportedTokens",
        "outputs": [
            {
                "internalType": "address[]",
                "name": "tokens",
                "type": "address[]"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [],
        "name": "maxStaleness",
        "outputs": [
            {
                "internalType": "uint256",
                "name": "staleness",
                "type": "uint256"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [],
        "name": "sourceId",
        "outputs": [
            {
                "internalType": "bytes32",
                "name": "id",
                "type": "bytes32"
            }
        ],
        "stateMutability": "pure",
        "type": "function"
    },
    {
        "inputs": [
            {
                "internalType": "address",
                "name": "token",
                "type": "address"
            }
        ],
        "name": "supportsToken",
        "outputs": [
            {
                "internalType": "bool",
                "name": "supported",
                "type": "bool"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    }
]
"##;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_valid_json() {
        let v: serde_json::Value = serde_json::from_str(I_DATA_FEED_ADAPTER_ABI).unwrap();
        assert!(v.is_array());
    }
}
