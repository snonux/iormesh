package kind

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pattern is every pod's tracer target: the workload's JVMs, exec'd by
// the test once the tracer is attached.
const pattern = "*workload.jar*"

// agentFlags add the Java agent to the gateway's tracer (the only
// registry flags: deploy passes the pod's disk bounds itself, task df4): live attach at
// start into the gated JVM. The jar is the tracer image's, the spool is
// on the tracer's emptyDir (the scratch image has no /tmp); the app
// container sees neither, so the tracer stages both inside the JVM's
// root (decision #14, task cf4).
var agentFlags = []string{"-spool=/opt/iormesh/data/spool", "-agent=/opt/iormesh/lib/iormesh-agent.jar"}

// writeRegistry writes the iormeshctl registry for the test's pods (0600
// in a 0700 directory, the registry's rules) and returns its path.
func (c *cluster) writeRegistry(t *testing.T, full bool) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "k8s:\n  image: %s\n  clusters:\n    - name: kind\n      context: %s\n      namespace: %s\npods:\n",
		c.cfg.tracer, c.cfg.context, c.ns)
	entry := func(app, mode string, flags []string) {
		fmt.Fprintf(&b, "  - cluster: kind\n    selector: app=%s\n    container: app\n", app)
		if mode != "" {
			fmt.Fprintf(&b, "    mode: %s\n", mode)
		}
		fmt.Fprintf(&b, "    projects:\n      - target: {pattern: %q}\n", pattern)
		if len(flags) > 0 {
			fmt.Fprintf(&b, "        flags: [%s]\n", quoteAll(flags))
		}
	}
	entry("server", "", nil)
	entry("client", "", nil)
	if full {
		entry("hairpin", "", nil)
		entry("gateway", "", agentFlags)
		entry("inventory", "sidecar", nil)
	}
	path := filepath.Join(c.dir, "servers.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// quoteAll renders flags as a YAML flow list's items.
func quoteAll(flags []string) string {
	q := make([]string, len(flags))
	for i, f := range flags {
		q[i] = fmt.Sprintf("%q", f)
	}
	return strings.Join(q, ", ")
}

// ref is a pod entry's reference on iormeshctl's command line.
func (c *cluster) ref(app string) string { return "kind/" + c.ns + "/app=" + app }

// deploy runs `iormeshctl deploy` for the apps' debug containers. Deploy
// itself returns only once each tracer reported its probes attached
// (task ef4), so the test starts workloads right after it, with no wait
// of its own; deploy's output must say so for every app.
func (c *cluster) deploy(t *testing.T, reg string, apps ...string) map[string]podJSON {
	t.Helper()
	refs := make([]string, len(apps))
	for i, a := range apps {
		refs[i] = c.ref(a)
	}
	out := c.ctl(t, nil, append([]string{"deploy", "-registry", reg}, refs...)...)
	pods := map[string]podJSON{}
	for _, a := range apps {
		p := c.pod(t, a)
		prefix := c.ref(a) + " pod " + p.Metadata.Name + ": started tracer"
		if !slices.ContainsFunc(strings.Split(out, "\n"), func(l string) bool {
			return strings.HasPrefix(l, prefix) && strings.Contains(l, "; attached, session ")
		}) {
			t.Fatalf("deploy did not report %s's tracer attached:\n%s", a, out)
		}
		pods[a] = p
	}
	return pods
}

// assertTracerSpec checks a debug container as the API server stored it
// and the kubelet ran it: exactly #13's capabilities after kubectl's
// profile and the --custom spec merged, nothing privileged, RuntimeDefault
// seccomp, uid 0, the downward-API env and both mounts, the tracer run by
// name (PATH) with the disk bounds deploy took from the pod (task df4:
// 3/4 of the 2Gi emptyDir, no percent limit), and the pinned digest in
// the status imageID (a registry digest, not a kind-loaded config
// digest).
func (c *cluster) assertTracerSpec(t *testing.T, p podJSON) {
	t.Helper()
	i := slices.IndexFunc(p.Spec.Ephemeral, func(s containerSpec) bool { return s.Name == "iormesh" })
	if i < 0 {
		t.Fatalf("pod %s: no iormesh ephemeral container", p.Metadata.Name)
	}
	s := p.Spec.Ephemeral[i]
	sc := s.SecurityContext
	want := []string{"BPF", "PERFMON", "SYS_RESOURCE", "SYS_PTRACE", "DAC_OVERRIDE", "KILL", "CHECKPOINT_RESTORE"}
	if !slices.Equal(sc.Capabilities.Add, want) || !slices.Equal(sc.Capabilities.Drop, []string{"ALL"}) {
		t.Errorf("pod %s: capabilities %+v", p.Metadata.Name, sc.Capabilities)
	}
	if sc.Privileged != nil && *sc.Privileged || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.RunAsUser == nil || *sc.RunAsUser != 0 || sc.Seccomp.Type != "RuntimeDefault" {
		t.Errorf("pod %s: security context %+v", p.Metadata.Name, sc)
	}
	var env, mounts []string
	for _, e := range s.Env {
		if e.ValueFrom != nil {
			env = append(env, e.Name+"="+e.ValueFrom.FieldRef.FieldPath)
		}
	}
	for _, m := range s.Mounts {
		mounts = append(mounts, m.Name+"@"+m.MountPath)
	}
	if !slices.Equal(env, []string{"POD_NAME=metadata.name", "POD_NAMESPACE=metadata.namespace", "POD_UID=metadata.uid"}) ||
		!slices.Equal(mounts, []string{"tracefs@/sys/kernel/tracing", "iormesh-traces@/opt/iormesh/data"}) ||
		len(s.Command) < 5 || s.Command[0] != "iormesh" ||
		!slices.Equal(s.Command[len(s.Command)-4:], []string{"-disk-limit", "0", "-disk-max-bytes", "1536MiB"}) {
		t.Errorf("pod %s: env %v, mounts %v, command %v", p.Metadata.Name, env, mounts, s.Command)
	}
	c.assertImageID(t, p, p.Status.Ephemeral)
}

// assertImageID: the tracer container's status imageID names the
// registry digest the image was pinned by.
func (c *cluster) assertImageID(t *testing.T, p podJSON, statuses []containerStatus) {
	t.Helper()
	digest := c.cfg.tracer[strings.LastIndex(c.cfg.tracer, "@")+1:]
	for _, s := range statuses {
		if s.Name == "iormesh" {
			if !strings.HasSuffix(s.ImageID, "@"+digest) {
				t.Errorf("pod %s: tracer imageID %s, want …@%s", p.Metadata.Name, s.ImageID, digest)
			}
			return
		}
	}
	t.Errorf("pod %s: no tracer container status", p.Metadata.Name)
}
