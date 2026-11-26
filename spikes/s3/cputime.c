// cputime: the part of GNU time that spikes/s3/measure.sh uses, for a
// host without /usr/bin/time (Rocky 9 minimal, task ak4). It runs a
// command, waits for it and writes its user and system CPU seconds, as
// `time -f '%U %S' -o FILE` does, to FILE. Static, so one build from the
// laptop runs on any x86-64 Linux; measure.sh takes it as S3_TIME.
//
// Usage: cputime -f '%U %S' -o FILE COMMAND [ARG...]
// Any other -f format is refused. The exit status is the command's (128+N
// for a signal N), as with GNU time.
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/wait.h>
#include <unistd.h>

int main(int argc, char **argv) {
    if (argc < 6 || strcmp(argv[1], "-f") || strcmp(argv[2], "%U %S") ||
        strcmp(argv[3], "-o")) {
        fprintf(stderr, "usage: cputime -f '%%U %%S' -o FILE COMMAND [ARG...]\n");
        return 2;
    }
    pid_t pid = fork();
    if (pid < 0) {
        perror("fork");
        return 1;
    }
    if (pid == 0) {
        execvp(argv[5], argv + 5);
        perror(argv[5]);
        _exit(127);
    }
    int status;
    struct rusage ru;
    if (wait4(pid, &status, 0, &ru) != pid) {
        perror("wait4");
        return 1;
    }
    FILE *f = fopen(argv[4], "w");
    if (!f) {
        perror(argv[4]);
        return 1;
    }
    fprintf(f, "%ld.%02ld %ld.%02ld\n", (long)ru.ru_utime.tv_sec,
            (long)ru.ru_utime.tv_usec / 10000, (long)ru.ru_stime.tv_sec,
            (long)ru.ru_stime.tv_usec / 10000);
    fclose(f);
    return WIFEXITED(status) ? WEXITSTATUS(status) : 128 + WTERMSIG(status);
}
