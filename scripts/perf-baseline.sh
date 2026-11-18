#!/usr/bin/env bash
# Adapted from ior scripts/perf-baseline.sh @ b21afc50328c (record,
# compare, tree_fingerprint, resolve_head, validate_label, the temp-file
# and rename discipline, the provenance header).
# Changes: a baseline is the untraced syscall-burst workload scenario
# (S3) timed in-process and written in benchstat format, instead of
# `go test -bench` plus ior's static BPF metrics; record builds the jar
# with `mage workloadBuild` first; record is split into small functions.
#
# Record and compare iormesh performance baselines (PLAN.md S3, decision
# #25; docs/testing.md "Performance baselines").
#
#   scripts/perf-baseline.sh record [label]   measure the current commit
#                                             (a dirty tree always gets a
#                                             "-dirty" label suffix; a
#                                             "-dirty" label on a clean
#                                             tree is refused; labels are
#                                             [A-Za-z0-9][A-Za-z0-9._+-]*,
#                                             without "..")
#   scripts/perf-baseline.sh compare OLD NEW   benchstat diff of two baselines
#
# OLD/NEW are labels (e.g. a short commit hash) or paths to
# perf/burst-*.txt.
#
# A baseline is one file, committed so a later change can be judged
# against the exact numbers that motivated it:
#
#   perf/burst-<label>.txt   syscall-burst wall times, one benchstat line
#                            per sample:
#     BenchmarkSyscallBurst/mode=untraced/threads=T  OPS  X ns/op  Y ns/syscall
#
# X and Y come from the scenario's own result line (elapsed_ns spans the
# loops only, not JVM startup), so they compare across hosts and JDKs
# far better than the process's wall time would. Only mode=untraced is
# recorded so far; the traced and bystander modes of S3 step 3 add their
# own mode= values to the same file.
#
# Environment:
#   PERF_COUNT    samples per thread count          (default 6: benchstat
#                 needs >= 4 to call a difference significant and >= 6
#                 for a confidence interval; S3 asks for three at least)
#   PERF_OPS      --ops per sample                  (default 1000000)
#   PERF_THREADS  thread counts, space-separated    (default "1 4 16")
#   PERF_FORCE    1 overwrites an existing baseline of the same label
#                 (default: refuse, before anything runs)
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
readonly repo_root
readonly perf_dir="$repo_root/perf"
readonly workload_jar="$repo_root/workload.jar"

die() {
    echo "perf-baseline: $*" >&2
    exit 1
}

# tree_fingerprint COMMIT prints one snapshot of everything a recording
# measures: the id of the tree COMMIT would have if every change outside
# perf/ were committed (tracked edits and deletions, and untracked files
# that are not ignored, since the jar build compiles a new uncommitted
# .java file just the same), built in a throwaway index so the real one
# is never touched. perf/ keeps COMMIT's content, so a tree that is clean
# outside perf/ prints exactly COMMIT^{tree}; record decides dirtiness
# and detects edits made during the run from this one value.
#
# No diff machinery is involved, so diff.external, GIT_EXTERNAL_DIFF,
# textconv and color settings cannot change it; symlinks are hashed as
# links, not by target. `git add` writes loose blobs for changed files
# into .git/objects; they are unreferenced and `git gc` prunes them. An
# untracked nested git repository is not added (git would record only its
# HEAD commit, or fail on one without commits); its path is appended on a
# line of its own instead, so it counts as dirty, but edits inside it are
# not seen. An untracked linked worktree (a directory whose .git is a
# file) is listed the same way, as "path/". A file git cannot read fails
# the snapshot, as does any other git error.
tree_fingerprint() {
    local commit=$1
    [ -n "$tmp_index_dir" ] || die "internal error: no temp index directory"
    local index=$tmp_index_dir/index others=$tmp_index_dir/others
    local entry tree nested=() excludes=()
    git -C "$repo_root" ls-files -z --others --exclude-standard -- . ':!perf' > "$others" ||
        die "git ls-files failed; cannot fingerprint the tree"
    # Without --directory, ls-files names an untracked nested repository
    # as "path/" and never lists a plain untracked file that way.
    while IFS= read -r -d '' entry; do
        if [[ $entry == */ ]]; then
            nested+=("$entry")
            excludes+=(":(exclude,literal)$entry")
        fi
    done < "$others"
    GIT_INDEX_FILE=$index git -C "$repo_root" read-tree "$commit" ||
        die "git read-tree $commit failed; cannot fingerprint the tree"
    GIT_INDEX_FILE=$index git -C "$repo_root" -c add.ignoreErrors=false \
        add -A -- . ':!perf' ${excludes[@]+"${excludes[@]}"} ||
        die "git add failed (unreadable file?); cannot fingerprint the tree"
    tree=$(GIT_INDEX_FILE=$index git -C "$repo_root" write-tree) ||
        die "git write-tree failed; cannot fingerprint the tree"
    printf '%s\n' "$tree" ${nested[@]+"${nested[@]}"}
}

# resolve_head prints the full hash of HEAD, or fails: every recording
# names the commit it measured, so an unborn branch or a broken checkout
# is fatal whatever the label.
resolve_head() {
    local head
    head=$(git -C "$repo_root" rev-parse --verify --quiet 'HEAD^{commit}') && [ -n "$head" ] ||
        die "cannot resolve HEAD (unborn branch or not a git checkout); commit first"
    echo "$head"
}

# validate_label rejects labels that would not name a plain file in perf/.
validate_label() {
    local label=$1
    [ -n "$label" ] || die "the label must not be empty"
    [[ $label != *..* ]] || die "label '$label' must not contain '..'"
    [[ $label =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] ||
        die "label '$label' must start with a letter or digit and contain only letters, digits and . _ + -"
}

# validate_settings rejects non-numeric sample settings up front, so a
# typo fails before the jar build and the minutes of measuring.
validate_settings() {
    [[ $count =~ ^[1-9][0-9]*$ ]] || die "PERF_COUNT '$count' is not a positive integer"
    [[ $ops =~ ^[1-9][0-9]*$ ]] || die "PERF_OPS '$ops' is not a positive integer"
    # The scenario parses --ops as a Java int; reject what it would
    # reject, before the jar build rather than at the first sample.
    [ "${#ops}" -le 10 ] && [ "$ops" -le 2147483647 ] || die "PERF_OPS '$ops' exceeds 2^31-1"
    local t
    [ -n "${threads// /}" ] || die "PERF_THREADS is empty"
    for t in $threads; do
        [[ $t =~ ^[1-9][0-9]*$ ]] || die "PERF_THREADS entry '$t' is not a positive integer"
        [ "${#t}" -le 4 ] && [ "$t" -le 1024 ] || die "PERF_THREADS entry '$t' exceeds 1024"
        [ "$ops" -ge "$t" ] || die "PERF_OPS $ops is below PERF_THREADS entry $t (each thread needs an op)"
    done
}

# State of the recording in progress. Globals, because the EXIT trap runs
# after record's locals are gone and the steps below share them.
tmp_burst=""
tmp_index_dir=""
head=""
label=""
dirty=""
fingerprint=""
burst_file=""
count=""
ops=""
threads=""

cleanup_tmp() {
    rm -f -- ${tmp_burst:+"$tmp_burst"}
    rm -rf -- ${tmp_index_dir:+"$tmp_index_dir"}
}

# begin_record resolves HEAD and the label, takes the tree snapshot and
# refuses to overwrite a committed baseline — all before anything runs.
begin_record() {
    local head_tree
    head=$(resolve_head)
    head_tree=$(git -C "$repo_root" rev-parse --verify --quiet "$head^{tree}") && [ -n "$head_tree" ] ||
        die "cannot resolve the tree of $head"
    if [ $# -eq 1 ]; then
        label=$1
    else
        label=$(git -C "$repo_root" rev-parse --short "$head") || die "cannot abbreviate $head"
    fi
    validate_label "$label"
    # The throwaway index lives in the git dir: its path is absolute and
    # never inside the worktree, so neither a relative TMPDIR nor one
    # inside the tree (it would show up as untracked) can skew the
    # fingerprint.
    local git_dir
    git_dir=$(git -C "$repo_root" rev-parse --absolute-git-dir) && [ -n "$git_dir" ] ||
        die "cannot resolve the git directory; cannot fingerprint the tree"
    tmp_index_dir=$(mktemp -d "$git_dir/perf-baseline.XXXXXX") ||
        die "cannot create a temp directory in $git_dir; cannot fingerprint the tree"
    fingerprint=$(tree_fingerprint "$head")
    mark_dirty "$head_tree"
    burst_file="$perf_dir/burst-$label.txt"
    # Refuse before measuring: a committed baseline is the evidence a
    # later change is judged against. The check follows the snapshot
    # because a dirty tree changes the final label.
    if [ "${PERF_FORCE:-}" != 1 ] && [ -e "$burst_file" ]; then
        die "${burst_file#"$repo_root"/} already exists; pick another label or set PERF_FORCE=1 to overwrite"
    fi
}

# mark_dirty suffixes the label when the snapshot differs from HEAD's
# tree: the header names HEAD, which is not what was measured, so both
# the file name and the commit line say so. The reverse is refused: a
# "-dirty" label on a clean tree would claim changes that do not exist.
mark_dirty() {
    local head_tree=$1
    if [ "$fingerprint" != "$head_tree" ]; then
        dirty=" +uncommitted changes"
        [[ $label == *-dirty ]] || label+="-dirty"
    elif [[ $label == *-dirty ]]; then
        die "label '$label' ends in -dirty but the tree is clean; drop the suffix"
    fi
}

# write_header prints the provenance block. benchstat ignores lines it
# cannot parse, so it travels inside the same file as the numbers.
write_header() {
    local subject
    subject=$(git -C "$repo_root" log -1 --format=%s "$head") || die "cannot read the subject of $head"
    echo "# iormesh performance baseline (syscall-burst)"
    echo "# label:       $label"
    echo "# commit:      $head ($subject)$dirty"
    echo "# date:        $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# kernel:      $(uname -r)"
    echo "# cpu:         $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | xargs) x$(nproc)"
    echo "# clocksource: $(cat /sys/devices/system/clocksource/clocksource0/current_clocksource 2>/dev/null || echo unknown)"
    echo "# java:        $(java -version 2>&1 | head -1)"
    echo "# settings:    count=$count ops=$ops threads=$threads"
    echo "# loadavg:     $(cut -d' ' -f1-3 /proc/loadavg) (at start; keep the host idle while recording)"
    # The burst's files live in java.io.tmpdir; tmpfs vs a disk
    # filesystem dominates ns/syscall, so the header names it.
    echo "# workdir fs:  $(df -T "${TMPDIR:-/tmp}" | awk 'NR == 2 {print $2}')"
    echo
}

# burst_sample runs one untraced syscall-burst and prints its benchstat
# line. The scenario prints its PID first and markers on stderr; only its
# "syscall-burst ops=…" result line is read.
burst_sample() {
    local -r t=$1
    local out line elapsed err
    # stderr goes to a file, not /dev/null: a failed sample must say why.
    # Marker lines are the scenario's normal stderr and are dropped.
    err=$(mktemp) || die "cannot create a temp file"
    if ! out=$(java -jar "$workload_jar" syscall-burst --ops "$ops" --threads "$t" 2>"$err"); then
        local why
        why=$(grep -v '^marker ' "$err" | tail -5)
        rm -f -- "$err"
        die "syscall-burst --ops $ops --threads $t failed: $why"
    fi
    rm -f -- "$err"
    line=$(grep -m1 '^syscall-burst ops=' <<< "$out") ||
        die "syscall-burst printed no result line: $out"
    elapsed=$(sed -n 's/.* elapsed_ns=\([0-9][0-9]*\)$/\1/p' <<< "$line")
    [ -n "$elapsed" ] || die "cannot parse elapsed_ns from: $line"
    # One op is a write plus a read, so ns/syscall is half of ns/op.
    awk -v t="$t" -v ops="$ops" -v e="$elapsed" 'BEGIN {
        printf "BenchmarkSyscallBurst/mode=untraced/threads=%d %d %.2f ns/op %.2f ns/syscall\n",
            t, ops, e / ops, e / (2 * ops)
    }'
}

# run_samples interleaves the thread counts sample by sample, so a slow
# drift of the host (thermals, a background job) spreads over every
# thread count instead of skewing the one measured last.
run_samples() {
    local -i i
    local t
    for ((i = 1; i <= count; i++)); do
        for t in $threads; do
            echo "perf-baseline: sample $i/$count threads=$t" >&2
            burst_sample "$t"
        done
    done
}

record() {
    [ $# -le 1 ] || die "usage: record [label]"
    count=${PERF_COUNT:-6} ops=${PERF_OPS:-1000000} threads=${PERF_THREADS:-1 4 16}
    validate_settings
    # INT/TERM exit through the EXIT trap, which removes every temp file.
    trap cleanup_tmp EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    begin_record "$@"

    cd "$repo_root"
    # Build the jar from this tree: a stale jar would measure other code
    # than the header's commit names.
    mage workloadBuild >&2 || die "mage workloadBuild failed"
    mkdir -p "$perf_dir"
    # The output goes to a temp file next to its final name (same
    # filesystem, so the final mv is a rename) and is only moved into
    # place once every check passed, so a failed or interrupted recording
    # leaves nothing behind that compare would take for a baseline.
    tmp_burst=$(mktemp "$perf_dir/.burst-$label.XXXXXX")
    write_header > "$tmp_burst"
    echo "perf-baseline: recording $label ($count samples x threads $threads, $ops ops) ..." >&2
    run_samples >> "$tmp_burst"
    finish_record
}

# finish_record checks the tree did not change while measuring, then
# renames the baseline into place.
finish_record() {
    # The header and file name describe the tree as it was before the
    # run; a commit, checkout or edit since then makes them wrong.
    local head_after fingerprint_after
    head_after=$(resolve_head)
    fingerprint_after=$(tree_fingerprint "$head")
    [ "$head_after" = "$head" ] && [ "$fingerprint_after" = "$fingerprint" ] ||
        die "tree changed during recording; nothing written"
    # mktemp creates 0600 files and mv keeps the mode; give the baseline
    # the mode a plain redirect would have (0666 minus the umask).
    chmod "$(printf '%o' $((0666 & ~$(umask))))" -- "$tmp_burst"
    mv -f -- "$tmp_burst" "$burst_file"
    tmp_burst=""
    echo "perf-baseline: wrote ${burst_file#"$repo_root"/}" >&2
}

# resolve maps a label or a path to the baseline file it names.
resolve() {
    local -r ref=$1
    if [ -f "$ref" ]; then
        echo "$ref"
    else
        echo "$perf_dir/burst-$ref.txt"
    fi
}

compare() {
    [ $# -eq 2 ] || die "usage: compare OLD NEW"
    local old new
    old=$(resolve "$1")
    new=$(resolve "$2")
    [ -f "$old" ] || die "missing $old"
    [ -f "$new" ] || die "missing $new"

    local benchstat=${BENCHSTAT:-benchstat}
    if ! command -v "$benchstat" > /dev/null 2>&1; then
        benchstat="$(go env GOPATH)/bin/benchstat"
        [ -x "$benchstat" ] || die "benchstat not found: go install golang.org/x/perf/cmd/benchstat@latest"
    fi
    echo "== syscall-burst (benchstat: old -> new; '~' means no significant change) =="
    "$benchstat" "$old" "$new"
}

main() {
    local cmd=${1:-}
    [ $# -gt 0 ] && shift
    case "$cmd" in
    record) record "$@" ;;
    compare) compare "$@" ;;
    *) die "usage: $0 record [label] | compare OLD NEW" ;;
    esac
}

main "$@"
