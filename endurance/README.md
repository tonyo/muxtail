# Endurance Test

Long-running stability test for muxtail. Runs muxtail in `-F` (follow-retry) mode against a continuously written log file that is periodically rotated, while recording CPU and RSS metrics over time.

## Usage

```
./endurance/run.sh [ROTATION_INTERVAL_SEC=300] [METRICS_INTERVAL_SEC=60] [DURATION_SEC=30]
```

| Argument | Default | Description |
|---|---|---|
| `ROTATION_INTERVAL_SEC` | 300 | How often to rotate the log file (seconds) |
| `METRICS_INTERVAL_SEC` | 60 | How often to sample CPU/RSS (seconds) |
| `DURATION_SEC` | 30 | How long to run; `0` = indefinitely |

## Examples

```bash
# Quick smoke test
./endurance/run.sh 60 10 120

# 72-hour run in background (rotate every 5min, sample every 30s)
nohup ./endurance/run.sh 300 30 259200 > /tmp/endurance.log 2>&1 &

# Run indefinitely
./endurance/run.sh 300 60 0
```

## Output

Each run creates a temp directory (printed at startup) containing:

| File | Contents |
|---|---|
| `metrics.csv` | `timestamp, rss_kb, cpu_pct, sys_free_mb, load1` — one row per sample |
| `events.log` | Timestamped log of rotations, process exits, OOM alerts, and cleanup reason |
| `muxtail.out` | Captured muxtail output |

## What it tests

- **Memory stability** — RSS should plateau and remain flat; any growth indicates a leak
- **Log rotation resilience** — muxtail must reopen files after rotation without losing lines or crashing
- **CPU steadiness** — CPU usage should stay low and consistent under sustained load
