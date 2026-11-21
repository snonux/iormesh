package kindrun

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MinFreeGiB is the disk guard's floor: images and the node take a few
// GiB on Docker's data root.
const MinFreeGiB = 20

// DiskGuard checks the filesystem of Docker's data root (where the node,
// the images and the registry volume land — not the repository's):
// at least MinFreeGiB free, and on btrfs no metadata within 0.5 GiB of
// its allocation while under 2 GiB is unallocated (the filesystem could
// not grow metadata). It never balances. The returned notice says what
// could not be checked; the run goes on with it printed (review N9).
func DiskGuard(r Runner) (notice string, err error) {
	root, err := r.Output(docker("info", "--format={{.DockerRootDir}}")...)
	if err != nil {
		return "", fmt.Errorf("disk guard: docker info: %w", err)
	}
	root = strings.TrimSpace(root)
	out, err := r.Output("df", "-B1G", "--output=avail", root)
	if err != nil {
		return "", fmt.Errorf("disk guard: df %s: %w", root, err)
	}
	fields := strings.Fields(out)
	free, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return "", fmt.Errorf("disk guard: df %s: %q", root, out)
	}
	if free < MinFreeGiB {
		return "", fmt.Errorf("disk guard: %s has %d GiB free, need %d", root, free, MinFreeGiB)
	}
	return btrfsGuard(r, root)
}

// btrfsGuard is DiskGuard's btrfs half. When the btrfs tools cannot run
// (another filesystem, no sudo rule) or print a format it does not know,
// it says so in the notice instead of passing silently.
func btrfsGuard(r Runner, root string) (string, error) {
	df, err := r.Output("sudo", "-n", "btrfs", "filesystem", "df", root)
	if err != nil {
		return "disk guard: btrfs metadata not checked (sudo -n btrfs filesystem df failed: not btrfs, or no sudo rule)", nil
	}
	usage, err := r.Output("sudo", "-n", "btrfs", "filesystem", "usage", "-g", root)
	if err != nil {
		return "disk guard: btrfs metadata not checked (sudo -n btrfs filesystem usage failed)", nil
	}
	total, used := gib(df, `Metadata[^:]*: total=([0-9.]+)GiB`), gib(df, `Metadata[^:]*:.*used=([0-9.]+)GiB`)
	unalloc := gib(usage, `Device unallocated:\s+([0-9.]+)GiB`)
	if total == 0 || !strings.Contains(usage, "Device unallocated:") {
		return "disk guard: btrfs metadata not checked (unknown btrfs output)", nil
	}
	if used >= total-0.5 && unalloc < 2 {
		return "", fmt.Errorf("disk guard: btrfs metadata %.2f of %.2f GiB with %.2f GiB unallocated",
			used, total, unalloc)
	}
	return "", nil
}

// gib is the first submatch of pattern in s as a float, 0 without one.
func gib(s, pattern string) float64 {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}
