package iormesh.workload;

/**
 * A command-line error: reported with usage, exit code 2. Public so
 * scenarios can reject their own options and roles with the same code
 * as the framework does, instead of failing like a broken run (exit 1).
 */
public final class UsageException extends Exception {
    private static final long serialVersionUID = 1L;

    public UsageException(String message) {
        super(message);
    }
}
