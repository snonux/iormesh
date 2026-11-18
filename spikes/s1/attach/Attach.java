import com.sun.tools.attach.VirtualMachine;

/**
 * Throwaway (S1, task xa4): live attach for decision #2 — loads the S1
 * agent into a running JVM through the JDK's attach API (jdk.attach), the
 * same handshake jcmd uses: an .attach_pid file plus SIGQUIT wake the
 * target's attach listener, then a Unix socket /tmp/.java_pid<pid>
 * carries the "load instrument" command.
 *
 * Usage: java Attach.java <pid> <agent.jar> <agent args>
 */
public final class Attach {
    public static void main(String[] args) throws Exception {
        long t0 = System.nanoTime();
        VirtualMachine vm = VirtualMachine.attach(args[0]);
        long t1 = System.nanoTime();
        try {
            vm.loadAgent(args[1], args[2]);
        } finally {
            vm.detach();
        }
        long t2 = System.nanoTime();
        System.out.printf("attached in %d ms, agent loaded in %d ms%n",
                (t1 - t0) / 1_000_000, (t2 - t1) / 1_000_000);
    }
}
