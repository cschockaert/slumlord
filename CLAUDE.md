# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Slumlord is a Kubernetes operator for cost optimization. It manages workload lifecycle to reduce cloud costs by scaling down resources during off-hours (e.g., nights, weekends).

## Build Commands

```bash
# Download dependencies
go mod tidy

# Build the operator binary
make build

# Run tests
make test

# Run a single test
go test -v ./internal/controller/... -run TestName

# Lint code
make lint

# Generate DeepCopy methods (after modifying API types)
make generate

# Generate CRD manifests (after modifying API types)
make manifests

# Run locally against a cluster (uses current kubeconfig)
make run
```

## Kubernetes Deployment

```bash
# Install CRDs
make install

# Deploy operator
make deploy

# Uninstall
make uninstall
```

## Architecture

### CRD Structure (api/v1alpha1/)

- **SlumlordSleepSchedule**: Namespace-scoped resource that defines sleep schedules for workloads
  - `spec.selector`: Label selector, name patterns (wildcards), and workload types (Deployment, StatefulSet, CronJob, Cluster, HelmRelease, Kustomization, ThanosRuler, Alertmanager, Prometheus, MariaDB, MaxScale)
  - `spec.schedule`: Time window with start/end times, timezone, and day-of-week filter
  - `status.managedWorkloads`: Tracks original replica counts/suspend states for restoration

- **SlumlordIdleDetector**: Namespace-scoped resource that detects and optionally scales down idle workloads
  - `spec.selector`: Label selector, name patterns (wildcards), and workload types (Deployment, StatefulSet, CronJob)
  - `spec.thresholds`: CPU/memory usage thresholds (0-100%)
  - `spec.idleDuration`: How long a workload must be idle before action (Go duration: `30m`, `1h`)
  - `spec.action`: `alert` (report only), `scale` (auto-scale to zero), or `resize` (right-size pod requests in-place via K8s 1.33 In-Place Pod Resize)
  - `spec.resize`: Configuration for resize action (`bufferPercent`, `minRequests` with CPU/memory floor)
  - `status.idleWorkloads`: Currently detected idle workloads with timestamps
  - `status.scaledWorkloads`: Workloads scaled down with original state for restoration
  - `status.resizedWorkloads`: Workloads with in-place resized pod requests, with original state for restoration
  - **Note**: Requires metrics-server in the cluster. Without it, runs in degraded mode (always returns not-idle).

### Controller (internal/controller/)

- **SleepScheduleReconciler**: Main reconciliation loop
  - Runs every minute to check if current time falls within sleep window
  - On sleep: scales Deployments/StatefulSets to 0, scales Prometheus Operator CRDs (ThanosRuler, Alertmanager, Prometheus) to 0, suspends CronJobs, hibernates CNPG clusters, suspends FluxCD resources, suspends MariaDB Operator CRDs (MariaDB, MaxScale)
  - On wake: restores original replica counts and suspend states from status
  - Finalizer ensures workloads are restored on schedule deletion

- **IdleDetectorReconciler**: Idle workload detection loop
  - Runs every 5 minutes to check workload resource usage
  - Supports MatchLabels and/or MatchNames (wildcard glob patterns via `path.Match`)
  - Status persisted after each scale-down to prevent data loss on partial failure
  - Restore only clears successfully restored workloads; failed ones retained for retry
  - Finalizer ensures workloads are restored on detector deletion
  - Resize action: patches pod requests in-place (never modifies Deployment/StatefulSet spec), computes target from actual usage + buffer, respects minRequests floor

### Helm Chart (charts/slumlord/)

- `version` and `appVersion` in `Chart.yaml` must always match the latest release tag (plain semver, no `v` prefix)
- Tags use standard `v` prefix (e.g., `v2.12.0`), but Docker images and Helm chart versions use plain semver (e.g., `2.12.0`)

### Changelog (CHANGELOG.md)

- Follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format
- Must be updated on each release: move items from `[Unreleased]` to a new version section
- Update comparison links at the bottom of the file

### Logging & Observability

- **Structured JSON logging** by default in production (zap encoder=json)
- controller-runtime automatically injects structured fields into every log line: `controller`, `controllerGroup`, `controllerKind`, `namespace`, `name`
- Helm values expose `log.level`, `log.encoder`, `log.development` — mapped to `--zap-log-level`, `--zap-encoder`, `--zap-devel` flags
- For local development, set `log.development: true` or pass `--zap-devel` to get console encoding with stacktraces
- Filter logs per controller in Loki/Grafana: `{app="slumlord"} | json | controller="slumlordsleepschedule"`

### Crash Isolation

- controller-runtime v0.23.1 defaults `RecoverPanic` to `true` on all controllers
- Each controller's reconcile loop runs in its own goroutine with panic recovery — a panic in one controller does not crash the process
- No explicit `RecoverPanic` setting needed in `SetupWithManager`; the default is sufficient

### Key Design Decisions

1. **State stored in status**: Original workload state (replicas, suspend) is stored in `status.managedWorkloads` / `status.scaledWorkloads` to survive operator restarts
2. **Timezone-aware**: Uses Go's `time.LoadLocation` for proper timezone handling
3. **Overnight schedules**: Handles schedules that cross midnight (e.g., 22:00-06:00)
4. **Per-namespace**: Each SlumlordSleepSchedule/SlumlordIdleDetector operates within its own namespace
5. **Finalizers**: Both controllers use finalizers to restore workloads on resource deletion
6. **Single binary, multiple controllers**: All controllers run in one process with feature flags (`--enable-*`). Crash isolation via `RecoverPanic` and structured logging with per-controller fields eliminate the need for separate deployments

## Release Process

To create a new release:

1. **Update CHANGELOG.md**: Move items from `[Unreleased]` to a new version section, update comparison links at the bottom
2. **Update Helm chart version**: Set `version` and `appVersion` in `charts/slumlord/Chart.yaml` to the new version
3. **Update README install command**: Update the `--version` in the Helm install example
4. **Commit**: Commit changes on a branch, create PR, merge to main
5. **Tag**: Create a tag on main with `v` prefix

```bash
git tag v2.13.0
git push origin v2.13.0
```

6. **CI does the rest**: The release workflow (`.github/workflows/release.yaml`) triggers on tag push and:
   - Runs tests
   - Builds multi-arch Docker image (`linux/amd64`, `linux/arm64`) and pushes to `ghcr.io/cschockaert/slumlord:<version>`
   - Runs Trivy vulnerability scan (blocks on CRITICAL/HIGH)
   - Packages and pushes Helm chart to `oci://ghcr.io/cschockaert/charts/slumlord`
   - Creates a GitHub Release with auto-generated notes

**Important**: Tags use `v` prefix (e.g., `v2.13.0`). Docker images and Helm chart versions use plain semver (the `v` is stripped by CI).
