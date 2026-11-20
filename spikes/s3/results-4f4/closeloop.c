// closeloop N: N close(-1) calls (a traced kind, EBADF), then prints the
// process's user+sys CPU time per call in ns. A bystander micro-benchmark:
// every call meets the dispatcher and the close handler's filter.
#include <stdio.h>
#include <stdlib.h>
#include <sys/resource.h>
#include <unistd.h>

int main(int argc, char **argv) {
    long n = argc > 1 ? atol(argv[1]) : 2000000;
    struct rusage ru;

    for (long i = 0; i < n; i++)
        close(-1);
    getrusage(RUSAGE_SELF, &ru);
    double us = ru.ru_utime.tv_sec * 1e6 + ru.ru_utime.tv_usec + ru.ru_stime.tv_sec * 1e6 + ru.ru_stime.tv_usec;
    printf("%.1f\n", us * 1000 / n);
    return 0;
}
