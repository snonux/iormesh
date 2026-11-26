# Kubernetes: controller, manifests, route map

Part of [Testing strategy](../testing.md).

## K8s controller wiring (task af4)

iormeshctl's pod path (decisions.md #18's "Wired" note;
docs/implementation.md M7 step 10) is tested in `mage test`
without a cluster, against fake kubectls on `PATH`:

- `internal/k8s` (`pods_test.go`, `deploy_test.go`): `Select`
  passes the selector as one `--selector=` element, keeps running
  pods only, sorted; no running match is `ErrNoPod`, more than 32
  `ErrTooManyPods`; empty, negative-only, set-based and
  option-like selectors never reach kubectl; a pod name or UID
  not in the API server's form fails; kubectl failing is
  `ErrUnreachable`; a tracer container terminated or without a
  status is `Stopped`, with why. `Deploy` starts a debug container on a
  prepared pod, leaves an ephemeral tracer running the pinned
  digest alone (containerd's and cri-dockerd's `imageID` forms),
  verifies a sidecar, and — after the `get` alone — refuses
  another digest, an earlier debug container whose spec image is
  another digest or a tag, a bare `sha256:` `imageID`, a tag, a
  tracer not
  running, a missing sidecar and an unprepared pod. No image:
  `Resolve` passes, `Debug` and `Deploy` refuse before kubectl
  runs. A held fetch lock refuses a second `Fetch` (`ErrBusy`)
  without running kubectl.
- `internal/federate` (`registry_k8s_test.go`): both sections
  round-trip through `Save`; references, the namespace and
  project defaults; `RemovePod`; refusals at load — unpinned
  image, unknown mode, bad context or cluster name, unknown
  cluster, `pod:` and `selector:` both or neither, a negative or
  empty selector, no namespace anywhere, the tracer's name as the
  app container, two projects in a debug entry, a duplicate
  entry, an unknown key, and a server host shaped like
  `pod-<uid>`.
- `internal/cli` (`linger_test.go`, review B1): a whole run —
  pattern target, short `-duration`, a temp trace dir — on a fake
  tracer (`openTracer`): with `-linger`, once the session ended the
  tracer is closed (in `bpf.Tracer`, every program detached and
  every map freed) and a segment is in `ready/`, yet the run has
  not returned; cancelling (SIGTERM) ends it. Without `-linger` it
  returns. Mutation-checked: skipping the linger fails the test
  ("session returned before it ended"), and so does a `finish`
  that no longer closes the tracer. `ready_test.go`: a project with
  no trace dir fails, one without `ready/` yet is an empty stream.
- `internal/ctl` (`pods_test.go`, its own fake answering per pod
  from files): collect of a pod entry runs `get pod`, then `exec
  … iormesh ready` and the remove — never `true`, `test` or
  `find` — into `<store>/shop/pod-<uid>`, and pins that directory
  to `pod-<uid>` and its FQDN in `hosts.json`; a pod kubectl
  cannot exec into, or that is gone, fails the collect and is
  never reported empty (checked by mutation: dropping the fetch
  error fails the test); a pod without a tracer container is
  "nothing to collect" without an exec; a selector collects each
  match into its own directory, and one matching nothing fails;
  `collect` alone includes pod entries; a tracer container that
  terminated (OOM-killed) fails the collect as "session ended",
  pointing at deploy for a fresh debug tracer (task ah4), with no
  exec tried (mutation-checked); an `-context`
  that is not the entry's cluster's, a malformed one, an unknown
  entry and an unpinned `-image` are refused before kubectl runs,
  and so are `-context`/`-namespace` on rm and on a collect or
  refresh of every target; deploy runs `debug … -- iormesh run
  -project shop -linger -pattern=… -duration 1h` (mutation-checked),
  refuses an unprepared pod naming the prepare patch and a
  registry without an image (collect still works there); rm
  keeps the registry when the last collect fails, drops the entry
  with `-force` or after a good collect, and says the tracer is
  not stopped.
- Debug tracers after an app container restart (task ah4).
  These tests use the fake kubectl and never run kind or Docker.
  `internal/k8s/tracers_test.go`:
  - `TracerName` and `tracerIndex` round-trip `iormesh` through
    `iormesh-8` and refuse look-alikes such as `iormesh-1`,
    `iormesh-02` and `iormesh-9`.
  - A pod whose tracers all terminated (exit 137) gets one
    `debug` call for the next name, with the pinned `--image`,
    `--target` and the disk bounds. This holds with containers
    listed out of order and with look-alike names beside them.
  - A newest tracer that runs is left alone, with no `debug`
    call, and the transport names it. It must carry the pinned
    spec image and `imageID` and the project.
  - Refused, with only the `get` run: a newest tracer still
    waiting or without a status, an earlier one running or
    without a status while the newest ended, and eight ended
    tracers (the cap).
  - After a replacement, the attach probes go to `iormesh-2`.
    The predecessor's `dead` or `ended duration` is waited past
    until `running`. A `dead` that never gives way times out,
    naming it, the chance that the run failed before attaching,
    and `kubectl logs … --container=iormesh-2`. An answer that
    changes from the first one seen (`dead` to `ended`, `ended`
    with a new reason, `ended` to `dead`) fails at the change. A
    replacement whose container exits fails at once. A deploy
    rerun that leaves `iormesh-2` running waits past the
    predecessor's `dead` the same way, while a first tracer's
    `dead` still fails at once.
  - `started=` times (task eh4, `attach_started_test.go`), with
    `iormesh-2` running since 00:00:00 in the pod read.
    `TestWaitAttachedOlderState`: a `dead`, `ended` or
    (reused-pid) `running` state started an hour earlier, or
    1 µs past the 2 s skew window, is waited past until the new
    run's `running`, with one pod read for the whole wait. Its
    timeout names both instants and the container's logs.
    `TestWaitAttachedNewState`: a new run's `ended` (with or
    without a reason) or `dead`, started after the container
    or exactly 2 s before it, fails at the first probe and names
    the logs. So does a new `dead` after the predecessor's
    `dead`, which the state-change check alone would miss. A new
    `running` attaches. `TestWaitAttachedNoContainerStart`: no
    status, or a waiting one, falls back to the state-change
    check and reads the pod again each probe.
    `TestWaitAttachedTimelessAfterOlder`: an answer without a
    time after a timed predecessor's fails as a change.
    `TestWaitAttachedMalformedStarted` fails at once on a time
    that is not one. `TestParseStarted` takes RFC3339 in any
    zone and unix ns, and refuses dates, missing zones, years
    before 2020 or from 2200, signs, overflow, over-long
    fractions, escape bytes and a time after `pending`.
    `TestReadyState` (`internal/cli`) pins the `started=` word
    in UTC with nanoseconds.
  - The `Lister` reports the newest tracer and its own status.
  - `Run` and `Fetch` exec into `Target.Tracer`. `New` refuses a
    malformed tracer name, a sidecar tracer other than
    `iormesh`, and an app container named like a tracer.

  `internal/ctl/pods_test.go`:
  - Collect of a pod with a killed `iormesh` and a running
    `iormesh-2` execs both the fetch and the remove into
    `iormesh-2`. It gets both sessions' segments into the one
    `pod-<uid>` and pins one identity.
  - Deploy starts `iormesh-2` and waits on it, or leaves a
    running `iormesh-2` alone, or fails at the cap without a
    `debug`.
  - The stopped-tracer error tells the operator to run deploy,
    except at the cap (`iormesh-8` stopped), where it says to
    restart the pod or use sidecar mode and never mentions
    deploy.

  `internal/segment` (`TestNextRunKeepsKilledRunsSegments`): a
  writer whose flock is gone without `Close` (a kill) is followed
  by another session's writer in the same dir. The new writer
  sweeps the unsealed files and publishes beside the killed run's
  sealed segment.

  Mutation checks: each of 13 single-line mutations fails at least
  one of these tests. The mutations: the next name repeated, the cap
  off by one, no all-ended check, an ended tracer kept as running,
  `superseded` unset or ignored, the `Lister` picking the oldest
  tracer, `exec` or `debug` naming `iormesh`, tracer-name
  validation dropped, a leading zero accepted, collect ignoring
  `PodState.Container`, and deploy skipping the attach wait after
  a replacement. The review fixes added six more, each failing a
  test too: a rerun not marked superseded, a first tracer marked
  superseded, no state-change check, the first answer not
  remembered, that memory reset per wait, and the cap message
  dropped.


## K8s manifests and pod identity (task vb4)

Unit tests in `mage test`, no cluster and no container engine
(milestones.md M7; docs/implementation.md M7 steps 5, 6 and 8):

- `internal/k8s/manifest_test.go`: `deploy/` must equal what
  `mage k8sManifests` writes (golden; edit the generator, never
  the files). Each file is then decoded as untyped YAML or JSON,
  the way the API server reads it, and the fields asserted:
  `hostUsers: true` in both patches, `shareProcessNamespace:
  true` only in the sidecar's; tracefs as a read-only `hostPath`
  `Directory` and the trace dir as an `emptyDir` with a
  `sizeLimit`; uid 0, `allowPrivilegeEscalation: false`, no
  `privileged`, `RuntimeDefault` seccomp, `drop: [ALL]` and
  exactly #13's seven capabilities, in order;
  `seLinuxOptions` with only `type: spc_t` and `level: s0` on
  the tracer container, and no pod-level `securityContext` in either patch
  (task 5h4); the downward-API
  env (`POD_NAME`, `POD_NAMESPACE`, `POD_UID`); the sidecar's
  image pinned by digest, its args ending in the ¾ byte cap and
  its small requests; and no field in the `--custom` profile
  that kubectl debug's own flags set. `Validate` refuses a tag,
  a malformed digest, an odd size and a command line that is
  not `run`.
- SELinux type (task 5h4): `k8sname.TestSELinuxType` takes a
  bare type name and refuses a full context, a level, upper
  case, a dash and over 128 bytes; `TestResolveSELinuxType`
  pins the precedence — an unset type defers, an empty one set
  higher up wins and omits the field, `spc_t` is the fallback, a
  malformed one is `ErrTransport`; `TestDebugSELinuxType` and
  `assertCustom` check the `--custom` file (the type for the
  minimal profile, none for an empty type or the privileged
  profile); `TestPodDeploySELinuxType` runs `iormeshctl deploy`
  against the fake kubectl with the default, a registry
  `selinux_type`, `-selinux-type` over it and `-selinux-type=`,
  and refuses `spc_t:s0` before any kubectl call; the registry
  tests round-trip an explicit `selinux_type: ""` and refuse a
  malformed one at load.
- SELinux level (task 8h4): `k8sname.TestSELinuxLevel` takes
  `s0` and `s0:cN,cM` (distinct N, M ≤ 1023) and refuses empty,
  a range, `s1`, a lone, a third or a repeated category,
  `c1024`, a leading zero, a full context and stray whitespace;
  `TestResolveSELinuxLevel` pins the same precedence as the
  type (unset defers, empty wins and omits the level, `s0` the
  fallback) and `ErrTransport` for a bad level from a layer or
  a `Target`; `TestDebugSELinuxType` checks the `--custom` file
  for a pinned pair, no `level` key for an empty level and no
  `seLinuxOptions` for an empty type; `TestPodDeploySELinuxLevel`
  runs `iormeshctl deploy` with the default, a `k8s:`
  `selinux_level`, a pod entry's over it, `-selinux-level` over
  both and `-selinux-level=`, and refuses three malformed levels
  before any kubectl call; the registry tests round-trip a
  pinned pair and an explicit `selinux_level: ""` and refuse a
  range, `c1024`, `s1` and a full context at load. Each check
  was mutation-checked.
- Pod byte cap and attach wait (task 5g4):
  `internal/k8s/quantity_test.go` reads every byte-size form
  (`2Gi`, `2G`, `2000M`, a byte count, `2e9`, `1.5Gi`, `.5Ki`)
  to its bytes and refuses a sign, a milli suffix, wrong case,
  spaces, a three-digit exponent, a value over 4 EiB and text
  over 40 bytes; `TestTraceCap` pins min(¾, limit − 96 MiB) and
  no cap under 128Mi; `TestCheckTraceLimit` and
  `TestDeployRefusesUnreadableLimit` refuse such a pod in debug
  mode before any `kubectl debug` and accept it in sidecar
  mode. `TestParseAttach` refuses every `ready -state` answer
  iormesh never writes (an upper-case, short, long or trailing
  session id, escape bytes, an end reason outside `[a-z_]{1,32}`,
  a word after `pending` or `dead` other than `dead`'s
  `started=` time), with no raw control byte in
  the error; `TestWaitAttachedProbeFails` (a remote exit status
  fails after one exec), `TestWaitAttachedInterrupted` (a
  cancel says "interrupted" and wraps `context.Canceled`),
  `TestWaitAttachedSlowProbe` (a 300 ms answer still counts
  after a 50 ms wait; a probe over its own bound is named) and
  `TestDeployRunningTracerProject` (an earlier tracer of
  another project, by `-project` or the namespace default, is
  refused) cover the wait; the timeout tests are back to 150
  and 100 ms. Mutation-checked: loosening the session or reason
  pattern, letting `pending`/`dead` take a word, dropping the
  length cap, the milli refusal, the exponent bound, the
  4 EiB bound, the 96 MiB reserve, the 128Mi floor, or the
  debug-mode check in `checkPrepared` each fails a test.
- `internal/k8s/cgroup_test.go`: `/proc/<pid>/cgroup` paths in
  the formats the kubelet and runtimes write — the systemd driver
  (`kubepods-<qos>-pod<uid_escaped>.slice/cri-containerd-<id>.scope`,
  `crio-<id>.scope`, CRI-O's `…/container` sub-cgroup,
  cri-dockerd's `docker-<id>.scope`) and the cgroupfs driver
  (`kubepods/<qos>/pod<uid>/<id>`, `crio-<id>`), every QoS class,
  a static pod's 32-hex UID, kind's `--cgroup-root=/kubelet`
  nesting inside the node and as seen from the host under the
  node container's scope; whole v1, v2 and hybrid files. Negative
  cases: `0::/` (a private cgroup namespace) and host or plain
  Docker cgroups give `ErrNotInPod`; bad or escaped UIDs, short
  or non-hex container ids, unknown runtimes, CRI-O's conmon, v1
  controllers naming different pods and text not in the kernel's
  format are errors. `cgroup_forged_test.go`: pod-named cgroups
  under `user.slice` or a `user@<uid>.service`, inexact slice
  prefixes and unknown classes give `ErrNotInPod`; a pod slice
  missing its systemd parents, under the wrong parent or a
  foreign root is an error (each case fails with the parent
  check disabled); the in-pod `/../<container>` form yields the
  container id. The strings are built to the formats;
  `TestParseProcCgroupKindCaptures` adds the files captured on
  kind (task yb4): from the host, from the node, the tracer's
  own, and the tracer seen from the app container's private
  cgroup namespace.
- `internal/segment/emptydir_test.go`: the trace dir one level
  inside a 0777 setgid directory (an emptyDir root) is accepted
  and created 0755, while the volume root as the trace dir is
  still refused. Mutation-checked: dropping `trustedDir`'s
  write-bit check fails the refusal, and checking the volume
  root too fails the acceptance. The watchdog's byte cap trips
  at the cap, counts regular files only, and is off at 0.
- `internal/cli/podproject_test.go`: with all three downward-API
  variables set `run`'s project defaults to the namespace
  (decision #21) and `-trace-dir` to `k8s.TraceRoot`; `-project`
  and `-trace-dir` still win, `-out` alone writes NDJSON only;
  none set leaves `-project` required; one or two set, or a
  malformed value (a namespace that is no valid label, a UID
  that is no UID), is refused even with `-project` (task 7f4);
  `-disk-max-bytes` reaches the watchdog.
- Pod identity (task 7f4): `internal/k8s/podidentity_test.go`
  checks `PodFromEnv` and `PodRef.Validate` (none set is no
  pod; partial, forged or malformed values are `ErrPodEnv`;
  RFC 1123 names, the API server's UID form only — a static
  pod's hash UID is refused — and the 190-byte FQDN bound;
  the refusal names the variables to unset),
  `HostID` and `FQDN` equal to the controller's `PodHostID` and
  `PodFQDN` pin, and end to end two pods whose app is pid 7
  with one start tick: their segments, one with an FQDN of
  exactly `MaxFQDN`, pass the controller's segment-name pattern
  and the name parser, go through the controller's `Unpack`
  (as do table names with a 10-digit seq), load under the
  controller's pins (directory `pod-<uid>`), and merge into two
  process nodes; that pod's name is one 117-byte part.
  `TestSegmentNameFitsUnpack`: a 249-byte name unpacks, a
  250-byte one is `ErrStream`. `Check` and `Deploy` refuse a
  static pod's UID, a missing identity and an FQDN over
  `MaxFQDN` (`ErrName`), and a debug deploy beside a sidecar.
  `internal/k8sname`: pod names are strict RFC 1123 subdomains
  (`a..b`, `a-.b` refused) with no per-part cap (a 64-byte part
  and a 253-byte name pass, 254 bytes fail), as Kubernetes'
  `IsDNS1123Subdomain`.
  `internal/cli/hostinfo_test.go`: in a pod the host id and FQDN
  are the pod's with or without a machine-id in the image,
  outside a pod the machine-id's, and a partial or malformed pod
  environment fails even with a machine-id present. Mutations
  (each test fails): host id without the UID, a wider
  `MaxFQDN`, the UID, name or FQDN check dropped, the partial
  set read as no pod, a shorter UID prefix, the pod branch
  skipped, and either one-tracer-per-pod refusal in
  `checkContainers` removed.

Checked on kind by task yb4
([below](m7-acceptance.md#m7-acceptance-on-kind-task-yb4)): the image builds and
runs, pods on one node traced side by side, `kubectl patch` and
`kubectl debug --custom` carry the capabilities, mounts and env,
and the trace dir stays 0755 under `fsGroup`. Not checked: the
oldest runtime whose `RuntimeDefault` lets `bpf` through (only
containerd 2.2.0 was run).


## Route map and ClusterIP join (task xb4)

Unit tests in `mage test`, no cluster (milestones.md M7 "Service-NAT
correlation", "Ingress route mapping", #15;
docs/implementation.md M7 steps 11 and 12):

- `internal/routemap/join_test.go`: two synthetic pods' traces —
  the client dials ClusterIPs, the server sees its own pod
  address and the client's (kube-proxy's DNAT, no SNAT) — go
  through the unchanged M4 merge (`federate.Halves`, `Fuse`): the
  two ClusterIP connections to the traced pod fuse by the ISN
  pair. The joined result matches `testdata/clusterip.golden`:
  the fused edge names the Service and the pod (matched by UID);
  an untraced Service with two endpoints stops at its
  StatefulSet with no pod, one with a sole endpoint names it; the
  FQDN is routed through Traefik (`ingress_test.go`
  `TestRouteNamesSoleTracedPod`: the route's edge names the
  Service's one endpoint when that pod is traced, not for two
  endpoints, an untraced pod or an endpoint without a UID;
  `viewmodel` `TestRoutesNameTracedPod` the same from
  observations); an address in the Service CIDR
  no Service holds is `unknown_service`, a fused pod missing from
  a Service's endpoints `stale_service`, an HTTP host whose route
  names another Service than the one reached
  `route_contradicted`; an address no map names yields nothing.
  `table_test.go`: static over discovered for one FQDN, Service
  name or ClusterIP (each shadowing flagged), wildcards of one
  label only, a nil table, an endpoint-less Service never stale,
  a fused direct-to-pod flow contradicting its HTTP host's
  route, a Host with a port. `ingress_test.go`: a reused pod IP
  (the map's endpoint has another UID than the server end's
  `pod-<uid>`) names no pod and flags `stale_service`; in-cluster
  clients asking for the FQDN through Traefik get the route's
  edge and no contradiction — by Traefik's ClusterIP, by an
  address in the Service CIDR with Traefik's Service unmapped
  (`unknown_service` plus the route), fused to a Traefik pod
  through its ClusterIP or directly — while another pod still
  contradicts. Mutation-checked (each fails a test): the UID
  guard on the address fallback, the ingress short-circuit, the
  route after `unknown_service`, the fused-pod ingress check.
- `internal/routemap/load_test.go`: a hostile `routes.yaml` is
  refused whole — group- or world-writable file or directory,
  over 4 MiB, an unknown key, a second document, an escape
  sequence in an FQDN, a single-label FQDN, a space in an
  ingress, a Service without a namespace, a DaemonSet or
  foreign-namespace workload, a duplicate FQDN, a bad or
  unspecified ClusterIP, no ports, a port past 65535, an
  endpoint pod of another namespace, a forged UID, two Services
  claiming one ClusterIP and port, a non-canonical CIDR, and past
  1024 endpoints. `paths_test.go`: a bad `path_templates` entry
  refuses the file too, and only the static map's templates
  reach the table.
- HTTP path templates (task xh4): `internal/pathtmpl` folds
  numeric, canonical-UUID and 16+-hex segments to `{id}` and
  keeps words and versions (`/api/v2/users`, `cafe`, 15 hex
  digits, near-UUIDs, `report-2024`, the query) verbatim;
  operator templates win, a literal one pins a path.
  `internal/servicedb/paths_test.go`: Observe counts one row per
  template; a source imported again under other keys replaces
  its rows (never counted twice), also rows a fused connection
  took over from a server row; raw rows stored before the
  templating (schema version 3 marks them) fold when read,
  while a stored template — a literal `/items/1` rule,
  `/api/2024/{x}` — is shown as stored. `TestBuildPathTemplates`
  (internal/ctl): the model's top paths show `/orders/{id}`,
  then the route map's `/orders/{order}` over the same
  database, while report.json keeps `/orders/1`.
- `internal/routemap/discover_test.go`: a namespace as kubectl
  lists it becomes `testdata/discovered.golden` — a Deployment's
  Service with its named target port resolved and its not-ready
  endpoint left out, a StatefulSet's, routes from an Ingress and
  a Traefik IngressRoute naming two hosts — while headless,
  ExternalName and UDP-only Services are left out and a host
  split across two Services (`ambiguous_route`), a Service over
  two workloads (`ambiguous_workload`), a forged endpoint
  address, a host with an escape sequence, a TraefikService
  backend, a negated matcher, a 65-port Service and an endpoint
  pod of another namespace, UID dropped (`skipped`), and endpoints
  naming no pod (`ambiguous_workload`, its own text) are flags;
  an Ingress's status address names Traefik's Service as the
  route's `ingress_service`, `-ingress-service` the
  IngressRoutes'; JSON that is not kubectl's fails
  whole. `MatchHosts` takes `Host` only (not `HostSNI`,
  `HostRegexp` or a longer name), in v2's list form too. A
  snapshot round-trips 0600; an unknown field, data after the
  JSON value, a forged context and a writable file are refused.
- `internal/k8s/routes_test.go`: a route get's exact argv
  (context, namespace, `get`, the resource, `--output=json`); an
  unserved resource is `ErrNoResource`; `secrets`, an option-like
  context and a bad namespace never reach kubectl; output past
  the cap is refused.
- `internal/ctl/routes_discover_test.go`, a fake kubectl on
  `PATH`: `routes discover` makes only read-only gets in the
  registered context and namespace, survives the missing Traefik
  CRDs and writes the snapshot; the next build names the Service,
  pod and route from it without running kubectl; a static route
  for the same FQDN wins and is flagged `shadowed`; a snapshot
  whose context the registry no longer lists fails the build; no
  subcommand, an unregistered cluster, a bad namespace, a bad
  `-ingress-service` and a failing kubectl write no snapshot; a
  missing `-routes` file fails the build.
- `internal/ctl/routes_test.go`: a build over a ClusterIP mesh
  with the map beside the registry prints the routed edges
  (`testdata/routes_summary.golden`: the fused edge with its
  pod, the database's StatefulSet with "2 endpoints, pod
  unknown", and checkout → FQDN → Traefik → Service →
  Deployment) and models them; a stale endpoint list flags the
  Service and keeps the fused edge on the server's host; a
  writable or forged map fails the build; `-routes` reads
  another file. The same build's `graph.svg` draws the routed
  edges (`testdata/routes_graph_svg.golden`, task bf4); the
  renderers' own route tests are in `internal/render`,
  `internal/tui` and `internal/site`
  ([visualization.md](../visualization.md), "Routed edges").

Checked on kind by task yb4
([below](m7-acceptance.md#m7-acceptance-on-kind-task-yb4)): a ClusterIP connection
between two traced pods fusing (iptables and nftables), the
hairpin, the M0 ingress scenario through a real Traefik, and
`routes discover` against a real API server as a list-only
identity (Traefik's CRDs under `traefik.io`; the
`traefik.containo.us` group was not installed, so only its
absence was exercised).
