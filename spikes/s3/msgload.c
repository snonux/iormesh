// msghdr loads for spikes/s3/measure.sh's "msg" mode (task ak4): the
// socket calls the default handlers took on after S3 (sendmsg, recvmsg,
// sendmmsg, recvmmsg with the msghdr peer read; tasks oh4, nk4) and the
// processes a `run -agent` session's late attacher looks at (task lh4).
// Plain C, like getppid.c: the JDK has no sendmmsg/recvmmsg and no
// netlink, and a C loop leaves the per-call kernel cost as the larger part
// of the CPU time.
//
// Usage: msgload LOAD THREADS OPS [BATCH]
//   udp-mmsg  per op, one sendmmsg of BATCH (default 64) 64-byte datagrams
//             from an unconnected UDP socket to a second one on 127.0.0.1
//             (msg_name set, so a traced call carries the peer), then
//             recvmmsg until all BATCH are back (msg_name set too): the
//             batching a QUIC or DNS server does, and the mmsg exits'
//             per-message reads. A BATCH over the receiver's buffer
//             (~250 at the default rmem) drops the rest; they are counted
//             in dropped, not waited for.
//   netlink   per op, one RTM_GETLINK dump on a NETLINK_ROUTE socket:
//             sendmsg of the request, recvmsg until NLMSG_DONE — what
//             `ip -s link` does. Every call is on a non-stream socket, so
//             a traced one also sends its address record (task nk4).
//   exec      per op, fork and exec `ip -s link` and `ss -s` (stdout to
//             /dev/null) and wait for each: the netlink load as a host
//             runs it, one short-lived process per command, so a traced
//             `run -agent` session's late attacher sees every exec.
//             THREADS must be 1.
//
// When $IORMESH_WORKLOAD_STARTUP_FILE is set, the program prints its PID
// on a line of its own, waits for that file to exist (so a tracer can
// attach by -pid first) and only then starts its loops, as the Java
// workload does. It prints one line:
//   msgload load=L threads=T ops=N batch=B calls=C dropped=D elapsed_ns=W cpu_ms=M
// calls is the socket calls (or execs) made, dropped the datagrams
// udp-mmsg's receiver never got, elapsed_ns the loops' wall
// time and cpu_ms the user+sys CPU time from the gate on (getrusage of
// the process and of its waited-for children; exec's commands count).
#define _GNU_SOURCE
#include <errno.h>
#include <linux/netlink.h>
#include <linux/rtnetlink.h>
#include <netinet/in.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define MAX_THREADS 256
#define MAX_BATCH 1024 // UIO_MAXIOV, the kernel's vlen limit
#define DGRAM 64

static const char *load;
static long ops;
static int batch = 64;
static long udp_dropped;

static void die(const char *what) {
    perror(what);
    exit(1);
}

static long long now_ns(void) {
    struct timespec ts;

    clock_gettime(CLOCK_MONOTONIC, &ts);
    return ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

// cpu_ms is the process's and its waited-for children's user+sys time.
static long long cpu_ms(void) {
    struct rusage s, c;

    getrusage(RUSAGE_SELF, &s);
    getrusage(RUSAGE_CHILDREN, &c);
    return (s.ru_utime.tv_sec + s.ru_stime.tv_sec + c.ru_utime.tv_sec +
            c.ru_stime.tv_sec) * 1000LL +
           (s.ru_utime.tv_usec + s.ru_stime.tv_usec + c.ru_utime.tv_usec +
            c.ru_stime.tv_usec) / 1000;
}

// udp_pair opens the receiver, bound to an ephemeral port on 127.0.0.1
// (its address in *to), and the unconnected sender.
static void udp_pair(int *rx, int *tx, struct sockaddr_in *to) {
    socklen_t len = sizeof(*to);

    memset(to, 0, sizeof(*to));
    to->sin_family = AF_INET;
    to->sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    *rx = socket(AF_INET, SOCK_DGRAM, 0);
    *tx = socket(AF_INET, SOCK_DGRAM, 0);
    if (*rx < 0 || *tx < 0)
        die("socket");
    if (bind(*rx, (struct sockaddr *)to, sizeof(*to)) != 0 ||
        getsockname(*rx, (struct sockaddr *)to, &len) != 0)
        die("bind");
}

// udp_drain receives what one sendmmsg queued, with recvmmsg calls until
// batch datagrams are back or the queue is empty, and returns the calls
// made. Loopback delivers during sendmmsg itself, so an empty queue means
// the rest was dropped (a batch over the receive buffer, ~250 datagrams
// at the default rmem); MSG_DONTWAIT keeps such a drop from blocking the
// loop, and the dropped datagrams are counted in udp_dropped.
static long udp_drain(int rx, struct mmsghdr *in, struct iovec *iio,
                      struct sockaddr_in *from) {
    long calls = 0;

    for (int got = 0; got < batch; calls++) {
        for (int i = got; i < batch; i++)
            in[i].msg_hdr = (struct msghdr){.msg_name = &from[i],
                                            .msg_namelen = sizeof(from[i]),
                                            .msg_iov = &iio[i], .msg_iovlen = 1};
        int n = recvmmsg(rx, in + got, batch - got, MSG_DONTWAIT, NULL);
        if (n < 0 && errno == EAGAIN) {
            __atomic_add_fetch(&udp_dropped, batch - got, __ATOMIC_RELAXED);
            return calls + 1;
        }
        if (n <= 0)
            die("recvmmsg");
        got += n;
    }
    return calls;
}

// udp_loop sends and receives batch datagrams per op; it returns the
// socket calls made.
static long udp_loop(void) {
    static __thread struct mmsghdr out[MAX_BATCH], in[MAX_BATCH];
    static __thread struct iovec oio[MAX_BATCH], iio[MAX_BATCH];
    static __thread struct sockaddr_in from[MAX_BATCH];
    static __thread char obuf[MAX_BATCH][DGRAM], ibuf[MAX_BATCH][DGRAM];
    struct sockaddr_in to;
    int rx, tx;
    long calls = 0;

    udp_pair(&rx, &tx, &to);
    for (int i = 0; i < batch; i++) {
        oio[i] = (struct iovec){obuf[i], DGRAM};
        iio[i] = (struct iovec){ibuf[i], DGRAM};
        out[i].msg_hdr = (struct msghdr){.msg_name = &to, .msg_namelen = sizeof(to),
                                         .msg_iov = &oio[i], .msg_iovlen = 1};
    }
    for (long op = 0; op < ops; op++) {
        if (sendmmsg(tx, out, batch, 0) != batch)
            die("sendmmsg");
        calls++;
        calls += udp_drain(rx, in, iio, from);
    }
    close(rx);
    close(tx);
    return calls;
}

// nl_request sends one RTM_GETLINK dump request with sendmsg.
static void nl_request(int fd, unsigned seq) {
    struct {
        struct nlmsghdr nh;
        struct ifinfomsg ifi;
    } req = {.nh = {.nlmsg_len = sizeof(req), .nlmsg_type = RTM_GETLINK,
                    .nlmsg_flags = NLM_F_REQUEST | NLM_F_DUMP, .nlmsg_seq = seq},
             .ifi = {.ifi_family = AF_UNSPEC}};
    struct sockaddr_nl kernel = {.nl_family = AF_NETLINK};
    struct iovec iov = {&req, sizeof(req)};
    struct msghdr mh = {.msg_name = &kernel, .msg_namelen = sizeof(kernel),
                        .msg_iov = &iov, .msg_iovlen = 1};

    if (sendmsg(fd, &mh, 0) != (ssize_t)sizeof(req))
        die("sendmsg");
}

// nl_dump receives one dump until NLMSG_DONE; it returns the recvmsg
// calls made.
static long nl_dump(int fd) {
    static __thread char buf[32768];
    struct sockaddr_nl from;
    struct iovec iov = {buf, sizeof(buf)};
    long calls = 0;

    for (;;) {
        struct msghdr mh = {.msg_name = &from, .msg_namelen = sizeof(from),
                            .msg_iov = &iov, .msg_iovlen = 1};
        ssize_t n = recvmsg(fd, &mh, 0);
        calls++;
        if (n <= 0)
            die("recvmsg");
        for (struct nlmsghdr *h = (struct nlmsghdr *)buf; NLMSG_OK(h, n);
             h = NLMSG_NEXT(h, n)) {
            if (h->nlmsg_type == NLMSG_DONE)
                return calls;
            if (h->nlmsg_type == NLMSG_ERROR) {
                fprintf(stderr, "msgload: netlink error\n");
                exit(1);
            }
        }
    }
}

static long netlink_loop(void) {
    int fd = socket(AF_NETLINK, SOCK_RAW | SOCK_CLOEXEC, NETLINK_ROUTE);
    long calls = 0;

    if (fd < 0)
        die("socket");
    for (long op = 0; op < ops; op++) {
        nl_request(fd, (unsigned)op + 1);
        calls += 1 + nl_dump(fd);
    }
    close(fd);
    return calls;
}

// run_cmd forks and execs argv with stdout on /dev/null and waits for it.
static void run_cmd(char *const argv[]) {
    int status;
    pid_t pid = fork();

    if (pid < 0)
        die("fork");
    if (pid == 0) {
        if (!freopen("/dev/null", "w", stdout))
            _exit(127);
        execvp(argv[0], argv);
        _exit(127);
    }
    if (waitpid(pid, &status, 0) != pid || !WIFEXITED(status) ||
        WEXITSTATUS(status) != 0) {
        fprintf(stderr, "msgload: %s failed\n", argv[0]);
        exit(1);
    }
}

static long exec_loop(void) {
    static char *const ip[] = {"ip", "-s", "link", NULL};
    static char *const ss[] = {"ss", "-s", NULL};

    for (long op = 0; op < ops; op++) {
        run_cmd(ip);
        run_cmd(ss);
    }
    return 2 * ops;
}

static void *thread_main(void *arg) {
    long *calls = arg;

    if (strcmp(load, "udp-mmsg") == 0)
        *calls = udp_loop();
    else if (strcmp(load, "netlink") == 0)
        *calls = netlink_loop();
    else
        *calls = exec_loop();
    return NULL;
}

// gate prints the PID and waits for $IORMESH_WORKLOAD_STARTUP_FILE, if
// set, polling every millisecond for up to 120 s.
static void gate(void) {
    const char *file = getenv("IORMESH_WORKLOAD_STARTUP_FILE");

    if (!file)
        return;
    printf("%d\n", getpid());
    fflush(stdout);
    for (int i = 0; access(file, F_OK) != 0; i++) {
        if (i == 120000) {
            fprintf(stderr, "msgload: no %s\n", file);
            exit(1);
        }
        usleep(1000);
    }
}

static int parse_args(int argc, char **argv) {
    if (argc < 4 || argc > 5) {
        fprintf(stderr, "usage: msgload udp-mmsg|netlink|exec THREADS OPS [BATCH]\n");
        exit(2);
    }
    load = argv[1];
    int threads = atoi(argv[2]);
    ops = atol(argv[3]);
    if (argc == 5)
        batch = atoi(argv[4]);
    if ((strcmp(load, "udp-mmsg") && strcmp(load, "netlink") && strcmp(load, "exec")) ||
        threads < 1 || threads > MAX_THREADS || ops < 1 || batch < 1 || batch > MAX_BATCH ||
        (strcmp(load, "exec") == 0 && threads != 1)) {
        fprintf(stderr, "msgload: bad LOAD, THREADS, OPS or BATCH\n");
        exit(2);
    }
    return threads;
}

int main(int argc, char **argv) {
    int threads = parse_args(argc, argv);
    pthread_t tid[MAX_THREADS];
    long calls[MAX_THREADS], total = 0;

    gate();
    long long c0 = cpu_ms(), t0 = now_ns();
    for (int i = 0; i < threads; i++)
        if (pthread_create(&tid[i], NULL, thread_main, &calls[i]) != 0)
            die("pthread_create");
    for (int i = 0; i < threads; i++) {
        pthread_join(tid[i], NULL);
        total += calls[i];
    }
    long long elapsed = now_ns() - t0;
    printf("msgload load=%s threads=%d ops=%ld batch=%d calls=%ld dropped=%ld elapsed_ns=%lld "
           "cpu_ms=%lld\n",
           load, threads, ops, strcmp(load, "udp-mmsg") ? 0 : batch, total, udp_dropped, elapsed,
           cpu_ms() - c0);
    return 0;
}
