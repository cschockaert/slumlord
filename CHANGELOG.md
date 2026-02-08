# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.1.0] - 2026-02-08

### Added

- SlumlordIdleDetector CRD and controller for detecting and scaling down idle workloads
- Two modes: `alert` (report only) and `scale` (auto-scale to zero after idle duration)
- MatchNames wildcard selector support (e.g., `prod-*`)
- CRD validation markers (idleDuration pattern, thresholds 0-100)
- Finalizer ensures workloads are restored on detector deletion
- Comprehensive tests: MatchNames filtering, finalizer deletion, partial restore, name-only selector

### Fixed

- Idle detector: persist status after each scale-down to prevent data loss on partial failure
- Idle detector: restore only clears successfully restored workloads, failed ones retained for retry

### Changed

- Bump actions/checkout from 4 to 6
- Bump actions/setup-go from 5 to 6
- Bump sigs.k8s.io/controller-runtime from 0.17.0 to 0.23.1

## [2.0.1] - 2026-02-08

### Fixed

- Upgrade Go from 1.22 to 1.25 and patch vulnerable dependencies
- Upgrade golangci-lint to v2.8.0 and make govulncheck advisory
- Remove deprecated govet shadow setting for golangci-lint v2

### Changed

- Bump golang Docker image from 1.22-alpine to 1.25-alpine
- Bump aquasecurity/trivy-action from 0.28.0 to 0.33.1
- Bump golangci/golangci-lint-action from 6 to 9
- Bump github/codeql-action from 3 to 4

## [2.0.0] - 2026-02-08

### Added

- Quality hardening: CI/CD, Helm, docs, tests, and bug fixes

### Breaking Changes

- Major CI/CD and project structure overhaul

## [1.0.0] - 2026-02-07

### Added

- Initial release of Slumlord operator
- SlumlordSleepSchedule CRD for defining sleep windows
- Support for Deployments, StatefulSets, and CronJobs
- CNPG Cluster and FluxCD HelmRelease/Kustomization sleep support
- Helm chart with OCI artifact push to GHCR
- DAYS column in kubectl get output
- Controller tests and CI/CD pipeline
- Timezone-aware scheduling with overnight schedule support

[Unreleased]: https://github.com/cschockaert/slumlord/compare/2.1.0...HEAD
[2.1.0]: https://github.com/cschockaert/slumlord/compare/2.0.1...2.1.0
[2.0.1]: https://github.com/cschockaert/slumlord/compare/2.0.0...2.0.1
[2.0.0]: https://github.com/cschockaert/slumlord/compare/1.0.0...2.0.0
[1.0.0]: https://github.com/cschockaert/slumlord/releases/tag/1.0.0
