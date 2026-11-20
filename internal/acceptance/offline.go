package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"iormesh/internal/viewmodel"
)

// buildFiles are a build's outputs the byte-for-byte check compares.
var buildFiles = []string{"graph.json", "model.json"}

// keepBytes remembers the two-host pair's build outputs for checkOffline.
func (r *Runner) keepBytes() error {
	r.crossBytes = map[string][]byte{}
	dir := r.buildDir([]string{projShop, projBilling})
	for _, f := range buildFiles {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		r.crossBytes[f] = data
	}
	return nil
}

// checkOffline reruns the pair's build with no way to reach a server and
// compares its outputs with the online build's, byte for byte.
func (r *Runner) checkOffline(ctx context.Context) (string, error) {
	if r.crossBytes == nil {
		return "", fmt.Errorf("no earlier build of %s+%s to compare with", projShop, projBilling)
	}
	dir := r.buildDir([]string{projShop, projBilling})
	// Delete the outputs first: equal bytes must come from the offline
	// build rewriting them, not from a build that left them alone.
	for _, f := range buildFiles {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			return "", err
		}
	}
	r.logf("  iormeshctl build -project %s -project %s (offline)", projShop, projBilling)
	_, how, err := r.ctl.Offline(ctx, "build", "-project", projShop, "-project", projBilling)
	if err != nil {
		return "", fmt.Errorf("offline build (%s): %w", how, err)
	}
	for _, f := range buildFiles {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return "", err
		}
		if !bytes.Equal(data, r.crossBytes[f]) {
			return "", fmt.Errorf("%s differs after the offline rebuild (%s)", f, how)
		}
	}
	return "graph.json and model.json identical; network removed by " + how, nil
}

// clockResult reports the clock bound between the two real hosts from
// the two-host pair's build. Real clocks are not skewed on purpose: the
// injected skew is proven on the rig (TestClockSkewRecovered); here the
// criterion is a non-empty, two-sided bound, with its width reported.
func (r *Runner) clockResult(m viewmodel.Model) {
	if len(m.Health.Clocks) == 0 {
		r.res.set(cClock, Fail, "no clock bound: sends and receives carry no tcp_seq "+
			"(the tracer needs bpf_rdonly_cast, kernel >= 6.2)")
		return
	}
	var parts []string
	ok := true
	for _, c := range m.Health.Clocks {
		twoSided := c.Windows - c.OneSided - c.Empty
		ok = ok && c.Empty == 0 && twoSided > 0
		parts = append(parts, fmt.Sprintf("%s~%s: %d windows (%d one-sided, %d empty), width %s..%s",
			c.HostA, c.HostB, c.Windows, c.OneSided, c.Empty, c.MinWidth.Round(time.Microsecond),
			c.MaxWidth.Round(time.Microsecond)))
	}
	status := Pass
	if !ok {
		status = Fail
	}
	r.res.set(cClock, status, "%s", strings.Join(parts, "; "))
}

// Service names the naming check gives (any name works; these say where
// they come from).
const (
	nameBilling = "m4-billing-svc"
	nameOrders  = "m4-orders-svc"
)

// checkNaming names two services through `iormeshctl services name` —
// the two-host server by its address, the orders service by 127.0.0.1 —
// rebuilds offline, and finds both names in the model's services and
// edges and in `services` output.
func (r *Runner) checkNaming(ctx context.Context) (string, error) {
	rules := [][]string{
		{"services", "name", "-port", strconv.Itoa(r.cfg.port(portTwoHost)), r.info[r.cfg.Host2].addr, nameBilling},
		{"services", "name", "-port", strconv.Itoa(r.cfg.port(portOrders)), "127.0.0.1", nameOrders},
	}
	for _, args := range rules {
		if _, err := r.ctlRun(ctx, args...); err != nil {
			return "", err
		}
	}
	m, _, err := r.build(ctx, r.meshProjects()...)
	if err != nil {
		return "", err
	}
	list, err := r.ctlRun(ctx, "services")
	if err != nil {
		return "", err
	}
	for _, name := range []string{nameBilling, nameOrders} {
		if err := namedInModel(m, name); err != nil {
			return "", err
		}
		if !strings.Contains(list, name) {
			return "", fmt.Errorf("`iormeshctl services` does not list %s", name)
		}
	}
	return "both named in services, edges and `iormeshctl services`; no file edited by hand", nil
}

// namedInModel fails unless name is a named service with an edge to it.
func namedInModel(m viewmodel.Model, name string) error {
	named := false
	for _, s := range m.Services {
		named = named || (s.Name == name && s.Named)
	}
	edge := false
	for _, e := range m.Edges {
		edge = edge || e.To == name
	}
	if !named || !edge {
		return fmt.Errorf("%s: named service %t, edge to it %t", name, named, edge)
	}
	return nil
}
