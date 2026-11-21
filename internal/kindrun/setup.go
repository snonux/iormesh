package kindrun

import (
	"fmt"
	"os"
	"strings"
)

// RegistryUp starts the registry with its named data volume. A running
// one is kept; a stopped one of the same name (a killed earlier run) is
// removed first, else `docker run --name` would refuse.
func RegistryUp(r Runner, c Config) error {
	state, err := r.Output(docker("container", "inspect", "--format={{.State.Running}}", c.Registry)...)
	switch {
	case err == nil && strings.TrimSpace(state) == "true":
		return nil
	case err == nil:
		if err := r.Run(docker("rm", "--force", "--volumes", c.Registry)...); err != nil {
			return err
		}
	}
	return r.Run(docker("run", "--detach", "--restart=no", "--publish=127.0.0.1:5001:5000",
		"--name="+c.Registry, "--volume="+c.RegistryVolume+":/var/lib/registry", c.RegistryImage)...)
}

// CreateCluster creates the cluster from the kind config file cfg,
// without --kubeconfig: kind runs as root, and root must not write a
// path the operator chose (review N10). kind merges the context into
// root's own kubeconfig, and `kind delete cluster` takes it out again.
func CreateCluster(r Runner, c Config, cfg string) error {
	return r.Run(kind("create", "cluster", "--name="+c.Cluster, "--config="+cfg)...)
}

// WriteKubeconfig writes the cluster's kubeconfig, as kind prints it, to
// c.Kubeconfig as the calling user (0600; created or replaced, never
// followed through a symlink planted there).
func WriteKubeconfig(r Runner, c Config) error {
	out, err := r.Output(kind("get", "kubeconfig", "--name="+c.Cluster)...)
	if err != nil {
		return err
	}
	if !strings.Contains(out, "apiVersion:") {
		return fmt.Errorf("kind get kubeconfig: no kubeconfig in its output")
	}
	if err := os.Remove(c.Kubeconfig); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(c.Kubeconfig, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(out); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// BuildArgv is the image build of tag from containerfile and context,
// labelled with the run's label so teardown finds whatever it leaves.
func BuildArgv(c Config, containerfile, tag, context string) []string {
	return docker("build", "--platform=linux/amd64", "--label="+c.Label,
		"--file="+containerfile, "--tag="+tag, context)
}

// HelmArgv are the Traefik install's helm calls with a repository config
// and cache under dir, so the operator's helm repositories stay as they
// were (review N7).
func HelmArgv(c Config, dir, kubeconfig string) [][]string {
	repo := []string{"--repository-config=" + dir + "/repositories.yaml",
		"--repository-cache=" + dir + "/cache"}
	add := append([]string{"helm", "repo", "add"}, repo...)
	add = append(add, "traefik", "https://traefik.github.io/charts")
	install := append([]string{"helm", "upgrade", "--install", "traefik", "traefik/traefik"}, repo...)
	install = append(install, "--version=42.4.1", "--kubeconfig="+kubeconfig,
		"--kube-context="+c.Context, "--namespace=traefik", "--create-namespace",
		"--set=service.spec.type=ClusterIP", "--set=ingressClass.isDefaultClass=true",
		"--wait", "--timeout=5m")
	return [][]string{add, install}
}
