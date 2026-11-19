package agentspool

// DNS is one host-to-address mapping a DNS hook of the agent recorded
// (task wd4, decision #8): InetAddress.getAllByName resolved Host to Addrs
// at ExitMono (CLOCK_MONOTONIC), on thread ThreadID (kernel tid KTID),
// from stack StackID. The agent writes a mapping once per JVM, and again
// only when the host's address set changes; IP literals and failed
// lookups are not recorded. Covered by the watermarks like an HTTP record.
type DNS struct {
	ExitMono uint64
	StackID  uint64
	ThreadID uint64
	KTID     int32
	Host     string
	// Addrs are the resolved addresses in the resolver's order, textual
	// ("127.0.0.1", "0:0:0:0:0:0:0:1"), at least one.
	Addrs []string
	// Otel is the OTEL span current on the thread at the lookup.
	Otel Otel
}

// decodeDNS checks a DNS record: a non-negative tid, a host and at least
// one address, and OTEL ids either all set or none.
func decodeDNS(rec []byte) (Record, error) {
	if len(rec) < dnsHead {
		return nil, errMalformed
	}
	n := le.Uint32(rec[36:])
	d := DNS{ExitMono: le.Uint64(rec[8:]), StackID: le.Uint64(rec[16:]),
		ThreadID: le.Uint64(rec[24:]), KTID: int32(le.Uint32(rec[32:]))}
	var otelOK bool
	d.Otel, otelOK = decodeOtel(rec, 40)
	if d.KTID < 0 || n < 2 || !otelOK {
		return nil, errMalformed
	}
	s, err := decodeStrings(rec, dnsHead, n)
	if err != nil {
		return nil, err
	}
	if s[0] == "" {
		return nil, errMalformed
	}
	d.Host, d.Addrs = s[0], s[1:]
	return d, nil
}
