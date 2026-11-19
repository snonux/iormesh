package stacks

import (
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
)

// image names one process image: a process incarnation between two execs.
// An exec replaces the address space, so its mappings are another image's.
type image struct {
	pid uint32
	inc uint64
	gen uint32 // execs seen since the trace saw the incarnation
}

// mapsCache keeps each image's executable mappings, read from
// /proc/<pid>/maps on the correlator's goroutine at the image's first
// stack (docs/schema.md, "Native stacks"). That read is the one step that
// races the process: once it exited its maps are gone, so it is done as
// early as userspace can, and the slow part — reading ELF files — is left
// to the resolver, off the hot path. A snapshot is kept for the session:
// a process that exited later still has its later stacks resolved against
// it. An address none of its mappings covers (a library loaded since)
// re-reads the maps once per stack while the process lives. Snapshots are
// never modified, only replaced, so the resolver may read one while the
// cache moves on.
//
// The files the mappings name are opened here too, once per (device,
// inode) for the session, and handed to the resolver open: after the
// process exited its /proc/<pid>/root is gone, and the host path cannot
// stand in safely — another mount namespace may have another file there,
// and on btrfs a file's st_dev is not the device its maps line shows, so
// "same file" cannot be checked.
type mapsCache struct {
	procRoot string
	alive    func(pid uint32, inc uint64) bool
	maps     map[image][]mapping
	opened   map[string]bool // module keys already handed to the resolver
}

// resolver turns addresses into frames against a mappings snapshot and
// the ELF files they map. It runs on the table's worker goroutine and is
// best effort: whatever it cannot read leaves the frame with less detail,
// never a guessed one. ELF files are parsed once per (device, inode), from
// the file mapsCache opened.
type resolver struct {
	modules map[string]*module // nil = unreadable
}

func newMapsCache(procRoot string, alive func(pid uint32, inc uint64) bool) *mapsCache {
	return &mapsCache{procRoot: procRoot, alive: alive, maps: map[image][]mapping{},
		opened: map[string]bool{}}
}

func newResolver() *resolver {
	return &resolver{modules: map[string]*module{}}
}

// snapshot returns img's mappings, (re-)reading them while the process
// lives when they are missing or miss one of ips, and the newly opened
// files of mappings no earlier snapshot opened (the caller passes them to
// the resolver, which closes them).
func (c *mapsCache) snapshot(img image, ips []uint64) ([]mapping, map[string]*os.File) {
	maps, cached := c.maps[img]
	if cached && coversAll(maps, ips) {
		return maps, nil
	}
	if !c.alive(img.pid, img.inc) {
		return maps, nil
	}
	data, err := os.ReadFile(c.procPath(img.pid, "maps"))
	// Checked after the read, too: a pid reused in between has another
	// process's maps, which must not be kept for this one.
	if err != nil || !c.alive(img.pid, img.inc) {
		return maps, nil
	}
	maps = parseMaps(data)
	c.maps[img] = maps
	return maps, c.openFiles(img.pid, maps)
}

// openFiles opens the not yet opened files of maps through the process's
// root, so a process in another mount namespace has its own file read.
// A file whose inode is not the mapped one (replaced since) is skipped.
func (c *mapsCache) openFiles(pid uint32, maps []mapping) map[string]*os.File {
	var files map[string]*os.File
	for _, m := range maps {
		key := m.key()
		if !m.isFile() || c.opened[key] {
			continue
		}
		f, err := os.Open(c.procPath(pid, "root") + m.path)
		if err != nil {
			continue
		}
		if !sameInode(f, m.inode) {
			_ = f.Close()
			continue
		}
		if files == nil {
			files = map[string]*os.File{}
		}
		files[key], c.opened[key] = f, true
	}
	return files
}

func (c *mapsCache) procPath(pid uint32, name string) string {
	return filepath.Join(c.procRoot, strconv.FormatUint(uint64(pid), 10), name)
}

// forget drops pid's snapshots: it exec'd, so later stacks are a new
// image's.
func (c *mapsCache) forget(pid uint32) {
	for img := range c.maps {
		if img.pid == pid {
			delete(c.maps, img)
		}
	}
}

func coversAll(maps []mapping, ips []uint64) bool {
	for _, ip := range ips {
		if _, ok := find(maps, ip); !ok {
			return false
		}
	}
	return true
}

// frames symbolizes ips, innermost first, against the snapshot maps,
// parsing files first (taking ownership: each is closed after parsing).
func (r *resolver) frames(maps []mapping, files map[string]*os.File, ips []uint64) []schema.StackFrame {
	for key, f := range files {
		mod, _ := readModule(f)
		_ = f.Close()
		r.modules[key] = mod
	}
	frames := make([]schema.StackFrame, len(ips))
	for i, ip := range ips {
		frames[i] = schema.StackFrame{Addr: "0x" + strconv.FormatUint(ip, 16)}
		if m, ok := find(maps, ip); ok {
			r.fill(&frames[i], m, ip, i == 0)
		}
	}
	return frames
}

// fill adds module, offset, build id and symbol to a frame in mapping m.
// Every frame but the innermost holds a return address, the instruction
// after the call, which may already lie in the next function; its symbol
// is looked up one byte earlier, as unwinders do. Offset stays the
// address's own.
func (r *resolver) fill(f *schema.StackFrame, m mapping, ip uint64, innermost bool) {
	f.Module = m.path
	if !m.isFile() {
		return
	}
	off := ip - m.start + m.pgoff
	f.Offset = &off
	mod := r.modules[m.key()]
	if mod == nil {
		return
	}
	f.BuildID = mod.buildID
	lookup := off
	if !innermost && lookup > 0 {
		lookup--
	}
	f.Symbol = mod.symbolize(lookup)
}

// sameInode reports whether f is the file of inode ino. The device is not
// compared: on btrfs st_dev names the subvolume, the maps line the
// filesystem, so the two differ for the very same file.
func sameInode(f *os.File, ino uint64) bool {
	var st unix.Stat_t
	return unix.Fstat(int(f.Fd()), &st) == nil && st.Ino == ino
}
