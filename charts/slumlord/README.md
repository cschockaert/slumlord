# Slumlord Helm Chart

Helm chart for Slumlord -- a Kubernetes operator that automatically scales down workloads during off-hours to reduce cloud costs.

## Prerequisites

- Kubernetes 1.26+
- Helm 3.x

## Installation

```bash
helm install slumlord oci://ghcr.io/cschockaert/charts/slumlord --version 0.2.0
```

To install with custom values:

```bash
helm install slumlord oci://ghcr.io/cschockaert/charts/slumlord --version 0.2.0 -f values.yaml
```

## Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of operator replicas | `1` |
| `image.repository` | Operator image repository | `ghcr.io/cschockaert/slumlord` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `image.tag` | Image tag (defaults to chart appVersion) | `""` |
| `imagePullSecrets` | Image pull secrets | `[]` |
| `nameOverride` | Override the chart name | `""` |
| `fullnameOverride` | Override the full release name | `""` |
| `serviceAccount.create` | Create a service account | `true` |
| `serviceAccount.annotations` | Service account annotations | `{}` |
| `serviceAccount.name` | Service account name (generated if empty) | `""` |
| `podAnnotations` | Pod annotations | `{}` |
| `podLabels` | Extra pod labels | `{}` |
| `podSecurityContext.runAsNonRoot` | Run pod as non-root | `true` |
| `podSecurityContext.seccompProfile.type` | Seccomp profile type | `RuntimeDefault` |
| `securityContext.allowPrivilegeEscalation` | Allow privilege escalation | `false` |
| `securityContext.capabilities.drop` | Linux capabilities to drop | `["ALL"]` |
| `securityContext.readOnlyRootFilesystem` | Read-only root filesystem | `true` |
| `securityContext.runAsNonRoot` | Run container as non-root | `true` |
| `securityContext.runAsUser` | Container user ID | `65532` |
| `resources.limits.cpu` | CPU limit | `500m` |
| `resources.limits.memory` | Memory limit | `128Mi` |
| `resources.requests.cpu` | CPU request | `10m` |
| `resources.requests.memory` | Memory request | `64Mi` |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |
| `leaderElection.enabled` | Enable leader election | `true` |
| `metrics.port` | Metrics port | `8080` |
| `metrics.service.enabled` | Create a Service for metrics scraping | `true` |
| `metrics.serviceMonitor.enabled` | Create a Prometheus ServiceMonitor | `false` |
| `metrics.serviceMonitor.interval` | Prometheus scrape interval | `30s` |
| `health.port` | Health probes port | `8081` |
| `podDisruptionBudget.enabled` | Enable PodDisruptionBudget | `false` |
| `podDisruptionBudget.minAvailable` | Minimum available pods | `` |
| `podDisruptionBudget.maxUnavailable` | Maximum unavailable pods | `` |
| `networkPolicy.enabled` | Enable NetworkPolicy | `false` |

## Uninstallation

Before uninstalling, delete all SlumlordSleepSchedule resources to restore workloads to their original state:

```bash
kubectl delete slumlordsleepschedules --all -A
```

Then uninstall the chart:

```bash
helm uninstall slumlord
```

## Source

<https://github.com/cschockaert/slumlord>
