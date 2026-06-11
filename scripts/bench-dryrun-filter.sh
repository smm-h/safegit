#!/usr/bin/env bash
#
# Benchmark 10 approaches for filtering scrub dry-run scan results
# to a commit range. Uses the safegit repo itself as test data.
#
set -euo pipefail

REPO_DIR="$(git rev-parse --show-toplevel)"
cd "$REPO_DIR"

# Determine the "from" commit: 50 commits back from HEAD
FROM_SHA=$(git log --oneline -50 | tail -1 | awk '{print $1}')
FROM_SHA_FULL=$(git rev-parse "$FROM_SHA")
PATTERN="error"

echo "=== Scrub dry-run filter benchmark ==="
echo "Repo:    $REPO_DIR"
echo "Range:   ${FROM_SHA}..HEAD ($(git rev-list "${FROM_SHA_FULL}..HEAD" | wc -l) commits)"
echo "Pattern: $PATTERN"
echo "Total blobs in repo: $(git cat-file --batch-all-objects --batch-check 2>/dev/null | grep -c ' blob ')"
echo ""

# Results table storage
declare -a R_APPROACH R_TIME R_MATCHES R_NOTES

record() {
    local idx=$1 time=$2 matches=$3 notes=$4
    R_APPROACH[$idx]=$idx
    R_TIME[$idx]=$time
    R_MATCHES[$idx]=$matches
    R_NOTES[$idx]=$notes
}

# Helper: measure wall-clock time of a command, capture stdout to a file
# Usage: elapsed=$(measure_time cmd args...)
# Stdout of cmd goes to $BENCH_STDOUT
BENCH_STDOUT=$(mktemp "$REPO_DIR/.bench-stdout-XXXXXX")
BENCH_TMPDIR=$(mktemp -d "$REPO_DIR/.bench-tmp-XXXXXX")
cleanup() {
    saferm delete -f --description "bench cleanup: temp stdout file" "$BENCH_STDOUT" 2>/dev/null || true
    saferm delete -rf --description "bench cleanup: temp working dir" "$BENCH_TMPDIR" 2>/dev/null || true
}
trap cleanup EXIT

measure_time() {
    local start end
    start=$(date +%s.%N)
    "$@" > "$BENCH_STDOUT" 2>/dev/null || true
    end=$(date +%s.%N)
    echo "$end - $start" | bc
}

echo "--- Approach 1: No filtering (baseline) ---"
# Scan all blobs, grep for pattern, count matches.
approach1() {
    git cat-file --batch-all-objects --batch-check 2>/dev/null \
        | awk '$2 == "blob" {print $1}' \
        | git cat-file --batch 2>/dev/null \
        | grep -c "$PATTERN" || echo 0
}
T=$(measure_time approach1)
M=$(cat "$BENCH_STDOUT")
record 1 "$T" "$M" "baseline, all objects"
echo "  Time: ${T}s  Matches: $M"

echo "--- Approach 2: Post-filter by commit reachability ---"
# Scan all (like #1), then build set of reachable blobs from range trees,
# filter matches to those blobs.
approach2() {
    # Phase 1: get all matching blob SHAs from full scan
    local all_match_blobs="$BENCH_TMPDIR/a2_match_blobs"
    git cat-file --batch-all-objects --batch-check 2>/dev/null \
        | awk '$2 == "blob" {print $1}' \
        | git cat-file --batch 2>/dev/null \
        | awk -v pat="$PATTERN" '
            /^[0-9a-f]{40} blob [0-9]+$/ { sha=$1; next }
            sha && $0 ~ pat { print sha; sha="" }
        ' | sort -u > "$all_match_blobs"

    # Phase 2: collect reachable blob SHAs from trees in range
    local range_blobs="$BENCH_TMPDIR/a2_range_blobs"
    git log --format=%T "${FROM_SHA_FULL}..HEAD" \
        | while read -r tree; do
            git ls-tree -r "$tree" 2>/dev/null | awk '{print $3}'
        done | sort -u > "$range_blobs"

    # Phase 3: intersect
    comm -12 "$all_match_blobs" "$range_blobs" | wc -l
}
T=$(measure_time approach2)
M=$(cat "$BENCH_STDOUT" | tr -d ' ')
record 2 "$T" "$M" "full scan + tree-walk filter"
echo "  Time: ${T}s  Matches: $M"

echo "--- Approach 3: Filter by attribution (commit range check) ---"
# Scan all, then for each matching blob, find an introducing commit
# and check if it's in the range using merge-base --is-ancestor.
approach3() {
    # Get matching blob SHAs (reuse from approach 1 style scan)
    local match_blobs="$BENCH_TMPDIR/a3_match_blobs"
    git cat-file --batch-all-objects --batch-check 2>/dev/null \
        | awk '$2 == "blob" {print $1}' \
        | git cat-file --batch 2>/dev/null \
        | awk -v pat="$PATTERN" '
            /^[0-9a-f]{40} blob [0-9]+$/ { sha=$1; next }
            sha && $0 ~ pat { print sha; sha="" }
        ' | sort -u > "$match_blobs"

    # For each match blob, use git log --find-object to find a commit
    # that introduced it, then check if that commit is in range.
    # (Limit to first 20 blobs to keep runtime sane)
    local count=0
    local checked=0
    while read -r blob && [ $checked -lt 20 ]; do
        checked=$((checked + 1))
        local commit
        commit=$(git log --all --find-object="$blob" --format=%H -1 2>/dev/null || true)
        if [ -n "$commit" ]; then
            if git merge-base --is-ancestor "$FROM_SHA_FULL" "$commit" 2>/dev/null \
               && git merge-base --is-ancestor "$commit" HEAD 2>/dev/null; then
                count=$((count + 1))
            fi
        fi
    done < "$match_blobs"
    echo "$count (of $checked checked)"
}
T=$(measure_time approach3)
M=$(cat "$BENCH_STDOUT")
record 3 "$T" "$M" "per-blob ancestry check (20 sample)"
echo "  Time: ${T}s  Matches: $M"

echo "--- Approach 4: rev-list --objects + set filter ---"
# Get all reachable objects in range, filter to blobs, scan only those.
approach4() {
    local range_objs="$BENCH_TMPDIR/a4_range_objs"
    # Get object SHAs reachable in range (includes blobs, trees, commits)
    git rev-list --objects "${FROM_SHA_FULL}..HEAD" \
        | awk '{print $1}' \
        | sort -u > "$range_objs"

    # Type-check and scan only blobs
    cat "$range_objs" \
        | git cat-file --batch-check 2>/dev/null \
        | awk '$2 == "blob" {print $1}' \
        | git cat-file --batch 2>/dev/null \
        | grep -c "$PATTERN" || echo 0
}
T=$(measure_time approach4)
M=$(cat "$BENCH_STDOUT")
record 4 "$T" "$M" "rev-list objects + batch scan"
echo "  Time: ${T}s  Matches: $M"

echo "--- Approach 5: diff-tree per commit ---"
# For each commit in range, get changed blobs via diff-tree.
# Union all blobs, scan only those.
approach5() {
    local changed_blobs="$BENCH_TMPDIR/a5_changed_blobs"
    git log --format=%H "${FROM_SHA_FULL}..HEAD" \
        | while read -r commit; do
            git diff-tree -r --diff-filter=ACMR --no-commit-id "$commit" 2>/dev/null \
                | awk '{print $4}'
        done | sort -u > "$changed_blobs"

    local blob_count
    blob_count=$(wc -l < "$changed_blobs")

    # Scan those blobs
    cat "$changed_blobs" \
        | git cat-file --batch 2>/dev/null \
        | grep -c "$PATTERN" || echo 0
}
T=$(measure_time approach5)
M=$(cat "$BENCH_STDOUT")
record 5 "$T" "$M" "diff-tree changed blobs only"
echo "  Time: ${T}s  Matches: $M"

echo "--- Approach 6: Pre-filtered scan (decomposed) ---"
# Same as #4 but measure the "get reachable objects" and "scan" phases separately.
approach6() {
    local range_blobs="$BENCH_TMPDIR/a6_range_blobs"

    # Phase 1: get reachable objects
    local t1_start t1_end t1
    t1_start=$(date +%s.%N)
    git rev-list --objects "${FROM_SHA_FULL}..HEAD" \
        | awk '{print $1}' \
        | git cat-file --batch-check 2>/dev/null \
        | awk '$2 == "blob" {print $1}' \
        | sort -u > "$range_blobs"
    t1_end=$(date +%s.%N)
    t1=$(echo "$t1_end - $t1_start" | bc)

    local blob_count
    blob_count=$(wc -l < "$range_blobs")

    # Phase 2: scan those blobs
    local t2_start t2_end t2
    t2_start=$(date +%s.%N)
    local matches
    matches=$(cat "$range_blobs" \
        | git cat-file --batch 2>/dev/null \
        | grep -c "$PATTERN" || echo 0)
    t2_end=$(date +%s.%N)
    t2=$(echo "$t2_end - $t2_start" | bc)

    echo "${matches} (enum=${t1}s scan=${t2}s blobs=${blob_count})"
}
T=$(measure_time approach6)
M=$(cat "$BENCH_STDOUT")
record 6 "$T" "$M" "rev-list decomposed (enum+scan)"
echo "  Time: ${T}s  Matches: $M"

echo "--- Approach 7: git log -p + grep ---"
# Scan diffs rather than full blob content. Different semantics.
approach7() {
    git log -p "${FROM_SHA_FULL}..HEAD" | grep -c "$PATTERN" || echo 0
}
T=$(measure_time approach7)
M=$(cat "$BENCH_STDOUT")
record 7 "$T" "$M" "git log -p (diff lines, not blobs)"
echo "  Time: ${T}s  Matches: $M"

echo "--- Approach 8: Two-phase (full scan + range filter) ---"
# Phase 1: full scan. Phase 2: build range set. Phase 3: intersect.
# Report each phase time.
approach8() {
    # Phase 1: full scan - get matching blob SHAs
    local t1_start t1_end t1
    local all_matches="$BENCH_TMPDIR/a8_all_matches"
    t1_start=$(date +%s.%N)
    git cat-file --batch-all-objects --batch-check 2>/dev/null \
        | awk '$2 == "blob" {print $1}' \
        | git cat-file --batch 2>/dev/null \
        | awk -v pat="$PATTERN" '
            /^[0-9a-f]{40} blob [0-9]+$/ { sha=$1; next }
            sha && $0 ~ pat { print sha; sha="" }
        ' | sort -u > "$all_matches"
    t1_end=$(date +%s.%N)
    t1=$(echo "$t1_end - $t1_start" | bc)

    # Phase 2: build range object set
    local t2_start t2_end t2
    local range_set="$BENCH_TMPDIR/a8_range_set"
    t2_start=$(date +%s.%N)
    git rev-list --objects "${FROM_SHA_FULL}..HEAD" \
        | awk '{print $1}' | sort -u > "$range_set"
    t2_end=$(date +%s.%N)
    t2=$(echo "$t2_end - $t2_start" | bc)

    # Phase 3: intersect
    local t3_start t3_end t3
    t3_start=$(date +%s.%N)
    local count
    count=$(comm -12 "$all_matches" "$range_set" | wc -l)
    t3_end=$(date +%s.%N)
    t3=$(echo "$t3_end - $t3_start" | bc)

    echo "${count} (scan=${t1}s range=${t2}s intersect=${t3}s)"
}
T=$(measure_time approach8)
M=$(cat "$BENCH_STDOUT")
record 8 "$T" "$M" "full scan + range set + intersect"
echo "  Time: ${T}s  Matches: $M"

echo "--- Approach 9: Bloom filter simulation ---"
# Measure cost of building a sorted set from rev-list objects
# that could serve as a bloom filter. Compare set-build cost.
approach9() {
    local range_objs_raw="$BENCH_TMPDIR/a9_raw"
    local range_objs_sorted="$BENCH_TMPDIR/a9_sorted"
    local range_objs_hashed="$BENCH_TMPDIR/a9_hashed"

    # Phase 1: raw enumeration
    local t1_start t1_end t1
    t1_start=$(date +%s.%N)
    git rev-list --objects "${FROM_SHA_FULL}..HEAD" \
        | awk '{print $1}' > "$range_objs_raw"
    t1_end=$(date +%s.%N)
    t1=$(echo "$t1_end - $t1_start" | bc)

    local obj_count
    obj_count=$(wc -l < "$range_objs_raw")

    # Phase 2: sort -u (simulates hash-set build)
    local t2_start t2_end t2
    t2_start=$(date +%s.%N)
    sort -u "$range_objs_raw" > "$range_objs_sorted"
    t2_end=$(date +%s.%N)
    t2=$(echo "$t2_end - $t2_start" | bc)

    local unique_count
    unique_count=$(wc -l < "$range_objs_sorted")

    # Phase 3: simulate bloom-filter build (hash each SHA to a bucket)
    # Using cksum as a cheap hash stand-in
    local t3_start t3_end t3
    t3_start=$(date +%s.%N)
    while read -r sha; do
        printf "%d\n" "0x${sha:0:8}" 2>/dev/null || echo 0
    done < "$range_objs_sorted" \
        | awk '{print $1 % 65536}' \
        | sort -un > "$range_objs_hashed"
    t3_end=$(date +%s.%N)
    t3=$(echo "$t3_end - $t3_start" | bc)

    local bucket_count
    bucket_count=$(wc -l < "$range_objs_hashed")

    echo "${unique_count} objs (enum=${t1}s sort=${t2}s bloom=${t3}s buckets=${bucket_count})"
}
T=$(measure_time approach9)
M=$(cat "$BENCH_STDOUT")
record 9 "$T" "$M" "bloom filter simulation"
echo "  Time: ${T}s  Matches: $M"

echo "--- Approach 10: Graph walk (ls-tree per commit) ---"
# Walk each commit's tree, collecting unique blob SHAs.
# Measures tree-walk cost of a custom walker.
approach10() {
    local walk_blobs="$BENCH_TMPDIR/a10_walk_blobs"
    local commits
    commits=$(git log --format=%H "${FROM_SHA_FULL}..HEAD")

    local t1_start t1_end t1
    t1_start=$(date +%s.%N)
    echo "$commits" \
        | while read -r commit; do
            git ls-tree -r "$commit" 2>/dev/null | awk '{print $3}'
        done | sort -u > "$walk_blobs"
    t1_end=$(date +%s.%N)
    t1=$(echo "$t1_end - $t1_start" | bc)

    local blob_count
    blob_count=$(wc -l < "$walk_blobs")

    # Phase 2: scan those blobs
    local t2_start t2_end t2
    t2_start=$(date +%s.%N)
    local matches
    matches=$(cat "$walk_blobs" \
        | git cat-file --batch 2>/dev/null \
        | grep -c "$PATTERN" || echo 0)
    t2_end=$(date +%s.%N)
    t2=$(echo "$t2_end - $t2_start" | bc)

    echo "${matches} (walk=${t1}s scan=${t2}s blobs=${blob_count})"
}
T=$(measure_time approach10)
M=$(cat "$BENCH_STDOUT")
record 10 "$T" "$M" "ls-tree per commit walk"
echo "  Time: ${T}s  Matches: $M"

echo ""
echo "============================================="
echo "                RESULTS TABLE"
echo "============================================="
printf "%-10s | %-10s | %-30s | %s\n" "Approach" "Time (s)" "Matches" "Notes"
printf "%-10s-+-%-10s-+-%-30s-+-%s\n" "----------" "----------" "------------------------------" "------------------------------------"
for i in 1 2 3 4 5 6 7 8 9 10; do
    printf "%-10s | %-10s | %-30s | %s\n" "$i" "${R_TIME[$i]}" "${R_MATCHES[$i]}" "${R_NOTES[$i]}"
done
echo ""
echo "Done."
