// TCP loopback load for task xe4's cost measurement: one writer thread
// sends N messages of S bytes, one reader thread reads them, both in this
// process. Prints its pid, waits for a line on stdin, runs, prints
// "tcpload ops=N size=S elapsed_ns=E cpu_ms=C" (C: this process's
// user+sys from the gate to the end).
#include <arpa/inet.h>
#include <netinet/in.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>

static long ops, size;
static int srv, pace;

static long cpu_ms(void) {
   struct rusage ru;
   getrusage(RUSAGE_SELF, &ru);
   return (ru.ru_utime.tv_sec + ru.ru_stime.tv_sec) * 1000 +
          (ru.ru_utime.tv_usec + ru.ru_stime.tv_usec) / 1000;
}

static void *reader(void *arg) {
   char buf[65536];
   long left = ops * size;
   (void)arg;
   while (left > 0) {
      ssize_t n = read(srv, buf, size < left ? size : left);
      if (n <= 0) { perror("read"); exit(1); }
      left -= n;
   }
   return NULL;
}

int main(int argc, char **argv) {
   struct sockaddr_in a = {.sin_family = AF_INET};
   socklen_t len = sizeof(a);
   char buf[65536] = {0}, line[16];
   struct timespec t0, t1;
   pthread_t th;
   long c0;
   int ln, cli;

   ops = argc > 1 ? atol(argv[1]) : 200000;
   size = argc > 2 ? atol(argv[2]) : 64;
   pace = argc > 3 ? atoi(argv[3]) : 0;
   a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
   ln = socket(AF_INET, SOCK_STREAM, 0);
   if (bind(ln, (void *)&a, sizeof(a)) || listen(ln, 1) || getsockname(ln, (void *)&a, &len)) {
      perror("listen");
      return 1;
   }
   printf("%d\n", getpid());
   fflush(stdout);
   if (!fgets(line, sizeof(line), stdin))
      return 1;
   cli = socket(AF_INET, SOCK_STREAM, 0);
   if (connect(cli, (void *)&a, sizeof(a)) || (srv = accept(ln, NULL, NULL)) < 0) {
      perror("connect");
      return 1;
   }
   c0 = cpu_ms();
   clock_gettime(CLOCK_MONOTONIC, &t0);
   pthread_create(&th, NULL, reader, NULL);
   for (long i = 0; i < ops; i++) {
      if (write(cli, buf, size) != size) { perror("write"); return 1; }
      // Paced (argv[3] != 0): a 1 ms pause every 100 writes keeps the
      // unfolded trace under what one consumer takes, so nothing is lost
      // and the per-event figures are clean.
      if (pace && i % 100 == 99)
         usleep(1000);
   }
   pthread_join(th, NULL);
   clock_gettime(CLOCK_MONOTONIC, &t1);
   printf("tcpload ops=%ld size=%ld elapsed_ns=%ld cpu_ms=%ld\n", ops, size,
          (t1.tv_sec - t0.tv_sec) * 1000000000L + t1.tv_nsec - t0.tv_nsec, cpu_ms() - c0);
   return 0;
}
