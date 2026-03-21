#!/usr/bin/env bash
set -exuo pipefail

NUM_LINES=600000
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

BINARY="./muxtail"
FILE_A="$WORK_DIR/file_a.log"
FILE_B="$WORK_DIR/file_b.log"
OUTPUT="$WORK_DIR/output.txt"

echo "=== muxtail stress test ==="
echo "Lines per file: $NUM_LINES"

# Build
echo "Building..."
go build -o "$BINARY" ./...

# Generate files with python3
echo "Generating test files..."
python3 - <<EOF
lines = $NUM_LINES
with open("$FILE_A", "w") as fa, open("$FILE_B", "w") as fb:
    for i in range(lines):
        seq = str(i).zfill(9)
        fa.write(f"AAA:{seq}:{'X'*60}\n")
        fb.write(f"BBB:{seq}:{'Y'*60}\n")
EOF

echo "File A size: $(du -sh "$FILE_A" | cut -f1)"
echo "File B size: $(du -sh "$FILE_B" | cut -f1)"

# Run muxtail in background
echo "Running muxtail..."
"$BINARY" -n "$NUM_LINES" --label '[A] ' --label '[B] ' "$FILE_A" "$FILE_B" > "$OUTPUT" &
MUXTAIL_PID=$!

# Poll until we have enough lines or timeout
TIMEOUT=60
ELAPSED=0
EXPECTED=$((NUM_LINES * 2))
while true; do
    ACTUAL=$(wc -l < "$OUTPUT" 2>/dev/null || echo 0)
    if [ "$ACTUAL" -ge "$EXPECTED" ]; then
        echo "Got $ACTUAL lines (expected $EXPECTED)"
        break
    fi
    if [ "$ELAPSED" -ge "$TIMEOUT" ]; then
        echo "Timeout after ${TIMEOUT}s, got $ACTUAL / $EXPECTED lines"
        break
    fi
    sleep 1
    ELAPSED=$((ELAPSED + 1))
done

kill "$MUXTAIL_PID" 2>/dev/null || true
wait "$MUXTAIL_PID" 2>/dev/null || true

echo "Output file: $(wc -l < "$OUTPUT") lines"

ERRORS=0

# Check 1: line count
ACTUAL=$(wc -l < "$OUTPUT")
if [ "$ACTUAL" -ne "$EXPECTED" ]; then
    echo "FAIL [check 1] line count: got $ACTUAL, expected $EXPECTED"
    ERRORS=$((ERRORS + 1))
else
    echo "PASS [check 1] line count: $ACTUAL"
fi

# Check 2: pattern integrity — every line must match one of the two patterns
INVALID=$(grep -cPv '^\[A\] AAA:[0-9]{9}:X{60}$|^\[B\] BBB:[0-9]{9}:Y{60}$' "$OUTPUT" || true)
if [ "$INVALID" -ne 0 ]; then
    echo "FAIL [check 2] pattern integrity: $INVALID invalid lines"
    grep -Pv '^\[A\] AAA:[0-9]{9}:X{60}$|^\[B\] BBB:[0-9]{9}:Y{60}$' "$OUTPUT" | head -5
    ERRORS=$((ERRORS + 1))
else
    echo "PASS [check 2] pattern integrity: all lines valid"
fi

# Check 3: cross-contamination
A_WITH_BBB=$(grep -c '^\[A\].*BBB' "$OUTPUT" || true)
B_WITH_AAA=$(grep -c '^\[B\].*AAA' "$OUTPUT" || true)
if [ "$A_WITH_BBB" -ne 0 ] || [ "$B_WITH_AAA" -ne 0 ]; then
    echo "FAIL [check 3] cross-contamination: [A]+BBB=$A_WITH_BBB, [B]+AAA=$B_WITH_AAA"
    ERRORS=$((ERRORS + 1))
else
    echo "PASS [check 3] no cross-contamination"
fi

# Check 4: sequence completeness — every seq 0..N-1 appears exactly once per label
SEQ_ERRORS=$(awk -v lines="$NUM_LINES" '
/^\[A\] AAA:/ {
    match($0, /AAA:([0-9]{9})/, m)
    seq_a[m[1]]++
}
/^\[B\] BBB:/ {
    match($0, /BBB:([0-9]{9})/, m)
    seq_b[m[1]]++
}
END {
    errs = 0
    for (i = 0; i < lines; i++) {
        seq = sprintf("%09d", i)
        if (seq_a[seq] != 1) {
            print "[A] seq " seq " count=" seq_a[seq]+0
            errs++
        }
        if (seq_b[seq] != 1) {
            print "[B] seq " seq " count=" seq_b[seq]+0
            errs++
        }
    }
    print errs
}
' "$OUTPUT")

SEQ_ERR_COUNT=$(echo "$SEQ_ERRORS" | tail -1)
if [ "$SEQ_ERR_COUNT" -ne 0 ]; then
    echo "FAIL [check 4] sequence completeness: $SEQ_ERR_COUNT missing/duplicate sequences"
    echo "$SEQ_ERRORS" | head -10
    ERRORS=$((ERRORS + 1))
else
    echo "PASS [check 4] all sequences present exactly once"
fi

echo ""
if [ "$ERRORS" -eq 0 ]; then
    echo "=== PASS (0 errors) ==="
    exit 0
else
    echo "=== FAIL ($ERRORS checks failed) ==="
    exit 1
fi
