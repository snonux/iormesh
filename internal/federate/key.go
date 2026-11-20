package federate

import "fmt"

// Connection roles: which end of the TCP handshake a half is.
const (
	RoleClient = "client" // the connecting side (connect, SYN_SENT)
	RoleServer = "server" // the accepting side (accept, accept4)
)

// ISNPair is decision #26's join key, oriented the same on both hosts:
// the connecting side's initial sequence number first. NAT rewrites
// addresses and ports, never sequence numbers, so both ends of one
// connection rebuild the same pair however many NAT hops lie between.
type ISNPair struct{ Client, Server uint32 }

// String prints the pair as "client/server" in hex, the form summaries
// and conn-id hashes use.
func (k ISNPair) String() string { return fmt.Sprintf("%08x/%08x", k.Client, k.Server) }

// seqFields are the four tcp_sock fields one tcp_state record carries
// (schema Args snd_una, rcv_nxt, bytes_acked, bytes_received).
type seqFields struct {
	SndUna, RcvNxt            uint32
	BytesAcked, BytesReceived uint64
}

// keyFrom rebuilds the ISN pair from one record taken at ESTABLISHED or
// later (S2, experiment 1), all arithmetic mod 2^32. The connecting
// side's bytes_acked already counts its own SYN (the SYN-ACK runs
// tcp_snd_una_update); the accepting child is cloned with snd_una =
// iss + 1 and bytes_acked = 0, so its own SYN needs the extra 1.
// bytes_received never counts the peer's SYN, on either side. The u64
// counters are reduced mod 2^32 by the conversion, which keeps the
// rebuild right past 4 GiB.
func keyFrom(role string, f seqFields) ISNPair {
	own := f.SndUna - uint32(f.BytesAcked)
	if role == RoleServer {
		own--
	}
	peer := f.RcvNxt - uint32(f.BytesReceived) - 1
	if role == RoleServer {
		return ISNPair{Client: peer, Server: own}
	}
	return ISNPair{Client: own, Server: peer}
}

// keyState reports whether a record in this TCP state can rebuild the
// key: the handshake is complete in each. The opening transitions
// (SYN_SENT, SYN_RECV, NEW_SYN_RECV) and LISTEN never qualify — snd_una
// and rcv_nxt are still 0 there. CLOSE is decided by the caller: it ends
// an established connection but also a refused connect, whose fields
// hold no peer ISN.
func keyState(state string) bool {
	switch state {
	case "ESTABLISHED", "FIN_WAIT1", "FIN_WAIT2", "TIME_WAIT",
		"CLOSE_WAIT", "LAST_ACK", "CLOSING":
		return true
	}
	return false
}
