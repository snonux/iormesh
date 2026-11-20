package k8s

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Runtimes as a container's cgroup names them. A cgroupfs-driver path
// carries a bare container id for containerd and Docker alike, so its
// runtime is RuntimeUnknown.
const (
	RuntimeContainerd = "containerd"
	RuntimeCRIO       = "cri-o"
	RuntimeDocker     = "docker"
	RuntimeUnknown    = ""
)

// QoS classes, as the kubelet's cgroup hierarchy encodes them: Guaranteed
// pods sit directly under kubepods, the other two under a class cgroup.
const (
	QoSGuaranteed = "guaranteed"
	QoSBurstable  = "burstable"
	QoSBestEffort = "besteffort"
)

// CgroupIdentity is the pod and container a cgroup path names.
type CgroupIdentity struct {
	// PodUID is the pod's metadata.uid, dashed and lowercase (the systemd
	// driver's underscores turned back into dashes), or a static pod's
	// 32-hex-digit UID, which has no dashes.
	PodUID string
	// ContainerID is the 64-hex-digit runtime container id (what
	// `kubectl get pod -o json` shows after "containerd://" or
	// "cri-o://"); empty for a process in the pod cgroup itself.
	ContainerID string
	// Runtime is one of the Runtime constants.
	Runtime string
	// QoS is one of the QoS constants.
	QoS string
}

var (
	// ErrNotInPod reports a cgroup that names no pod: a host process, or
	// any process seen from inside a private cgroup namespace (cgroup v2's
	// default for containers), whose path is "/". An in-pod tracer then
	// takes its pod from the downward API (PodFromEnv).
	ErrNotInPod = errors.New("cgroup names no pod")
	// errMalformed reports a /proc/<pid>/cgroup that is not the kernel's
	// format, or a pod cgroup whose ids are not what the kubelet writes.
	errMalformed = errors.New("malformed cgroup")
)

var (
	// dashedUID is a pod UID as the API server issues it; staticUID is a
	// static pod's, which the kubelet derives from a hash of the manifest.
	dashedUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	staticUID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// containerID is the runtime's id: CRI-O, containerd and Docker all
	// use 64 hex digits.
	containerID = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// hierarchyID is the first field of a /proc/<pid>/cgroup line.
	hierarchyID = regexp.MustCompile(`^[0-9]+$`)
)

// ParseProcCgroup reads the contents of /proc/<pid>/cgroup (cgroup v1,
// v2 or hybrid) and returns the pod and container the process runs in.
// The v2 line ("0::<path>") wins; on a v1-only host every controller
// line that names a pod must name the same one. It returns ErrNotInPod
// (wrapped) when no line names a pod, and another error when the
// contents are not the kernel's format.
func ParseProcCgroup(data []byte) (CgroupIdentity, error) {
	var v2 string
	var v1 []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		f := strings.SplitN(line, ":", 3)
		if len(f) != 3 || !hierarchyID.MatchString(f[0]) || !strings.HasPrefix(f[2], "/") {
			return CgroupIdentity{}, fmt.Errorf("%w: line %q", errMalformed, line)
		}
		if f[0] == "0" && f[1] == "" {
			v2 = f[2]
			continue
		}
		v1 = append(v1, f[2])
	}
	if err := sc.Err(); err != nil {
		return CgroupIdentity{}, fmt.Errorf("%w: %v", errMalformed, err)
	}
	if v2 == "" && len(v1) == 0 {
		// The kernel always writes at least one line.
		return CgroupIdentity{}, fmt.Errorf("%w: no cgroup lines", errMalformed)
	}
	if v2 != "" {
		if id, err := ParseCgroupPath(v2); !errors.Is(err, ErrNotInPod) || len(v1) == 0 {
			return id, err
		}
	}
	// Hybrid hosts keep the v2 hierarchy empty of pods ("0::/") and put
	// them in the v1 controllers: fall through to those.
	return parseV1Paths(v1)
}

// parseV1Paths applies ParseCgroupPath to every v1 controller's path and
// insists they agree: a process sits in one pod, whatever the controller.
func parseV1Paths(paths []string) (CgroupIdentity, error) {
	var found *CgroupIdentity
	for _, p := range paths {
		id, err := ParseCgroupPath(p)
		if errors.Is(err, ErrNotInPod) {
			continue
		}
		if err != nil {
			return CgroupIdentity{}, err
		}
		if found != nil && *found != id {
			return CgroupIdentity{}, fmt.Errorf("%w: controllers disagree: %+v and %+v", errMalformed, *found, id)
		}
		found = &id
	}
	if found == nil {
		return CgroupIdentity{}, ErrNotInPod
	}
	return *found, nil
}

// ParseCgroupPath returns the pod and container one cgroup path names,
// under either kubelet cgroup driver:
//
//	systemd:  …/kubepods[-<qos>]-pod<uid_with_underscores>.slice/<runtime>-<id>.scope
//	cgroupfs: …/kubepods[/<qos>]/pod<uid>/[crio-]<id>
//
// whatever prefixes the kubepods part (a kind node's docker scope, a
// kubelet --cgroup-root such as kind's /kubelet.slice). The container is
// the pod cgroup's child; deeper cgroups (CRI-O's ".../container", a
// container's own sub-cgroups) still belong to it.
func ParseCgroupPath(path string) (CgroupIdentity, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		id, ok, err := podComponent(parts[:i], part)
		if err != nil {
			return CgroupIdentity{}, fmt.Errorf("%w: %q: %v", errMalformed, path, err)
		}
		if !ok {
			continue
		}
		if i+1 < len(parts) {
			id.ContainerID, id.Runtime, err = containerComponent(parts[i+1], strings.HasSuffix(part, ".slice"))
			if err != nil {
				return CgroupIdentity{}, fmt.Errorf("%w: %q: %v", errMalformed, path, err)
			}
		}
		return id, nil
	}
	return CgroupIdentity{}, fmt.Errorf("%w: %q", ErrNotInPod, path)
}

// podComponent reports whether part, below ancestors, is a kubelet pod
// cgroup, and its identity if so. A part that looks like a pod cgroup in
// a kubepods hierarchy but carries a bad UID is an error, not "no pod":
// the kubelet never writes one, so the path was mangled or forged.
func podComponent(ancestors []string, part string) (CgroupIdentity, bool, error) {
	if name, ok := strings.CutSuffix(part, ".slice"); ok {
		return systemdPod(name)
	}
	uid, ok := strings.CutPrefix(part, "pod")
	if !ok {
		return CgroupIdentity{}, false, nil
	}
	qos, ok := cgroupfsQoS(ancestors)
	if !ok {
		return CgroupIdentity{}, false, nil
	}
	if !validUID(uid) {
		return CgroupIdentity{}, false, fmt.Errorf("pod UID %q", uid)
	}
	return CgroupIdentity{PodUID: uid, QoS: qos}, true, nil
}

// systemdPod parses a systemd-driver slice name without ".slice":
// "kubepods[-<qos>]-pod<uid>", possibly prefixed by the kubelet's cgroup
// root ("kubelet-kubepods-…" on kind). systemd escapes the UID's dashes,
// which separate slice levels, as underscores.
func systemdPod(name string) (CgroupIdentity, bool, error) {
	i := strings.LastIndex(name, "-pod")
	if i < 0 {
		return CgroupIdentity{}, false, nil
	}
	prefix := name[:i]
	var qos string
	switch {
	case strings.HasSuffix(prefix, "kubepods-"+QoSBurstable):
		qos = QoSBurstable
	case strings.HasSuffix(prefix, "kubepods-"+QoSBestEffort):
		qos = QoSBestEffort
	case strings.HasSuffix(prefix, "kubepods"):
		qos = QoSGuaranteed
	default:
		return CgroupIdentity{}, false, nil
	}
	raw := name[i+len("-pod"):]
	uid := strings.ReplaceAll(raw, "_", "-")
	if !validUID(uid) {
		return CgroupIdentity{}, false, fmt.Errorf("pod UID %q", raw)
	}
	return CgroupIdentity{PodUID: uid, QoS: qos}, true, nil
}

// cgroupfsQoS reports the QoS class a cgroupfs-driver pod directory sits
// in, judged by its ancestors: "kubepods" directly (Guaranteed) or
// "kubepods/<class>". Anything else is not the kubelet's hierarchy.
func cgroupfsQoS(ancestors []string) (string, bool) {
	n := len(ancestors)
	switch {
	case n >= 1 && ancestors[n-1] == "kubepods":
		return QoSGuaranteed, true
	case n >= 2 && ancestors[n-2] == "kubepods" &&
		(ancestors[n-1] == QoSBurstable || ancestors[n-1] == QoSBestEffort):
		return ancestors[n-1], true
	}
	return "", false
}

// validUID accepts an API-server pod UID or a static pod's.
func validUID(uid string) bool {
	return dashedUID.MatchString(uid) || staticUID.MatchString(uid)
}

// containerComponent parses the pod cgroup's child: under the systemd
// driver "<runtime>-<id>.scope", under cgroupfs "<id>" or CRI-O's
// "crio-<id>". CRI-O's conmon (the runtime's monitor, "crio-conmon-<id>")
// also lives in the pod cgroup but is no container of the pod, and is
// refused like any name no runtime writes.
func containerComponent(part string, systemd bool) (id, runtime string, err error) {
	name := part
	if systemd {
		var ok bool
		if name, ok = strings.CutSuffix(part, ".scope"); !ok {
			return "", "", fmt.Errorf("container cgroup %q: want a .scope", part)
		}
	}
	runtime = RuntimeUnknown
	switch {
	case strings.HasPrefix(name, "crio-conmon-"):
		return "", "", fmt.Errorf("container cgroup %q: CRI-O's conmon, not a container", part)
	case strings.HasPrefix(name, "cri-containerd-") && systemd:
		id, runtime = strings.TrimPrefix(name, "cri-containerd-"), RuntimeContainerd
	case strings.HasPrefix(name, "crio-"):
		id, runtime = strings.TrimPrefix(name, "crio-"), RuntimeCRIO
	case strings.HasPrefix(name, "docker-") && systemd:
		id, runtime = strings.TrimPrefix(name, "docker-"), RuntimeDocker
	case systemd:
		return "", "", fmt.Errorf("container cgroup %q: unknown runtime", part)
	default:
		id = name
	}
	if !containerID.MatchString(id) {
		return "", "", fmt.Errorf("container id %q: want 64 hex digits", id)
	}
	return id, runtime, nil
}
