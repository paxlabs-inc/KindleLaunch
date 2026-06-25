//! Auto-generated ABI for IPoolRegistry
//!
//! Usage with alloy:
//!   alloy::sol! { IPoolRegistry, "i_pool_registry.json" }
//!
//! Or parse at runtime:
//!   let abi: alloy_json_abi::JsonAbi = serde_json::from_str(I_POOL_REGISTRY_ABI).unwrap();

/// Raw ABI JSON for IPoolRegistry
pub const I_POOL_REGISTRY_ABI: &str = r##"
[
    {
        "inputs": [],
        "name": "DuplicateToken",
        "type": "error"
    },
    {
        "inputs": [],
        "name": "PoolNotFound",
        "type": "error"
    },
    {
        "inputs": [],
        "name": "Unauthorized",
        "type": "error"
    },
    {
        "inputs": [],
        "name": "ZeroAddress",
        "type": "error"
    },
    {
        "anonymous": false,
        "inputs": [
            {
                "indexed": true,
                "internalType": "address",
                "name": "pool",
                "type": "address"
            },
            {
                "indexed": true,
                "internalType": "address",
                "name": "token",
                "type": "address"
            },
            {
                "indexed": true,
                "internalType": "address",
                "name": "creator",
                "type": "address"
            },
            {
                "indexed": false,
                "internalType": "address",
                "name": "optical",
                "type": "address"
            },
            {
                "indexed": false,
                "internalType": "uint256",
                "name": "nftId",
                "type": "uint256"
            },
            {
                "indexed": false,
                "internalType": "uint256",
                "name": "timestamp",
                "type": "uint256"
            }
        ],
        "name": "PoolRegistered",
        "type": "event"
    },
    {
        "inputs": [
            {
                "internalType": "uint256",
                "name": "offset",
                "type": "uint256"
            },
            {
                "internalType": "uint256",
                "name": "limit",
                "type": "uint256"
            }
        ],
        "name": "getAllPools",
        "outputs": [
            {
                "internalType": "address[]",
                "name": "",
                "type": "address[]"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [
            {
                "internalType": "address",
                "name": "pool",
                "type": "address"
            }
        ],
        "name": "getNftIdByPool",
        "outputs": [
            {
                "internalType": "uint256",
                "name": "",
                "type": "uint256"
            }
        ],
        "stateMutability": "view",
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
        "name": "getPoolByToken",
        "outputs": [
            {
                "internalType": "address",
                "name": "",
                "type": "address"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [],
        "name": "getPoolCount",
        "outputs": [
            {
                "internalType": "uint256",
                "name": "",
                "type": "uint256"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [
            {
                "internalType": "address",
                "name": "pool",
                "type": "address"
            }
        ],
        "name": "getPoolMetadata",
        "outputs": [
            {
                "components": [
                    {
                        "internalType": "address",
                        "name": "creator",
                        "type": "address"
                    },
                    {
                        "internalType": "address",
                        "name": "token",
                        "type": "address"
                    },
                    {
                        "internalType": "address",
                        "name": "optical",
                        "type": "address"
                    },
                    {
                        "internalType": "uint256",
                        "name": "nftId",
                        "type": "uint256"
                    },
                    {
                        "internalType": "uint256",
                        "name": "createdAt",
                        "type": "uint256"
                    },
                    {
                        "internalType": "uint256",
                        "name": "createdBlock",
                        "type": "uint256"
                    }
                ],
                "internalType": "struct IPoolRegistry.PoolMetadata",
                "name": "",
                "type": "tuple"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [
            {
                "internalType": "address",
                "name": "creator",
                "type": "address"
            }
        ],
        "name": "getPoolsByCreator",
        "outputs": [
            {
                "internalType": "address[]",
                "name": "",
                "type": "address[]"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [
            {
                "internalType": "address",
                "name": "pool",
                "type": "address"
            }
        ],
        "name": "isRegisteredPool",
        "outputs": [
            {
                "internalType": "bool",
                "name": "",
                "type": "bool"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    },
    {
        "inputs": [
            {
                "internalType": "address",
                "name": "pool",
                "type": "address"
            },
            {
                "internalType": "address",
                "name": "token",
                "type": "address"
            },
            {
                "internalType": "address",
                "name": "creator",
                "type": "address"
            },
            {
                "internalType": "address",
                "name": "optical",
                "type": "address"
            },
            {
                "internalType": "uint256",
                "name": "nftId",
                "type": "uint256"
            }
        ],
        "name": "register",
        "outputs": [],
        "stateMutability": "nonpayable",
        "type": "function"
    }
]
"##;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_valid_json() {
        let v: serde_json::Value = serde_json::from_str(I_POOL_REGISTRY_ABI).unwrap();
        assert!(v.is_array());
    }
}
