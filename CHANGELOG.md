# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.3.0] - 2026-02-09

### Added

- Embedded dashboard UI served on `:8082` with Tailwind CSS dark theme and auto-refresh
- API endpoints (`/api/overview`, `/api/schedules`, `/api/idle-detectors`) for schedule visibility
- Helm chart: dashboard service, optional ingress, network policy
- SlumlordBinPacker CRD and controller for node consolidation (bin packing)
- Cluster-scoped resource with optional namespace filter and node selector
- Two modes: `report` (analyze only) and `consolidate` (evict pods via Eviction API)
- Requests-based node utilization analysis (no metrics-server dependency)
- Pod placement simulation with taint/toleration and nodeSelector matching
- Protections: skip DaemonSet, mirror, orphan, hostPath, system, and terminating pods
- PDB enforcement via Kubernetes Eviction API
- Optional time window for consolidation (reuses SleepWindow pattern)
- DryRun mode for safe plan preview
- MaxEvictionsPerCycle cap for controlled rollouts
- Helm chart integration with `binPacker.enabled` toggle and conditional RBAC
- Comprehensive tests: utilization calculation, evictability, taint matching, simulation, schedule window

## [2.2.0] - 2026-02-08

### Added

- Metrics-server integration for idle detection (CPU/memory usage via `k8s.io/metrics` PodMetrics API)
- `getPodsForWorkload` helper to resolve Deployment/StatefulSet pod selectors
- `computeUsagePercent` aggregates CPU/memory usage vs requests across all pods
- CronJob idle detection via active Jobs' running pod metrics
- Graceful degradation: nil MetricsClient runs in safe mode (always returns not-idle)
- RBAC permissions for pods and jobs resources
- Unit tests for `computeUsagePercent` and `isIdleByThresholds`
- Integration tests for `checkWorkloadMetrics` and `checkCronJobMetrics` with fake metrics client

### Changed

- Replaced stub `checkWorkloadMetrics` and `checkCronJobMetrics` with real metrics-server queries
- Added `MetricsClient` field to `IdleDetectorReconciler`
- Wired metrics clientset creation in `cmd/main.go`

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

[Unreleased]: https://github.com/cschockaert/slumlord/compare/2.3.0...HEAD
[2.3.0]: https://github.com/cschockaert/slumlord/compare/2.2.0...2.3.0
[2.2.0]: https://github.com/cschockaert/slumlord/compare/2.1.0...2.2.0
[2.1.0]: https://github.com/cschockaert/slumlord/compare/2.0.1...2.1.0
[2.0.1]: https://github.com/cschockaert/slumlord/compare/2.0.0...2.0.1
[2.0.0]: https://github.com/cschockaert/slumlord/compare/1.0.0...2.0.0
[1.0.0]: https://github.com/cschockaert/slumlord/releases/tag/1.0.0
