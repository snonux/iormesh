/*
 * Throwaway (S1, task xa4): how well can user space (the Go tracer)
 * measure boot - monotonic? Reads mono, boot, mono back to back; the
 * offset estimate is boot - midpoint(mono), its uncertainty half the
 * mono gap. The true offset is a constant between suspends (BOOTTIME
 * is MONOTONIC plus the accumulated suspend time), so the spread of the
 * estimates is pure sampling error.
 * Build: cc -O2 -o offset offset.c. Prints one summary line.
 */
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

static long long now(clockid_t id)
{
	struct timespec ts;

	clock_gettime(id, &ts);
	return (long long)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

static int cmp(const void *a, const void *b)
{
	long long x = *(const long long *)a, y = *(const long long *)b;

	return (x > y) - (x < y);
}

int main(int argc, char **argv)
{
	int n = argc > 1 ? atoi(argv[1]) : 100000;
	long long *off = calloc(n, sizeof(*off));
	long long *gap = calloc(n, sizeof(*gap));

	if (!off || !gap)
		return 1;
	for (int i = 0; i < n; i++) {
		long long m1 = now(CLOCK_MONOTONIC);
		long long b = now(CLOCK_BOOTTIME);
		long long m2 = now(CLOCK_MONOTONIC);

		off[i] = b - (m1 + m2) / 2;
		gap[i] = m2 - m1;
	}
	qsort(off, n, sizeof(*off), cmp);
	qsort(gap, n, sizeof(*gap), cmp);
	printf("samples=%d offset_ns min=%lld p50=%lld max=%lld "
	       "spread_p1_p99=%lld gap_ns p50=%lld p99=%lld max=%lld\n",
	       n, off[0], off[n / 2], off[n - 1],
	       off[n * 99 / 100] - off[n / 100],
	       gap[n / 2], gap[n * 99 / 100], gap[n - 1]);
	free(off);
	free(gap);
	return 0;
}
