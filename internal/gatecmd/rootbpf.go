package gatecmd

import "strings"

// RootBPFTests are the tests of internal/sock_root_test.go,
// internal/sockseq_root_test.go, internal/loader_root_test.go,
// internal/follow_root_test.go, internal/javamark_root_test.go,
// internal/dispatch_root_test.go, internal/streamseq_root_test.go,
// internal/pidns_root_test.go, internal/sockreuse_root_test.go,
// internal/exit_root_test.go, internal/pidnsconnect_root_test.go,
// internal/badfd_root_test.go, internal/sockclose_root_test.go,
// internal/sockfinal_root_test.go, internal/btf_root_test.go and
// internal/rename_root_test.go: they
// load the real BPF object, attach the TCP programs or the loader's default
// probe set, and read the ring buffer (M1 steps 4 and 5; process-tree
// following and the exec watch, M2 steps 1–2; every event family, M2 step
// 7; native stacks, M2 step 9; the Java mark's uprobes on a running JVM, M3
// task td4; no skipped runs on a shared CPU, task 8e4; the tcp_sock
// sequence fields, M4 task ve4; the data calls' tcp_seq, M4 task xe4, the
// read that consumes the peer's FIN included and one skipping an urgent
// byte excluded, task wi4; a tracer in its own PID namespace, M7 task 4f4;
// a stale socket owner after a lost TCP_CLOSE run, and a pod-mode
// connect's state records, task if4; a thread's and a process's exit told
// apart, the el9 signal->live fallback included, task nf4; a call on fd -1
// before and after a fork and exec, task mf4; the key a CLOSE record
// rebuilds after an RST abort, and a dual-stack bound connect kept on one
// connection, task qf4; the raw_syscalls record relocated against el9 BTF
// on any kernel, the data handlers too since task wi4, and a traced loop
// seen as several syscall kinds, task of4; the final counter
// snapshot of a socket still open at the session's end, task sf4; a
// prctl(PR_SET_NAME) rename reflected in the process's comm, task 0g4). They
// skip for anybody but root, so `mage test` passes them by, and `mage
// bpfRootTest` is the one place that runs them. The names are listed
// rather than matched by a pattern so that a renamed test fails a gate
// (internal/buildgate) instead of silently no longer running. Adapted from
// ior's RootLinkTests (internal/gatecmd/gatecmd.go @ b21afc50328c).
func RootBPFTests() []string {
	return []string{
		"TestSockLoopbackConnectTuple",
		"TestSockNonTargetPidNoEvents",
		"TestSockFailedConnectUnregisters",
		"TestSockResetBeforeAcceptNotRegistered",
		"TestSockSeqFields",
		"TestSockSeqStress",
		"TestSockRefusedConnectNoSeq",
		"TestSockSeqAbortClose",
		"TestSockDualStackBoundConnect",
		"TestSockReuseAfterLostClose",
		"TestSockStaleOwnerOfLiveSocket",
		"TestSockFinalSnapshot",
		"TestLoaderDeliversRecords",
		"TestLoaderTargetsOnlyTheMap",
		"TestLoaderCloseEndsRecords",
		"TestLoaderRefusesEmptyAttach",
		"TestLoaderAttachesEveryFamily",
		"TestKernelSignalHasNoSender",
		"TestNativeStacksOnlyWhenAsked",
		"TestJavaMarkNamesTheNative",
		"TestFollowForkExec",
		"TestUntracedForkNotFollowed",
		"TestExecWatchAdoptAndIgnore",
		"TestExecWatchExitedBeforeAdopt",
		"TestSharedCPUNotSkipped",
		"TestStreamSeqValues",
		"TestStreamSeqConcurrentSenders",
		"TestStreamSeqFolded",
		"TestStreamSeqFinRead",
		"TestStreamSeqUrgentSkipNoSeq",
		"TestPidNSTracerConfined",
		"TestProcessExitGroupDead",
		"TestPidNSConnectEstablished",
		"TestNegativeFdAcrossExec",
		"TestEl9RecordLayout",
		"TestDispatchSeesSeveralKinds",
		"TestRenameReflected",
	}
}

// RootBPFTestArgs are the arguments the test binary of ./internal gets to
// run RootBPFTests, and nothing else, as root. -test.v is not for the reader
// alone: RootBPFTestsNotPassed reads the verdicts it prints.
func RootBPFTestArgs() []string {
	return []string{
		"-test.run", "^(" + strings.Join(RootBPFTests(), "|") + ")$",
		"-test.timeout=5m",
		"-test.count=1",
		"-test.v",
	}
}

// RootBPFTestsNotPassed returns the tests of RootBPFTests that output, the
// -test.v output of the test binary, does not report as passed.
//
// The exit status cannot say so: a binary whose -test.run matches nothing,
// or whose tests all skipped (what they do for anybody but root), exits 0.
// A verdict counts only at the start of a line, where the testing package
// prints that of a top-level test; a subtest's is indented.
func RootBPFTestsNotPassed(output string) []string {
	return notPassed(output, RootBPFTests())
}

// notPassed returns the tests of names that output, the -test.v output of
// a test binary, does not report as passed. Shared by the root BPF tests,
// the integration tests (integration.go) and the kind acceptance runs
// (kind.go): each gate believes only a "--- PASS" line, never the exit
// status.
func notPassed(output string, names []string) []string {
	passed := map[string]bool{}
	for line := range strings.SplitSeq(output, "\n") {
		if rest, ok := strings.CutPrefix(line, "--- PASS: "); ok {
			name, _, _ := strings.Cut(rest, " ")
			passed[name] = true
		}
	}
	var missing []string
	for _, name := range names {
		if !passed[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
