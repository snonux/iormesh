package ctl

import (
	"fmt"
	"io"
)

// The full usage text iormeshctl prints for -h, help, no arguments and a
// command-line error, held to every subcommand and flag by
// TestUsageNamesEverySubcommandAndFlag. It is data, not code: four
// constants, one per section, so usage stays a one-line function (task
// qk4). They are constants, not an embedded file, because
// TestCtlPrintsAreTerminalSafe (internal/buildgate) passes a constant
// printed through fmt as it is, while a variable would have to go through
// safe, which quotes text holding newlines.

// usageSynopsis is the one-line synopsis of each subcommand.
const usageSynopsis = `usage:
  iormeshctl add -project NAME (-pattern GLOB | -listen-port N) [-registry FILE] HOST [TRACER-FLAG...]
  iormeshctl rm [-project NAME] [-registry FILE] [-store DIR] [-force] [K8S] HOST|POD
  iormeshctl collect [-project NAME]... [-registry FILE] [-store DIR] [-db FILE] [K8S] [HOST|POD...]
  iormeshctl deploy [-project NAME] [-registry FILE] [-dist DIR] [-key FILE] [-restart]
                    [K8S] [-image IMG] [-selinux-type TYPE] [-selinux-level LEVEL]
                    [-attach-timeout D] HOST|POD...
  iormeshctl bootstrap [-apply] [-registry FILE] [-dist DIR] [-key FILE] [-arch ARCH]
                       [-user NAME] HOST
  iormeshctl status [-project NAME]... [-registry FILE] [K8S] [HOST|POD...]
  iormeshctl ensure [-project NAME]... [-registry FILE] [-store DIR] [HOST|POD...]
  iormeshctl list [-registry FILE]
  iormeshctl build [-project NAME]... [-store DIR] [-db FILE] [-builds DIR] [-routes FILE]
                   [-registry FILE] [-namespace NS] [-from T] [-to T] [-last DUR]
  iormeshctl prune -older-than DUR [-project NAME]... [-store DIR] [-db FILE] [-apply]
  iormeshctl refresh [-project NAME]... [-registry FILE] [-store DIR] [-db FILE] [-builds DIR]
                     [-routes FILE] [K8S] [HOST|POD...]
  iormeshctl show [VIEW] [-project NAME]... [-store DIR] [-db FILE] [-builds DIR] [-json] [-unresolved]
                  [-namespace NS] [-windowed]
      VIEW: summary (default) | services | edges | health | conversations
            | sequence [ID] | thread [SERVICE/THREAD] | processes | report
  iormeshctl open [-project NAME]... [-store DIR] [-db FILE] [-builds DIR]
                  [-namespace NS] [-windowed]
  iormeshctl tui [-project NAME]... [-store DIR] [-db FILE] [-builds DIR]
                 [-namespace NS] [-windowed]     (needs a terminal)
  iormeshctl expand [-project NAME]... [-registry FILE] [-store DIR] [-db FILE] [-hops N] [-json]
                    [-apply [-dist DIR] [-key FILE]]
  iormeshctl query [-project NAME]... [-store DIR] [-db FILE] [-from T] [-to T]
                   [-window-segments] [-host GLOB] [-app GLOB] [-thread GLOB]
                   [-conn ID [-follow]] [-file GLOB] [-method GLOB] [-ref REF]
                   [-event KIND] [-pid PID]
                   [-errno NAME|N] [-otel-trace ID] [-max-bytes N]
  iormeshctl services [list] [-db FILE] [-project NAME] [-paths]
  iormeshctl services edges [-db FILE] [-project NAME] [-from NAME]
  iormeshctl services name [-db FILE] [-port N] HOST-GLOB SERVICE
  iormeshctl services rules [-db FILE]
  iormeshctl services unname [-db FILE] RULE-ID
  iormeshctl routes discover [-registry FILE] [-cluster NAME] [-namespace NS]... [-db FILE]
                            [-ingress-service NS/NAME]
  iormeshctl verify [-key FILE] FILE...      (FILE.minisig next to each)
  iormeshctl sign -key SECRET-KEY [-version V] DIR
  iormeshctl version
`

// usageCommands is the paragraph on the default paths and what each
// subcommand does beyond its synopsis.
const usageCommands = `
The registry defaults to ~/.config/iormesh/servers.yaml, the pinned
release key to ~/.config/iormesh/minisign.pub, the service database to
~/.local/share/iormesh/services.db, the trace store to
~/.local/share/iormesh/traces (<project>/<host>/), the build outputs to
~/.local/share/iormesh/builds (<project>[+<project>...]/graph.json,
model.json, report.json and sites/<run>/index.html, one site folder per
run, kept until deleted), dist/ to ./dist. open prints the newest site's
path and opens it with xdg-open (open on macOS) when there is one.
deploy only upgrades a tracer already installed on the host
(docs/decisions.md #1); bootstrap is the first install of a registered
host with your own sudo: it prints the exact steps (for -arch, default
amd64, and the SSH user -user, default HOST's user@ part or yours; a User in
~/.ssh/config is not seen) and contacts nothing, or with -apply
runs them over ssh with sudo -n, which needs sudo without a password.
rm's uninstall removes the sudoers rule bootstrap installed. sign
signs the release builds in DIR (an unpacked release archive or dist/)
with your own minisign secret key (mode 0600, password from
$IORMESH_MINISIGN_PASSWORD or the terminal, never echoed), each at
-version (default this iormeshctl's), which must be its own stamp;
verify, deploy and bootstrap then accept them under your pinned key.
status asks every target's tracer (sudo iormesh status, or
iormesh ready -status in a pod) and prints one row per project; list
prints the registry. ensure, per host (every registered host and pod
entry without HOST|POD), starts again every session that ended on its own (duration,
targets gone, error, reboot, crash), never one stopped with iormesh
stop, then collects; a disk_limit end restarts only once the collect
emptied its ready/. It never upgrades and starts no project the host
never ran, so it is safe from a timer (docs/architecture.md). In a pod
entry's pods it starts the next debug tracer, as deploy does, where
the last one was killed (a non-zero exit), never one that exited 0
(stopped on purpose), then collects. collect without HOST collects every registered host; rm
stops, syncs off and uninstalls before it unregisters, and refuses to
uninstall over traces it could not sync unless -force. build (and
refresh, which is collect then build) merges every project in the store
without -project, never contacts a server, and exits 1 when it had to
leave a damaged segment out. show, open and tui read a build and
never the segments: -project picks the build of exactly those projects
(default: of every project in the store, which -store names), as build
-project wrote it; -db is accepted and unused. build -namespace NS
keeps only the sessions of pods in Kubernetes namespace NS (and the
services and edges their connections make) and writes
<project>[+<project>...]@NS/, which show, open and tui -namespace NS
read. build -from, -to (T as query's) or -last DUR builds only the
segments whose time range overlaps that window, into
<project>[+<project>...][@NS]~window/, which show, open and tui
-windowed read (its site and TUI title name the window); the full
build stays. Its services and edges are
the connections seen in the window (a connection open across it shows
its whole lifetime), not the database's whole history. prune lists the
store's segment files whose range ended more than DUR ago and deletes them only
with -apply, which first has the service database forget them (their
request counts, and the connections seen only in them; naming rules stay)
and deletes nothing when that fails; it never touches a file it cannot
parse as a segment, a dot file or a symlink, and nothing is ever pruned on
its own. -ref on query is a
conversation step's ref from show sequence. expand lists the unresolved peers of the
store's merged mesh against the registry's expand: allowlist, hop limit
(-hops, default 1) and max_nodes; it contacts no server and deploys
nothing unless -apply, which registers each candidate (one per address,
bound to its listening port) and deploys to it as deploy does; one
-apply is one round: refresh and expand -apply again for the next.
query reads the store as build does and writes the matching
events as schema NDJSON, at most -max-bytes (default 1 MiB) of it; a cut
slice ends in a truncation record. T is RFC 3339, YYYY-MM-DDTHH:MM:SS[.frac]
UTC, or HH:MM:SS[.frac] UTC on the one UTC date the store's segments
start on — refused when they start on more than one, for build and
query alike: give the date then; -to is exclusive. query echoes the
resolved window on stderr. A window is exact: query reads the whole
store and cuts the events to it. -window-segments reads only the
segments whose range overlaps the window, as build -from/-to does —
faster on a large store, not exact: an event inside it
written over a minute after -to is missed, a connection opened before
those segments keeps its host-local id, and -app, -file and -follow see
only them. Filters AND; -event takes a schema kind, -errno a Linux errno
name or number, -otel-trace a W3C trace id.
`

// usagePods is the paragraph on Kubernetes targets (docs/milestones.md M7).
const usagePods = `
POD is a registry pods: entry, <cluster>/<namespace>/<pod> or
<cluster>/<namespace>/<selector>; K8S is -context CTX -namespace NS,
overriding the entry's (-context may only restate its cluster's; rm
and a collect or refresh of every target take neither). A selector
reaches every running pod it matches; each pod is collected into
<project>/pod-<uid>/, pinned in the host pins beside -db. deploy
starts a debug container (iormesh run -linger) in each prepared pod,
or checks a sidecar, with the pinned image; rm collects a pod entry
once more and unregisters it, leaving its tracer running.
-selinux-type and -selinux-level set a minimal-profile debug tracer's
SELinux type and MCS level over the registry's (empty omits the field).
`

// usageRoutes is the paragraph on the route map (decision #15).
const usageRoutes = `
The route map (decision #15) is ~/.config/iormesh/routes.yaml (fqdn ->
ingress -> service -> workload, and services with their ClusterIPs and
endpoints), private like the registry; routes discover reads a
registered cluster's Ingresses, Traefik IngressRoutes, Services,
EndpointSlices, pods and ReplicaSets with kubectl get (read-only) into
routes-discovered.json beside the database. build reads both, the file
winning, and prints the routed edges; it never runs kubectl.
`

// usage prints the full usage text to w. fmt.Fprint puts no space
// between two string operands, so the sections join as written.
func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, usageSynopsis, usageCommands, usagePods, usageRoutes)
}
