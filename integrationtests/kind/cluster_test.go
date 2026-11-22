package kind

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"
)

// cluster is one test's view of the kind cluster: kubectl in the
// configured context, scoped to the test's namespace, and iormeshctl with
// a registry and store of the test's own.
type cluster struct {
	cfg  config
	ns   string
	dir  string // registry, store, db, builds
	logs *strings.Builder
}

// kubectl runs kubectl in the cluster's context and namespace and returns
// its stdout; a failure fails the test with stderr.
func (c *cluster) kubectl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := c.try(args...)
	if err != nil {
		t.Fatalf("kubectl %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// try is kubectl returning the error instead of failing the test.
func (c *cluster) try(args ...string) (string, error) { return c.tryIn(nil, args...) }

// tryIn is try with stdin as kubectl's standard input (none if nil): how
// the microservices run ships a published certificate into its caller's
// pod (copyFile).
func (c *cluster) tryIn(stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	full := append([]string{"--context=" + c.cfg.context, "--namespace=" + c.ns}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", full...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+c.cfg.kubeconfig)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &cmdError{err: err, stderr: stderr.String()}
	}
	return stdout.String(), nil
}

// cmdError keeps a command's stderr with its exit error.
type cmdError struct {
	err    error
	stderr string
}

func (e *cmdError) Error() string { return e.err.Error() + ": " + strings.TrimSpace(e.stderr) }

// ctl runs iormeshctl with KUBECONFIG set and returns its stdout and
// stderr together; a failure fails the test.
func (c *cluster) ctl(t *testing.T, env []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cfg.ctl, args...)
	cmd.Env = append(append(os.Environ(), "KUBECONFIG="+c.cfg.kubeconfig), env...)
	out, err := cmd.CombinedOutput()
	c.logs.WriteString("$ iormeshctl " + strings.Join(args, " ") + "\n" + string(out))
	if err != nil {
		t.Fatalf("iormeshctl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// apply renders testdata/name with data and applies it.
func (c *cluster) apply(t *testing.T, name string, data any) {
	t.Helper()
	text, err := render(name, data)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.dir, name)
	if err := os.WriteFile(path, text, 0o600); err != nil {
		t.Fatal(err)
	}
	c.kubectl(t, "apply", "--filename="+path)
}

// render executes the template testdata/name with data.
func render(name string, data any) ([]byte, error) {
	tmpl, err := template.ParseFiles(filepath.Join("testdata", name))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, data); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// patch applies a strategic-merge patch file to a Deployment, as the
// operator applies deploy/'s patches.
func (c *cluster) patch(t *testing.T, deployment string, patch []byte) {
	t.Helper()
	path := filepath.Join(c.dir, deployment+"-patch.yaml")
	if err := os.WriteFile(path, patch, 0o600); err != nil {
		t.Fatal(err)
	}
	c.kubectl(t, "patch", "deployment", deployment, "--patch-file="+path)
}

// rollout waits for the Deployments to finish rolling out.
func (c *cluster) rollout(t *testing.T, deployments ...string) {
	t.Helper()
	for _, d := range deployments {
		c.kubectl(t, "rollout", "status", "deployment/"+d, "--timeout=180s")
	}
}

// podJSON is the part of a pod the checks read.
type podJSON struct {
	Metadata struct {
		Name              string  `json:"name"`
		UID               string  `json:"uid"`
		DeletionTimestamp *string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Ephemeral  []containerSpec `json:"ephemeralContainers"`
		Containers []containerSpec `json:"containers"`
	} `json:"spec"`
	Status struct {
		PodIP     string            `json:"podIP"`
		Ephemeral []containerStatus `json:"ephemeralContainerStatuses"`
		Ctrs      []containerStatus `json:"containerStatuses"`
		Conds     []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

type containerSpec struct {
	Name            string   `json:"name"`
	Command         []string `json:"command"`
	SecurityContext struct {
		RunAsUser                *int64 `json:"runAsUser"`
		AllowPrivilegeEscalation *bool  `json:"allowPrivilegeEscalation"`
		Privileged               *bool  `json:"privileged"`
		Capabilities             struct {
			Add  []string `json:"add"`
			Drop []string `json:"drop"`
		} `json:"capabilities"`
		Seccomp struct {
			Type string `json:"type"`
		} `json:"seccompProfile"`
		SELinux *struct {
			Type  string `json:"type"`
			Level string `json:"level"`
		} `json:"seLinuxOptions"`
	} `json:"securityContext"`
	Env []struct {
		Name      string `json:"name"`
		ValueFrom *struct {
			FieldRef struct {
				FieldPath string `json:"fieldPath"`
			} `json:"fieldRef"`
		} `json:"valueFrom"`
	} `json:"env"`
	Mounts []struct {
		Name      string `json:"name"`
		MountPath string `json:"mountPath"`
	} `json:"volumeMounts"`
}

type containerStatus struct {
	Name        string          `json:"name"`
	ImageID     string          `json:"imageID"`
	ContainerID string          `json:"containerID"`
	Ready       bool            `json:"ready"`
	State       json.RawMessage `json:"state"`
}

// pod returns the live (not terminating) pod of a Deployment's app label.
func (c *cluster) pod(t *testing.T, app string) podJSON {
	t.Helper()
	var list struct {
		Items []podJSON `json:"items"`
	}
	if err := json.Unmarshal([]byte(c.kubectl(t, "get", "pods", "--selector=app="+app,
		"--output=json")), &list); err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Items {
		if p.Metadata.DeletionTimestamp == nil {
			return p
		}
	}
	t.Fatalf("no live pod for app=%s", app)
	return podJSON{}
}

// waitLog waits, polling once a second up to limit, until the tracer
// container's log of pod holds want; it returns the log.
func (c *cluster) waitLog(t *testing.T, pod, want string, limit time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		out, _ := c.try("logs", pod, "--container=iormesh")
		if strings.Contains(out, want) {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("pod %s: tracer log lacks %q after %s:\n%s", pod, want, limit, out)
		}
		time.Sleep(time.Second)
	}
}
