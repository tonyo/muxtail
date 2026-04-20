#!/usr/bin/env bash
# endurance/run.sh — Multi-day endurance test for muxtail
#
# Exercises log rotation stress (-F mode) while recording CPU/RSS metrics.
#
# Usage:
#   ./endurance/run.sh [ROTATION_INTERVAL_SEC=300] [METRICS_INTERVAL_SEC=60] [DURATION_SEC=30]
#
# Quick smoke test (rotate every 60s, sample every 10s, run for 120s):
#   ./endurance/run.sh 60 10 120
#
# Run indefinitely (pass 0 for duration):
#   ./endurance/run.sh 300 60 0

set -euo pipefail

ROTATION_INTERVAL=${1:-300}   # rotate log file every N seconds (default 5 min)
METRICS_INTERVAL=${2:-60}     # sample CPU/RSS every N seconds (default 1 min)
DURATION=${3:-30}             # stop after N seconds; 0 = run indefinitely

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_DIR=$(dirname "$SCRIPT_DIR")

WORK_DIR=$(mktemp -d /tmp/muxtail-soak-XXXXXX)
LOG_FILE="$WORK_DIR/app.log"
MUXTAIL_OUT="$WORK_DIR/muxtail.out"
METRICS_CSV="$WORK_DIR/metrics.csv"
EVENTS_LOG="$WORK_DIR/events.log"
BINARY="$WORK_DIR/muxtail"

log_event() {
    echo "$(date -Iseconds) $*" | tee -a "$EVENTS_LOG"
}

echo "=== muxtail soak test ==="
echo "Work dir:          $WORK_DIR"
echo "Rotation interval: ${ROTATION_INTERVAL}s"
echo "Metrics interval:  ${METRICS_INTERVAL}s"
if (( DURATION > 0 )); then
    echo "Duration:          ${DURATION}s"
else
    echo "Duration:          indefinite"
fi
echo ""

# Log system info at startup
log_event "START kernel=$(uname -r) uptime=$(uptime -p) mem_free_mb=$(free -m | awk '/^Mem/{print $4}')MB load=$(cut -d' ' -f1-3 /proc/loadavg)"

# Build binary from source
echo "Building muxtail..."
go build -o "$BINARY" "$REPO_DIR"
log_event "BINARY built ok: $BINARY"
echo ""

# Collect background PIDs for cleanup
ALL_PIDS=()

cleanup() {
    local rc=$?
    echo ""
    log_event "CLEANUP triggered (exit code $rc) mem_free_mb=$(free -m | awk '/^Mem/{print $4}')MB load=$(cut -d' ' -f1-3 /proc/loadavg)"
    # Snapshot recent dmesg OOM lines if any
    dmesg --time-format iso 2>/dev/null | grep -iE 'oom|killed process|out of memory' | tail -5 >> "$EVENTS_LOG" || true
    echo "Stopping all background processes..."
    for pid in "${ALL_PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    echo "Done. Work dir: $WORK_DIR"
}
trap cleanup EXIT

# Watch a background PID and log when it exits (poll since wait can't track non-child PIDs)
watch_pid() {
    local name=$1 pid=$2
    while kill -0 "$pid" 2>/dev/null; do
        sleep 2
    done
    log_event "PROCESS EXIT: $name PID=$pid"
}

# Start muxtail in follow-retry mode
touch "$LOG_FILE"
"$BINARY" -F -p basename "$LOG_FILE" >> "$MUXTAIL_OUT" 2>&1 &
MUXTAIL_PID=$!
ALL_PIDS+=("$MUXTAIL_PID")
log_event "STARTED muxtail PID=$MUXTAIL_PID"
echo "muxtail output:   $MUXTAIL_OUT"
watch_pid "muxtail" "$MUXTAIL_PID" &

# Log writer: appends ~100 lines/sec
# echo >> path re-opens the file each iteration, so post-rotation writes go to the new file.
(
    seq=0
    while true; do
        echo "$(date -Iseconds) line $seq" >> "$LOG_FILE"
        seq=$(( seq + 1 ))
        sleep 0.01
    done
) &
WRITER_PID=$!
ALL_PIDS+=("$WRITER_PID")
watch_pid "log-writer" "$WRITER_PID" &

# Rotation daemon: rename app.log, create fresh one, clean up old rotations
(
    rotation_count=0
    while true; do
        sleep "$ROTATION_INTERVAL"
        stamp=$(date +%Y%m%d%H%M%S)
        mv "$LOG_FILE" "${LOG_FILE}.${stamp}" 2>/dev/null || true
        touch "$LOG_FILE"
        rotation_count=$(( rotation_count + 1 ))
        log_event "ROTATION #${rotation_count} -> app.log.${stamp}"
        # Delete rotations older than 2 minutes to keep disk use bounded
        find "$WORK_DIR" -maxdepth 1 -name 'app.log.*' -mmin +2 -delete 2>/dev/null || true
    done
) &
ROTATOR_PID=$!
ALL_PIDS+=("$ROTATOR_PID")
watch_pid "rotator" "$ROTATOR_PID" &

# Metrics sampler: reads /proc for RSS, CPU, system free mem, load; writes CSV
(
    hz=$(getconf CLK_TCK)
    prev_ticks=0
    prev_ts=0
    prev_oom_seq=""
    echo "timestamp,rss_kb,cpu_pct,sys_free_mb,load1" > "$METRICS_CSV"
    while kill -0 "$MUXTAIL_PID" 2>/dev/null; do
        sleep "$METRICS_INTERVAL"
        ts=$(date +%s)

        # muxtail RSS
        rss=$(awk '/VmRSS/{print $2}' /proc/"$MUXTAIL_PID"/status 2>/dev/null || echo 0)

        # muxtail CPU
        stat_line=$(cat /proc/"$MUXTAIL_PID"/stat 2>/dev/null || echo "")
        utime=0; stime=0
        if [[ -n "$stat_line" ]]; then
            utime=$(echo "$stat_line" | awk '{print $14}')
            stime=$(echo "$stat_line" | awk '{print $15}')
        fi
        ticks=$(( utime + stime ))
        cpu=0
        if (( prev_ts > 0 )); then
            elapsed=$(( ts - prev_ts ))
            if (( elapsed > 0 )); then
                cpu=$(awk "BEGIN{printf \"%.2f\", ($ticks - $prev_ticks) / ($elapsed * $hz) * 100}")
            fi
        fi

        # System free memory (MB) and 1-min load average
        sys_free=$(free -m | awk '/^Mem/{print $4}')
        load1=$(cut -d' ' -f1 /proc/loadavg)

        echo "$(date -Iseconds),$rss,$cpu,$sys_free,$load1" >> "$METRICS_CSV"

        # Check dmesg for new OOM kills
        oom_seq=$(dmesg --time-format iso 2>/dev/null | grep -iE 'oom|killed process|out of memory' | tail -1 || true)
        if [[ -n "$oom_seq" && "$oom_seq" != "$prev_oom_seq" ]]; then
            log_event "OOM DETECTED: $oom_seq"
            prev_oom_seq=$oom_seq
        fi

        prev_ticks=$ticks
        prev_ts=$ts
    done
    log_event "METRICS sampler exiting: muxtail no longer alive"
) &
ALL_PIDS+=("$!")

echo "Metrics CSV:      $METRICS_CSV"
echo "Events log:       $EVENTS_LOG"
echo ""
if (( DURATION > 0 )); then
    echo "Running for ${DURATION}s. Press Ctrl+C to stop early."
else
    echo "Running indefinitely. Press Ctrl+C to stop."
fi
echo ""

if (( DURATION > 0 )); then
    sleep "$DURATION" || log_event "MAIN SLEEP interrupted (exit $?)"
    log_event "DURATION reached after ${DURATION}s"
else
    wait "$MUXTAIL_PID" || log_event "MUXTAIL wait returned (exit $?)"
fi
