package stacks

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
	"iormesh/internal/target"
)

// image names one process image: a process incarnation between two execs.
// tag is the image's mm->arg_start as the stack record carries it and
// /proc/<pid>/stat shows it (field 48): an exec draws a new one, so the
// tag, not the order in which the tracer hears of execs, says which
// image a record's addresses belong to.
type image struct {
	pid uint32
	inc uint64
	tag uint64
}

// snapshot is what the cache knows of one image: its executable
// mappings, each with the module key of the file the tracer opened for it,
// and the addresses no mapping covered even after a re-read (a bad frame,
// or code since unmapped), so they do not make every later stack re-read
// the maps.
type snapshot struct {
	maps   []mapping
	missed map[uint64]bool
}

// mapsCache keeps each image's mappings, read from /proc/<pid>/maps on
// the correlator's goroutine at the image's first stack (docs/schema.md,
// "Native stacks"). That read races the process: once it exited or
// exec'd, its maps are gone or another image's, so it is done as early as
// userspace can, and it is believed only when /proc/<pid>/stat shows the
// record's incarnation and image tag both before and after it. The slow
// part — parsing ELF files — is left to the resolver, off the hot path. A
// snapshot is kept until the process execs or exits (forget); an address
// it does not cover re-reads the maps once while the image lives.
//
// The mapped files are opened here too, through /proc/<pid>/map_files —
// the file object the mapping holds, with no path lookup the traced
// process could steer (a swapped component, a FIFO, a symlink that
// /proc/<pid>/root would resolve against the host's root) — non-blocking,
// and only regular files are kept. They are handed to the resolver open,
// once per module key for the session.
type mapsCache struct {
	procRoot string
	images   map[image]*snapshot
	opened   map[string]bool // module keys already handed to the resolver
}

// resolver turns addresses into frames against a snapshot and the ELF
// files it maps. It runs on the table's worker goroutine and is best
// effort: whatever it cannot read leaves the frame with less detail. ELF
// files are parsed once per module key, from the file mapsCache opened.
type resolver struct {
	modules map[string]*module // nil = unreadable
}

func newMapsCache(procRoot string) *mapsCache {
	return &mapsCache{procRoot: procRoot, images: map[image]*snapshot{}, opened: map[string]bool{}}
}

func newResolver() *resolver {
	return &resolver{modules: map[string]*module{}}
}

// snapshot returns img's mappings, (re-)reading them while img is the
// process's current image when they are missing or miss one of ips not
// already known to be uncovered, and the newly opened module files (the
// caller passes them to the resolver, which closes them). Without a
// trustworthy read the mappings are nil and the frames stay bare
// addresses.
func (c *mapsCache) snapshot(img image, ips []uint64) ([]mapping, map[string]*os.File) {
	snap := c.images[img]
	if snap != nil && !snap.lacks(ips) {
		return snap.maps, nil
	}
	maps, files, ok := c.read(img)
	if !ok {
		if snap == nil {
			return nil, nil
		}
		snap.remember(ips)
		return snap.maps, nil
	}
	snap = &snapshot{maps: maps, missed: map[uint64]bool{}}
	snap.remember(ips)
	c.images[img] = snap
	return maps, files
}

// read reads img's maps and opens their files, if img is the process's
// current image before and after; ok is false otherwise (exited, exec'd,
// pid reused, hidden), with nothing opened. The files' module keys count
// as handed to the resolver only on an accepted read: a refused one closes
// them unused, so a later image mapping the same file opens it again
// rather than leaving it unsymbolized for the session.
func (c *mapsCache) read(img image) ([]mapping, map[string]*os.File, bool) {
	if img.tag == 0 || !c.current(img) {
		return nil, nil, false
	}
	data, err := os.ReadFile(c.procPath(img.pid, "maps"))
	if err != nil {
		return nil, nil, false
	}
	maps := parseMaps(data)
	files := c.openFiles(img.pid, maps)
	if !c.current(img) {
		closeAll(files)
		return nil, nil, false
	}
	for key := range files {
		c.opened[key] = true
	}
	return maps, files, true
}

// current reports whether pid still runs img: the same incarnation (start
// time) and the same image tag.
func (c *mapsCache) current(img image) bool {
	line, err := os.ReadFile(c.procPath(img.pid, "stat"))
	if err != nil {
		return false
	}
	st, err := target.ParseStat(line)
	return err == nil && st.StartNS == img.inc && st.ArgStart == img.tag
}

// forget drops pid's snapshots: it exec'd or exited.
func (c *mapsCache) forget(pid uint32) {
	for img := range c.images {
		if img.pid == pid {
			delete(c.images, img)
		}
	}
}

// openFiles sets each file mapping's module key and returns the files not
// handed to the resolver before, one per module key: mappings of one file
// share the open. It does not mark them handed out — read does, once the
// read is accepted.
func (c *mapsCache) openFiles(pid uint32, maps []mapping) map[string]*os.File {
	var files map[string]*os.File
	byMapping := map[string]string{} // maps-line identity → module key
	for i := range maps {
		m := &maps[i]
		if !m.isFile() {
			continue
		}
		if key, ok := byMapping[m.ident()]; ok {
			m.mod = key
			continue
		}
		f, key, err := openMapped(c.procPath(pid, "map_files"), *m)
		if err != nil {
			continue
		}
		byMapping[m.ident()], m.mod = key, key
		if c.opened[key] || files[key] != nil { // handed out, or opened by another maps line
			_ = f.Close()
			continue
		}
		if files == nil {
			files = map[string]*os.File{}
		}
		files[key] = f
	}
	return files
}

func (c *mapsCache) procPath(pid uint32, name string) string {
	return filepath.Join(c.procRoot, strconv.FormatUint(uint64(pid), 10), name)
}

// lacks reports whether ips holds an address neither the mappings nor the
// known misses cover.
func (s *snapshot) lacks(ips []uint64) bool {
	for _, ip := range ips {
		if _, ok := find(s.maps, ip); !ok && !s.missed[ip] {
			return true
		}
	}
	return false
}

// remember records the addresses of ips the mappings do not cover.
func (s *snapshot) remember(ips []uint64) {
	for _, ip := range ips {
		if _, ok := find(s.maps, ip); !ok {
			s.missed[ip] = true
		}
	}
}

// openMapped opens the file behind mapping m through dir
// (/proc/<pid>/map_files), non-blocking, and returns it with its module
// key, or an error for anything but a regular file. The key is device,
// inode, size and modification time of the opened file: an inode number
// reused by a later file, or a file rewritten in place, is another module.
func openMapped(dir string, m mapping) (*os.File, string, error) {
	path := filepath.Join(dir, fmt.Sprintf("%x-%x", m.start, m.end))
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, "", err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = f.Close()
		return nil, "", fmt.Errorf("%s: not a regular file", path)
	}
	key := fmt.Sprintf("%d:%d:%d:%d.%d", st.Dev, st.Ino, st.Size, st.Mtim.Sec, st.Mtim.Nsec)
	return f, key, nil
}

// frames symbolizes ips, innermost first, against the snapshot maps,
// parsing files first (taking ownership: each is closed after parsing).
func (r *resolver) frames(maps []mapping, files map[string]*os.File, ips []uint64) []schema.StackFrame {
	for key, f := range files {
		r.modules[key] = safeReadModule(f)
		_ = f.Close()
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
	mod := r.modules[m.mod]
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

func closeAll(files map[string]*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}
