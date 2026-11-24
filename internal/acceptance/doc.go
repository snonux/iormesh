// Package acceptance drives docs/milestones.md M4's acceptance against two
// real Linux hosts (`mage m4Acceptance HOST1 HOST2`; docs/testing.md, "M4
// acceptance on two real hosts"). The simulated rig in integrationtests/
// proves the same criteria on network namespaces; this package is the
// run on real machines, which only the owner can do.
//
// What it does, in order: a preflight that refuses to start unless both
// hosts are bootstrapped (decision #1: installed tracer, root-owned key,
// the one sudoers rule) and idle; `iormeshctl add` and `deploy` of one
// project per scenario role; the M0/M4 workloads run over SSH; repeated
// `collect` during the live sessions (streaming); `refresh` and offline
// `build`s whose outputs are checked per criterion; service naming; a
// disk-watchdog trip; and `rm` of both hosts. Every criterion is printed
// as PASS, FAIL, SKIP or NOT RUN, never PASS without having run.
//
// Safety rules the code keeps:
//
//   - Only the two named hosts are ever contacted (hostGate); iormeshctl
//     runs with HOME pointed at a private work directory, so its registry
//     holds only those two hosts and the operator's own registry, store
//     and service database are never read or written.
//   - The tracer's lifecycle on the servers (start, stop, upgrade,
//     uninstall) goes only through iormeshctl (that is what is being
//     accepted); the runner itself runs only the workloads (each under
//     `timeout`, stopped with pkill when its scenario fails), read-only
//     probes (ls, test, df, stat, ss, systemctl list-units) and the
//     watchdog's fill file in a directory the SSH user owns.
//   - The one privileged command the runner issues itself is the
//     read-only `sudo -n /opt/iormesh/bin/iormesh status -json`,
//     through the same narrow sudoers rule iormeshctl uses; the rule is
//     otherwise only probed with `sudo -n -l`. The operator's own sudo
//     is never used on a server.
//   - Teardown (rm, stopping leftover roles, removing the run's files)
//     runs on a context that outlives ^C, bounded by cleanupTimeout.
//
// It is cgo-free like iormeshctl, whose packages it uses to read the build
// outputs (internal/schema, internal/viewmodel, internal/normalize).
package acceptance
