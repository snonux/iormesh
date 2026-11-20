package federate

import "testing"

// TestKeyFrom checks the role-aware rebuild against S2's experiment 1:
// both ends of one connection must rebuild the same pair at any record
// from ESTABLISHED on, with data, FIN, TFO payload, 32-bit wrap and
// counters past 4 GiB.
func TestKeyFrom(t *testing.T) {
	ciss, siss := uint32(0xfffffff0), uint32(0x12345678) // vars: the sums wrap
	want := ISNPair{Client: ciss, Server: siss}
	cases := []struct {
		name string
		role string
		f    seqFields
	}{
		// SYN_SENT → ESTABLISHED: the SYN-ACK acked the client's SYN.
		{"client established", RoleClient,
			seqFields{SndUna: ciss + 1, RcvNxt: siss + 1, BytesAcked: 1}},
		// TFO: 100 bytes rode in the SYN and were acked with it.
		{"client tfo", RoleClient,
			seqFields{SndUna: ciss + 101, RcvNxt: siss + 1, BytesAcked: 101}},
		// After a request/response and both FINs.
		{"client close", RoleClient, seqFields{SndUna: ciss + 1 + 20 + 1,
			RcvNxt: siss + 1 + 300 + 1, BytesAcked: 22, BytesReceived: 301}},
		// SYN_RECV → ESTABLISHED: the child starts at snd_una = iss + 1.
		{"server established", RoleServer,
			seqFields{SndUna: siss + 1, RcvNxt: ciss + 1}},
		// Accept snapshot with the request and the FIN already queued.
		{"server accept with data", RoleServer,
			seqFields{SndUna: siss + 1, RcvNxt: ciss + 1 + 20 + 1, BytesReceived: 21}},
		// Counters past 4 GiB reduce mod 2^32 like the sequence numbers.
		{"server past 4 GiB", RoleServer, seqFields{SndUna: siss + 1 + 7,
			RcvNxt: ciss + 1 + 5, BytesAcked: 1<<32 + 7, BytesReceived: 3<<32 + 5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keyFrom(c.role, c.f); got != want {
				t.Fatalf("keyFrom = %v, want %v", got, want)
			}
		})
	}
}

// TestKeyState: only records with the handshake complete may rebuild the
// key; CLOSE is the caller's decision.
func TestKeyState(t *testing.T) {
	for state, want := range map[string]bool{
		"ESTABLISHED": true, "FIN_WAIT1": true, "FIN_WAIT2": true, "TIME_WAIT": true,
		"CLOSE_WAIT": true, "LAST_ACK": true, "CLOSING": true,
		"SYN_SENT": false, "SYN_RECV": false, "NEW_SYN_RECV": false,
		"LISTEN": false, "CLOSE": false,
	} {
		if got := keyState(state); got != want {
			t.Errorf("keyState(%s) = %v, want %v", state, got, want)
		}
	}
}
