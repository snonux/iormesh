package report

// List caps: each list of a process is cut after this many entries and
// says how many more there were (the More* fields).
const (
	MaxProcesses = 256
	MaxFiles     = 32
	MaxConns     = 32
	MaxThreads   = 32
	MaxCalls     = 32 // requests per thread, and per connection
	MaxMethods   = 10
	// MaxFileOps and MaxFileMethods cap one file's ops and methods.
	MaxFileOps     = 8
	MaxFileMethods = 4
)

// Peer states of a connection end.
const (
	// PeerTraced: the other end is a process of this rollup (a loopback
	// pair its host fused, or a cross-host pair the merge fused).
	PeerTraced = "traced"
	// PeerUntraced: no process of the rollup holds the other end.
	PeerUntraced = "untraced"
)

// Roles of a connection end, from the connect or accept the trace saw.
const (
	RoleClient = "client"
	RoleServer = "server"
)

// Directions of a thread's request.
const (
	DirOut = "out" // the thread sent the request (its process is the client)
	DirIn  = "in"  // the thread served it
)

// Report is one consolidation run's rollup.
type Report struct {
	Processes     []Process `json:"processes"`
	MoreProcesses int       `json:"more_processes"`
	// Untracked counts events (each once) the collector could not fully
	// attribute to a process, file, connection, thread or method because
	// its tracking cap was reached: the rollup is partial when it is not
	// zero.
	Untracked uint64 `json:"untracked"`
}

// Process is one traced process incarnation.
type Process struct {
	// ID is the merged graph's process node id
	// ("process:<host_id>/<pid>@<incarnation>").
	ID      string `json:"id"`
	Project string `json:"project"`
	// Host is the store's host directory, FQDN the session's.
	Host        string `json:"host"`
	FQDN        string `json:"fqdn"`
	PID         uint32 `json:"pid"`
	Incarnation uint64 `json:"pid_incarnation"`
	Comm        string `json:"comm"`
	// App is the target pattern its session took it on by ("" for -pid
	// and port sessions); Argv its last exec's command line, redacted
	// by the tracer (none when it was running before the trace).
	App  string   `json:"app"`
	Argv []string `json:"argv"`
	// Serves lists the endpoint labels it accepted connections on (a
	// service name, hostname:port or ip:port), sorted.
	Serves []string `json:"serves"`
	Events uint64   `json:"events"`
	// JavaEvents counts its events with a Java stack (attribution java).
	JavaEvents  uint64       `json:"java_events"`
	Files       []File       `json:"files"`
	MoreFiles   int          `json:"more_files"`
	Conns       []Conn       `json:"conns"`
	MoreConns   int          `json:"more_conns"`
	Threads     []Thread     `json:"threads"`
	MoreThreads int          `json:"more_threads"`
	Methods     []MethodStat `json:"methods"`
	MoreMethods int          `json:"more_methods"`
}

// File is one path a process opened or changed, in the order it first
// did.
type File struct {
	Path string `json:"path"`
	// ReadOpens and WriteOpens count successful opens for reading only
	// and for writing (O_WRONLY or O_RDWR).
	ReadOpens  int64  `json:"read_opens"`
	WriteOpens int64  `json:"write_opens"`
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
	// Ops are the structural changes in first-seen order, each once:
	// "create", "mkdir", "unlink", "rename -> NEW", "renamed from OLD",
	// … with " (ERRNO)" when the call failed.
	Ops     []string `json:"ops"`
	MoreOps int      `json:"more_ops"`
	// Methods are the first MaxFileMethods causing Java methods
	// ("class#method") of its events, first seen first; later ones are
	// not listed (and not counted: that would need tracking them all).
	Methods []string `json:"methods"`
}

// Conn is one connection end of a process.
type Conn struct {
	// ID is the merged trace's conn id: what `iormeshctl query -conn`
	// takes, shared by both ends of a fused connection.
	ID string `json:"id"`
	// FD is the descriptor this end was held by (-1 when no event with
	// an fd named it): one process may hold both ends of a connection or
	// a pipe, and each end is its own entry.
	FD   int32  `json:"fd"`
	Role string `json:"role"` // RoleClient, RoleServer or "" (unseen)
	// Local and Remote are the addresses this end saw ("" unknown).
	Local  string `json:"local"`
	Remote string `json:"remote"`
	// Service labels the server endpoint (service name, hostname:port
	// or ip:port) from the service database.
	Service string `json:"service"`
	// PeerState says whether the other end was traced; Peers name its
	// processes ("project/host/comm[pid]") when it was.
	PeerState    string    `json:"peer_state"`
	Peers        []string  `json:"peers"`
	SentBytes    uint64    `json:"sent_bytes"`
	RecvBytes    uint64    `json:"recv_bytes"`
	Requests     []Request `json:"requests"`
	MoreRequests int       `json:"more_requests"`
}

// Request is one HTTP request as an http event recorded it.
type Request struct {
	Method string `json:"method"`
	// Scheme is "https" over TLS, "http" plain, as the library saw it.
	Scheme string `json:"scheme"`
	Path   string `json:"path"`
	Status uint16 `json:"status"`
	// Thread is the recording thread's name (see Thread.Name).
	Thread string `json:"thread"`
}

// Thread is one thread of a process, in the order it first appeared.
type Thread struct {
	// Name is the Java thread name, else "tid N".
	Name   string `json:"name"`
	TID    uint32 `json:"tid"`
	Events uint64 `json:"events"`
	Bytes  uint64 `json:"bytes"`
	// Carried counts the events among Events that virtual threads issued
	// while this kernel thread carried them (java.carrier): their I/O,
	// named by the carrier, never the carrier's own work (task 0f4).
	Carried uint64 `json:"carried,omitempty"`
	// Calls are its HTTP requests in the order it made or served them:
	// the thread's part of a conversation.
	Calls     []Call `json:"calls"`
	MoreCalls int    `json:"more_calls"`
}

// Call is one request on a thread's line.
type Call struct {
	Dir    string `json:"dir"` // DirOut, DirIn, or "" when the role is unknown
	Method string `json:"method"`
	Scheme string `json:"scheme"`
	Path   string `json:"path"`
	Status uint16 `json:"status"`
	Conn   string `json:"conn"`
	// Service labels the server endpoint; Peers the other end's
	// processes, as on Conn.
	Service string   `json:"service"`
	Peers   []string `json:"peers"`
	// Carrier flags a request a virtual thread made or served on this
	// carrier thread: not the carrier's own call.
	Carrier bool `json:"carrier,omitempty"`
}

// MethodStat counts the events a Java method caused.
type MethodStat struct {
	Method string `json:"method"`
	Events uint64 `json:"events"`
	Bytes  uint64 `json:"bytes"`
}
