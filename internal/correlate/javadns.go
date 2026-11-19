package correlate

import (
	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// The dns events of the Java join (task wd4, decision #8). The agent's
// DNS hook writes one DNS record when a JVM resolves a host name to a set
// of addresses it had not mapped that name to before; the joiner turns it
// into a dns event in the same queue as the http events (javahttp.go),
// stamped when the lookup returned, so it lands after the I/O that
// completed before it — the resolver's own reads among them.

// addDNS queues a DNS record by its time.
func (j *JavaJoin) addDNS(v *jvm, r agentspool.DNS) {
	j.queue(pendingHTTP{boot: j.boot(r.ExitMono), v: v, dns: &r})
}

// dnsEvent builds the dns event of one record: envelope from the JVM and
// the stream, the mapping as the agent recorded it, and the Java side
// from the record's stack with the OTEL span of the lookup.
func (j *JavaJoin) dnsEvent(h pendingHTTP) (schema.Event, error) {
	v, r := h.v, h.dns
	ev := schema.Event{SchemaVersion: schema.Version, Project: j.project, HostID: j.hostID,
		BootNS: h.boot, WallNS: uint64(int64(h.boot) + j.wallOff), PID: v.pid,
		PIDInc: v.incarnation(), TID: uint32(r.KTID), Comm: v.comm, Event: schema.KindDNS,
		FD: schema.NoFD, Attribution: schema.AttributionNone,
		DNS: &schema.DNS{Host: r.Host, Addrs: r.Addrs}}
	j.stats.DNS++
	stack, err := j.stack(v, r.StackID)
	if err != nil || stack == nil {
		return ev, err
	}
	java := &schema.Java{Stack: stack}
	if th, ok := v.threads[r.ThreadID]; ok && !th.Virtual {
		java.Thread = v.nameAt(th, r.ExitMono)
	}
	ev.Attribution, ev.Java = schema.AttributionJava, java
	ev.Otel = otelOf(r.Otel)
	return ev, nil
}
