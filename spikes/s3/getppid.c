// getppid loop for spikes/s3/measure.sh's "kind" mode (task ae4): a
// bystander whose syscall is of a kind no iormesh family traces. Since task
// 8e4 every syscall on the host meets the raw_syscalls dispatchers, so this
// prices what a trace costs the rest of the host's syscalls, not only its
// reads and writes. Plain C rather than the Java workload: getppid is not
// reachable from the JDK, and a C loop leaves the per-call kernel cost as
// the larger part of the CPU time.
//
// Usage: getppid THREADS OPS   (OPS calls per thread)
// Prints one line: getppid threads=T ops=N elapsed_ns=W cpu_ms=C, where
// elapsed_ns is the loops' wall time and cpu_ms the process's user+sys CPU
// time (getrusage; startup is a few hundred microseconds of it).
#define _GNU_SOURCE
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/resource.h>
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>

static long ops;

// loop calls getppid through syscall(2), so no libc wrapper could ever
// answer from a cache.
static void *loop(void *arg) {
    (void)arg;
    for (long i = 0; i < ops; i++)
        syscall(SYS_getppid);
    return NULL;
}

static long long now_ns(void) {
    struct timespec ts;

    clock_gettime(CLOCK_MONOTONIC, &ts);
    return ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

static long long cpu_ms(void) {
    struct rusage ru;

    getrusage(RUSAGE_SELF, &ru);
    return (ru.ru_utime.tv_sec + ru.ru_stime.tv_sec) * 1000LL +
           (ru.ru_utime.tv_usec + ru.ru_stime.tv_usec) / 1000;
}

int main(int argc, char **argv) {
    if (argc != 3) {
        fprintf(stderr, "usage: getppid THREADS OPS\n");
        return 2;
    }
    int threads = atoi(argv[1]);
    ops = atol(argv[2]);
    if (threads < 1 || threads > 256 || ops < 1) {
        fprintf(stderr, "getppid: bad THREADS or OPS\n");
        return 2;
    }
    pthread_t tid[256];
    long long t0 = now_ns();
    for (int i = 0; i < threads; i++)
        if (pthread_create(&tid[i], NULL, loop, NULL) != 0) {
            perror("pthread_create");
            return 1;
        }
    for (int i = 0; i < threads; i++)
        pthread_join(tid[i], NULL);
    printf("getppid threads=%d ops=%ld elapsed_ns=%lld cpu_ms=%lld\n", threads,
           ops, now_ns() - t0, cpu_ms());
    return 0;
}
