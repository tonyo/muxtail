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
OUTPUT=${4:-file}             # muxtail output destination: "file" or "null"

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_DIR=$(dirname "$SCRIPT_DIR")

WORK_DIR=$(mktemp -d /tmp/muxtail-endurance-XXXXXX)
LOG_FILE="$WORK_DIR/app.log"
MUXTAIL_OUT="$WORK_DIR/muxtail.out"
METRICS_CSV="$WORK_DIR/metrics.csv"
EVENTS_LOG="$WORK_DIR/events.log"
BINARY="$WORK_DIR/muxtail"

log_event() {
    echo "$(date -Iseconds) $*" | tee -a "$EVENTS_LOG"
}

echo "=== muxtail endurance test ==="
echo "Work dir:          $WORK_DIR"
echo "Rotation interval: ${ROTATION_INTERVAL}s"
echo "Metrics interval:  ${METRICS_INTERVAL}s"
if (( DURATION > 0 )); then
    echo "Duration:          ${DURATION}s"
else
    echo "Duration:          indefinite"
fi
echo "Output:            ${OUTPUT}"
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
if [[ "$OUTPUT" == "null" ]]; then
    "$BINARY" -F -p basename "$LOG_FILE" > /dev/null 2>&1 &
else
    "$BINARY" -F -p basename "$LOG_FILE" >> "$MUXTAIL_OUT" 2>&1 &
fi
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
    prev_write_bytes=0
    prev_ctx=0
    prev_oom_seq=""
    echo "timestamp,rss_kb,vm_size_kb,vm_swap_kb,cpu_pct,threads,fd_count,write_bytes_delta,ctx_switches_delta,sys_free_mb,sys_swap_used_mb,load1" > "$METRICS_CSV"
    while kill -0 "$MUXTAIL_PID" 2>/dev/null; do
        sleep "$METRICS_INTERVAL"
        ts=$(date +%s)

        # muxtail /proc/status fields
        status=$(cat /proc/"$MUXTAIL_PID"/status 2>/dev/null || echo "")
        rss=$(echo     "$status" | awk '/VmRSS/{print $2}')
        vm_size=$(echo "$status" | awk '/VmSize/{print $2}')
        vm_swap=$(echo "$status" | awk '/VmSwap/{print $2}')
        threads=$(echo "$status" | awk '/^Threads/{print $2}')
        vol_ctx=$(echo "$status" | awk '/voluntary_ctxt_switches/{print $2}' | head -1)
        nonvol_ctx=$(echo "$status" | awk '/nonvoluntary_ctxt_switches/{print $2}')
        ctx=$(( ${vol_ctx:-0} + ${nonvol_ctx:-0} ))

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

        # muxtail I/O — write_bytes delta since last sample
        write_bytes=$(awk '/^write_bytes/{print $2}' /proc/"$MUXTAIL_PID"/io 2>/dev/null || echo 0)
        write_delta=$(( write_bytes - prev_write_bytes ))

        # context switch delta since last sample
        ctx_delta=$(( ctx - prev_ctx ))

        # Open file descriptor count
        fd_count=$(ls /proc/"$MUXTAIL_PID"/fd 2>/dev/null | wc -l || echo 0)

        # System memory and load
        free_out=$(free -m)
        sys_free=$(echo     "$free_out" | awk '/^Mem/{print $4}')
        sys_swap=$(echo     "$free_out" | awk '/^Swap/{print $3}')
        load1=$(cut -d' ' -f1 /proc/loadavg)

        echo "$(date -Iseconds),$rss,${vm_size:-0},${vm_swap:-0},$cpu,${threads:-0},$fd_count,$write_delta,$ctx_delta,$sys_free,$sys_swap,$load1" >> "$METRICS_CSV"

        # Check dmesg for new OOM kills
        oom_seq=$(dmesg --time-format iso 2>/dev/null | grep -iE 'oom|killed process|out of memory' | tail -1 || true)
        if [[ -n "$oom_seq" && "$oom_seq" != "$prev_oom_seq" ]]; then
            log_event "OOM DETECTED: $oom_seq"
            prev_oom_seq=$oom_seq
        fi

        prev_ticks=$ticks
        prev_ts=$ts
        prev_write_bytes=$write_bytes
        prev_ctx=$ctx
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
