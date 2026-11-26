# pool.awk: pools 9m4's sample files by variant (and call) from the file
# path (.../<variant>-<call>-<round>/samples.txt or .../<variant>-<round>/),
# prints median CPU ratio, wall ratio, and added CPU ns per syscall.
function median(a, n,   s) {
    if (n == 0) return "-"
    asort(a, s, "@val_num_asc")
    return n % 2 ? s[(n + 1) / 2] : (s[n / 2] + s[n / 2 + 1]) / 2
}
/^mode=/ {
    delete f
    for (i = 1; i <= NF; i++) { split($i, kv, "="); f[kv[1]] = kv[2] }
    n = split(FILENAME, parts, "/"); key = parts[n - 1]; sub(/-[0-9]+$/, "", key)
    calls = (f["mode"] ~ /^k/) ? f["ops"] * f["threads"] : f["ops"] * 2
    if (f["mode"] ~ /untraced$/) { uc = f["burst_cpu_ms"]; ue = f["elapsed_ns"] }
    if (f["mode"] ~ /bystander$/) {
        m = ++cnt[key]
        r[key][m] = f["burst_cpu_ms"] / uc
        w[key][m] = f["elapsed_ns"] / ue
        d[key][m] = (f["burst_cpu_ms"] - uc) * 1e6 / calls
        b[key][m] = uc * 1e6 / calls
        if (r[key][m] > 1.25) over[key]++
    }
}
END {
    for (k in cnt)
        printf "%-18s n=%2d cpu=%.3f wall=%.3f added_ns=%.0f base_ns=%.0f over1.25=%d\n", k, cnt[k],
            median(r[k], cnt[k]), median(w[k], cnt[k]), median(d[k], cnt[k]), median(b[k], cnt[k]), over[k] + 0
}
