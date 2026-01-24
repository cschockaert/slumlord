# Slumlord

Kubernetes operator for cost optimization - put your workloads to sleep at night.

## Features

- **Sleep Schedules**: Scale down Deployments, StatefulSets, and suspend CronJobs during off-hours
- **Timezone Support**: Define schedules in any timezone
- **Day-of-week Filtering**: Apply schedules only on specific days
- **Label Selectors**: Target workloads by labels
- **Automatic Restoration**: Workloads return to their original state when waking up

## Quick Start

```bash
# Install the CRD
kubectl apply -f config/crd/

# Create a sleep schedule
kubectl apply -f - <<EOF
apiVersion: slumlord.io/v1alpha1
kind: SlumlordSleepSchedule
metadata:
  name: nightly-sleep
spec:
  selector:
    matchLabels:
      slumlord.io/managed: "true"
  schedule:
    start: "22:00"
    end: "06:00"
    timezone: "Europe/Paris"
EOF
```

## License

Apache 2.0
