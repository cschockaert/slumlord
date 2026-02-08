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
  - `spec.selector`: Label selector and workload types (Deployment, StatefulSet, CronJob)
  - `spec.schedule`: Time window with start/end times, timezone, and day-of-week filter
  - `status.managedWorkloads`: Tracks original replica counts/suspend states for restoration

### Controller (internal/controller/)

- **SleepScheduleReconciler**: Main reconciliation loop
  - Runs every minute to check if current time falls within sleep window
  - On sleep: scales Deployments/StatefulSets to 0, suspends CronJobs
  - On wake: restores original replica counts and suspend states from status

### Helm Chart (charts/slumlord/)

- `version` and `appVersion` in `Chart.yaml` must always match the latest release tag
- No `v` prefix (plain semver: `2.0.1`, not `v2.0.1`)

### Changelog (CHANGELOG.md)

- Follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format
- Must be updated on each release: move items from `[Unreleased]` to a new version section
- Update comparison links at the bottom of the file

### Key Design Decisions

1. **State stored in status**: Original workload state (replicas, suspend) is stored in `status.managedWorkloads` to survive operator restarts
2. **Timezone-aware**: Uses Go's `time.LoadLocation` for proper timezone handling
3. **Overnight schedules**: Handles schedules that cross midnight (e.g., 22:00-06:00)
4. **Per-namespace**: Each SlumlordSleepSchedule operates within its own namespace
