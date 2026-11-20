package schema

// KindDNS is one host name a JVM resolved, as the Java agent's DNS hook
// recorded it (task wd4, decision #8: hostname↔IP via the agent's
// InetAddress hook): not a syscall but an event of its own, stamped when
// the lookup returned, on the thread and stack that asked. Event.DNS
// names the host and its addresses; fd is NoFD. A JVM records each
// mapping once, and again only when the host's address set changes, so
// the event says "from here on this name meant these addresses", not
// "a lookup happened" — IP literals and failed lookups are not recorded.
const KindDNS = "dns"

// DNS is the mapping on a dns event. Frozen with M3's acceptance: fields
// are only appended.
type DNS struct {
	// Host is the name as the app passed it to the resolver.
	Host string `json:"host" parquet:"host"`
	// Addrs are the addresses it resolved to, in the resolver's order
	// (the one the JDK connects to first leads), textual — IPv6 in the
	// JDK's full form ("0:0:0:0:0:0:0:1").
	Addrs []string `json:"addrs" parquet:"addrs,list"`
}

// validateDNS keeps the dns object and its kind together and complete: a
// mapping needs its host and at least one address.
func (e *Event) validateDNS() error {
	if (e.Event == KindDNS) != (e.DNS != nil) {
		return invalidf("dns object on a %s event (set exactly on dns events)", e.Event)
	}
	if e.DNS != nil && (e.DNS.Host == "" || len(e.DNS.Addrs) == 0) {
		return invalidf("dns event without its host or addresses")
	}
	return nil
}
