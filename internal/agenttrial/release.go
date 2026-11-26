package agenttrial

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"time"
)

// DefaultReleasePort is the controller's port the release is served on
// (127.0.0.1 there), the URL the task prompt names.
const DefaultReleasePort = 18480

// ReleaseServer serves the release archive of the commit under test
// (decision #37, "Path tested"; `mage releaseUntagged`'s directory for an
// untagged commit, #40) to the
// controller as a release page would: an HTTP file server on the laptop's
// loopback, reached from the controller through an SSH reverse tunnel, so
// the VMs need no route to the laptop and the laptop opens no port to any
// network. Only Dir is served, read-only.
type ReleaseServer struct {
	Dir        string
	Shell      *Shell
	Controller string
	// RemotePort is the controller's loopback port (DefaultReleasePort).
	RemotePort int

	srv    *http.Server
	tunnel *exec.Cmd
}

// URL is the release's address as the controller sees it.
func (r *ReleaseServer) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", cmp.Or(r.RemotePort, DefaultReleasePort))
}

// TunnelArgv is the reverse tunnel's ssh command line to the controller
// (checked like any call: the controller must be one of the Shell's
// hosts), forwarding its loopback port to the laptop's local one.
func (r *ReleaseServer) TunnelArgv(local int) ([]string, error) {
	if err := r.Shell.Check(r.Controller, "true"); err != nil {
		return nil, err
	}
	argv := append([]string{cmp.Or(r.Shell.SSH, "ssh")}, r.Shell.Options...)
	return append(argv, "-N", "-T", "-e", "none", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
		"-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", cmp.Or(r.RemotePort, DefaultReleasePort), local),
		"--", r.Controller), nil
}

// Start serves Dir and opens the tunnel; Stop ends both. The tunnel is
// proved from the controller by the harness (CheckReleaseCommand).
func (r *ReleaseServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	r.srv = &http.Server{Handler: http.FileServer(http.Dir(r.Dir)), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = r.srv.Serve(ln) }()
	argv, err := r.TunnelArgv(ln.Addr().(*net.TCPAddr).Port)
	if err != nil {
		r.Stop()
		return err
	}
	r.tunnel = exec.CommandContext(ctx, argv[0], argv[1:]...)
	if err := r.tunnel.Start(); err != nil {
		r.tunnel = nil
		r.Stop()
		return err
	}
	return nil
}

// CheckReleaseCommand fetches the release's SHA256SUMS on the controller:
// the harness runs it after Start, so a broken tunnel fails the
// preparation, not the agent.
func (r *ReleaseServer) CheckReleaseCommand() string {
	return "curl -fsS -o /dev/null " + r.URL() + "SHA256SUMS"
}

// Stop closes the tunnel and the server.
func (r *ReleaseServer) Stop() {
	if r.tunnel != nil && r.tunnel.Process != nil {
		_ = r.tunnel.Process.Kill()
		_ = r.tunnel.Wait()
	}
	if r.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = r.srv.Close()
		}
	}
}
