# Changelog

All notable changes to this project are documented in this file. This changelog is
seeded from the repository's git history. Commit messages in the current history
are mostly timestamped checkpoints; as the project matures, this file should be
updated with human-readable release notes for each tagged version.

## [Unreleased]

### Added
- Go backend microservices for the KindleLaunch data and media planes.
- Solidity launchpad AMM (`contracts/`) with a six-layer architecture, virtual
  reserve pricing, programmable Opticals, and UUPS/beacon upgradeability.
- Continuous integration via `.github/workflows/ci.yml` (Go + Solidity + image
  publishing).
- Docker build pipeline (`deploy/Dockerfile.svc`, `deploy/deploy.sh`,
  `deploy/docker-compose.strangler.yml`).
- Integration kit (`contracts/integration-kit/`) with ABI and binding exports for
  JS/TS, Go, and Rust.
- Project documentation and source-of-truth spec in `knowledge/`.

## Initial Development — 2026-06-22 to 2026-06-25

The project bootstrapped from `first commit` on 2026-06-22 and reached its current
shape through rapid checkpoint commits on 2026-06-25. The git history for this
period is listed below.

### 2026-06-25

- `d03e1d6` — 20260625T211934
- `201c06b` — 20260625T195435
- `32919cc` — 20260625T194159
- `a161676` — 20260625T193054
- `3c83984` — 20260625T192550
- `b441553` — 20260625T192341
- `d309b95` — 20260625T184825
- `5f50892` — 20260625T182606
- `52e6cba` — 20260625T180807
- `7e8454e` — 20260625T180402
- `66795e9` — 20260625T175737
- `c0fb864` — 20260625T175103
- `13b218d` — 20260625T174612
- `83411d2` — 20260625T174400
- `c1d9a9f` — 20260625T174034
- `d1b49ba` — 20260625T171944
- `c92c66a` — 20260625T161924
- `5fca61a` — 20260625T151837
- `0aa2ef1` — 20260625T135059
- `c740800` — 20260625T131757
- `4908d2d` — 20260625T010110

### 2026-06-24

- `cd4eec7` — 20260624T200020

### 2026-06-22

- `8ca0e8a` — 20260622T024740
- `4a02fd1` — 20260622T022042
- `d5ce815` — 20260622T014254
- `e4df095` — 20260622T010943
- `37b358a` — 20260622T004700
- `5c5ed4d` — first commit
