# Security Policy

This document describes how to report security issues for KindleLaunch and what
you can expect after reporting.

## Supported versions

Only the latest commit on the `main` branch and the deployed Paxeer mainnet
contracts are actively supported with security updates. Older checkpoints are not
backported.

| Component | Supported |
|-----------|-----------|
| `main` branch | Yes |
| Released Docker images (`ghcr.io/sidiora-technologies/kindlelaunch-*`) | Latest tag only |
| Deployed Paxeer mainnet contracts | Yes |
| Localhost / testnet deployments | No |

## Reporting a vulnerability

If you believe you have found a security vulnerability in the smart contracts,
backend services, or any part of the repository, please report it privately.

**Do not open a public issue or pull request for security vulnerabilities.**

Email the security team at:

- **security@sidiora.app** (preferred)

Please include:

- A clear description of the vulnerability and its impact.
- Steps to reproduce, or a proof-of-concept if you have one.
- The component(s) affected (contracts, Go services, deployment, etc.).
- Any suggested mitigation or fix.

## Response process

1. **Acknowledgment** — within 72 hours of receiving a complete report, we will
   acknowledge it and confirm receipt.
2. **Investigation** — we will assess the report, assign a severity, and work on
   a fix.
3. **Disclosure** — once a fix is deployed, we will coordinate public disclosure
   with you, credit you if you wish, and publish a security advisory in the
   changelog.

## Scope

In scope:

- Smart contracts under `contracts/contracts/`.
- Go backend services under `core/`, `media/`, `protocol/`, and `shared/`.
- Deployment and configuration under `deploy/` and `.github/workflows/`.
- Authentication and authorization logic (EIP-191, webhook HMAC, API keys).

Out of scope:

- Third-party infrastructure (Paxeer chain, Redis, Postgres, Cloudflare R2) unless
  the issue is caused by our configuration or code.
- Social engineering or phishing targeting individual contributors.

## Safe harbor

We will not take legal action against researchers who act in good faith, follow
this policy, and do not exploit or publicly disclose vulnerabilities before we
have had a reasonable time to fix them.

## Security-related configuration

- Production RPC endpoints, private keys, and API credentials must never be
  committed. Use `.env` files (ignored by git) and a secrets manager in
  production.
- All contract upgrades are governed by `Timelock` with a 48-hour delay.
- No EOA admin key holds direct upgrade power.
