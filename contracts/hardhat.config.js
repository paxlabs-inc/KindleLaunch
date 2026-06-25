require("@nomicfoundation/hardhat-toolbox");
require("dotenv/config");
require("solidity-docgen");

const optionalPlugins = [
  "hardhat-gas-reporter",
  "solidity-coverage",
  "slither",
  "hardhat-deploy",
  "hardhat-ethers",
  "hardhat-waffle",
  "hardhat-contract-sizer",
  "hardhat-abi-exporter",
];

for (const plugin of optionalPlugins) {
  try {
    require(plugin);
  } catch (error) {
    if (error.code !== "MODULE_NOT_FOUND") {
      throw error;
    }
  }
}
// Retrieve the private key and API keys from the .env file
const privateKey = process.env.PRIVATE_KEY;


// Check if the private key is set
if (!privateKey) {
  console.warn("🚨 WARNING: PRIVATE_KEY is not set in the .env file. Deployments will not be possible.");
}

/** @type import('hardhat/config').HardhatUserConfig */
module.exports = {
  solidity: {
    version: "0.8.27",
    settings: {
      optimizer: {
        enabled: true,
        runs: 200,
      },
      viaIR: true,
      outputSelection: {
        "*": {
          "*": [
            "abi",
            "evm.bytecode",
            "evm.deployedBytecode",
            "evm.methodIdentifiers",
            "metadata",
            "storageLayout",
          ],
          "": ["ast"],
        },
      },
    },
  },
  gasReporter: {
    enabled: process.env.REPORT_GAS === "true",
    currency: "USD",
  },
  networks: {
    'paxeer-network': {
      url: '',
      accounts: privateKey ? [privateKey] : [],
    },
    localhost: {
      url: 'http://127.0.0.1:8545',
      // Intentionally no accounts override — falls through to the hardhat
      // node's default 20 accounts (each funded with 10,000 ETH). Account #0
      // () is the deployer recorded
      // in deployments/localhost-addresses.json, so re-deploys stay idempotent.
      chainId: 31337,
    },
  },
  etherscan: {
    apiKey: {
      'paxeer-network': 'empty'
    },
    customChains: [
      {
        network: "paxeer-network",
        chainId: 125,
        urls: {
          apiURL: "",
          browserURL: ""
        }
      }
    ] 
  },
  docgen: {
    path: "docs",
    clear: true,
    runOnCompile: true,
    except: ["test/**", "mocks/**", "lib/**"],
    pages: "files",
    template: "hardhat",
    outputDir: "docs",
  },
};