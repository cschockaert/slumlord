# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.12.1] - 2026-02-18

### Changed

- Release workflow now triggers on `vX.Y.Z` tags (CI strips `v` for Docker/Helm versions)
- Bump golang Docker image from 1.25-alpine to 1.26-alpine
- Bump k8s.io/api, k8s.io/apimachinery, k8s.io/metrics from 0.35.0 to 0.35.1

## [2.12.0] - 2026-02-15

### Added

- Configurable reconciliation intervals per-resource via `spec.reconcileInterval` on all 4 CRDs
- Global reconcile interval override via CLI flags: `--sleep-reconcile-interval`, `--idle-reconcile-interval`, `--binpacker-reconcile-interval`, `--nodedrain-reconcile-interval`
- Helm values for reconciliation interval configuration (`reconcileIntervals.*`)
- Combined SleepSchedule + IdleDetector example (`config/samples/combined_example.yaml`)
- Performance Tuning section in documentation
- In-place resize example for IdleDetector with `reconcileInterval: 10m`

### Changed

- Controllers now accept per-resource reconcile interval (CRD spec) with fallback to global flag, then to built-in default
- Default reconciliation intervals are now staggered to avoid concurrent API server spikes: SleepSchedule=5m, IdleDetector=5m30s, BinPacker=6m, NodeDrainPolicy=6m30s

## [2.11.0] - 2026-02-12

### Added

- `action: resize` mode for SlumlordIdleDetector — right-sizes idle workload pod requests in-place using Kubernetes 1.33 In-Place Pod Resize API
- `spec.resize` configuration block with `bufferPercent` and `minRequests` (CPU/memory floor)
- `status.resizedWorkloads` tracking with original/current requests for restore
- Automatic restore of original requests when workloads are no longer idle or CR is deleted
- CronJob skip with warning when using resize action (no long-running pods to resize)
- RBAC permission for pods/patch (in-place resize)

## [2.10.0] - 2026-02-12

### Changed

- Default logging to production mode (JSON encoder, info level) instead of development mode
- Helm chart now exposes `log.level`, `log.encoder`, and `log.development` values for zap configuration

## [2.9.0] - 2026-02-12

### Added

- PDB/replica-aware safety for BinPacker consolidation plan
- Disruption budget checks before pod evictions (respects PDB `DisruptionsAllowed`)
- Fallback safety: require at least 2 ready replicas when no PDB exists
- Support for both Deployment and StatefulSet workload resolution
- Performance considerations note in README

## [2.8.0] - 2026-02-12

### Added

- `spec.suspend` field on SleepingSchedule to temporarily pause schedule management
- Automatic wake-up of sleeping workloads when a schedule is suspended
- `Ready` condition on SleepingSchedule status (Suspended/Active/Sleeping reasons)
- `Suspended` print column in `kubectl get sleepschedules` output

## [2.7.0] - 2026-02-11

### Added

- Workload state verification after wake transitions (10-minute window detects and corrects desynced workloads)
- Kubernetes events on SleepSchedule CRDs for sleep/wake transitions and desync corrections
- Prometheus metrics: `slumlord_reconcile_actions_total`, `slumlord_wake_duration_seconds`, `slumlord_managed_workloads`
- Smart requeue intervals: 5min idle, 30s approaching transition, 30s during verification window
- `MaxConcurrentReconciles=5` for SleepScheduleReconciler (parallel namespace reconciliation)

### Changed

- Reduced log noise: no-op reconciliations now log at debug level (V1) instead of INFO
- Increased default memory limit from 128Mi to 256Mi

### Fixed

- Wake-up taking hours on clusters with 120+ SleepSchedule CRDs ([#40](https://github.com/cschockaert/slumlord/issues/40))

## [2.6.0] - 2026-02-10

### Added

- Support for MariaDB Operator CRDs: `MariaDB` and `MaxScale` in SlumlordSleepSchedule
- Suspend/resume reconciliation via `spec.suspend` (same pattern as FluxCD)
- RBAC permissions for `k8s.mariadb.com` resources (mariadbs, maxscales)
- Graceful handling when MariaDB Operator CRDs are not installed (skip with log, no crash)

### Changed

- Renamed `sleepFluxResources`/`wakeFluxResource` to generic `sleepSuspendResources`/`wakeSuspendResource`
- Fixed wake tests to be time-independent (no more failures during 23:00-23:59 UTC)

## [2.5.0] - 2026-02-10

### Added

- Support for Prometheus Operator CRDs: `ThanosRuler`, `Alertmanager`, `Prometheus` in SlumlordSleepSchedule
- Generic `sleepReplicaResources`/`wakeReplicaResource` functions for any CRD using `spec.replicas`
- RBAC permissions for `monitoring.coreos.com` resources (thanosrulers, alertmanagers, prometheuses)
- Graceful handling when Prometheus Operator CRDs are not installed (skip with log, no crash)

## [2.4.0] - 2026-02-09

### Added

- SlumlordNodeDrainPolicy CRD and controller for draining underutilized nodes on a cron schedule
- Cluster-scoped resource with node selector and OR-logic thresholds (CPU or memory)
- Cron-based scheduling with timezone support via `robfig/cron/v3`
- Full drain lifecycle: cordon, evict pods (Eviction API), wait for drain, optionally delete node
- Safety controls: `maxNodesPerRun`, `minReadyNodes`, `dryRun`, `suspend`
- Pod filtering: skip DaemonSet, mirror, terminating, emptyDir (configurable), pod selector
- Kubernetes events for drain lifecycle (DrainStarted, DrainCompleted, DrainFailed, NodeDeleted)
- Per-node drain results in status with CPU/memory percentages and eviction counts
- Dashboard: `/api/node-drain-policies` endpoint and Node Drain Policies UI section
- Helm chart integration with `nodeDrain.enabled` toggle and conditional RBAC

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

[Unreleased]: https://github.com/cschockaert/slumlord/compare/v2.12.1...HEAD
[2.12.1]: https://github.com/cschockaert/slumlord/compare/v2.12.0...v2.12.1
[2.12.0]: https://github.com/cschockaert/slumlord/compare/v2.11.0...v2.12.0
[2.11.0]: https://github.com/cschockaert/slumlord/compare/v2.10.0...v2.11.0
[2.10.0]: https://github.com/cschockaert/slumlord/compare/v2.9.0...v2.10.0
[2.9.0]: https://github.com/cschockaert/slumlord/compare/v2.8.0...v2.9.0
[2.8.0]: https://github.com/cschockaert/slumlord/compare/v2.7.0...v2.8.0
[2.7.0]: https://github.com/cschockaert/slumlord/compare/v2.6.0...v2.7.0
[2.6.0]: https://github.com/cschockaert/slumlord/compare/v2.5.0...v2.6.0
[2.5.0]: https://github.com/cschockaert/slumlord/compare/v2.4.0...v2.5.0
[2.4.0]: https://github.com/cschockaert/slumlord/compare/v2.3.0...v2.4.0
[2.3.0]: https://github.com/cschockaert/slumlord/compare/v2.2.0...v2.3.0
[2.2.0]: https://github.com/cschockaert/slumlord/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/cschockaert/slumlord/compare/v2.0.1...v2.1.0
[2.0.1]: https://github.com/cschockaert/slumlord/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/cschockaert/slumlord/compare/v1.0.0...v2.0.0
[1.0.0]: https://github.com/cschockaert/slumlord/releases/tag/v1.0.0
