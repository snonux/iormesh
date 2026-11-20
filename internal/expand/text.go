package expand

import (
	"bufio"
	"fmt"
	"io"
	"net/netip"

	"iormesh/internal/termsafe"
)

// WriteText prints a proposal as `iormeshctl expand` shows it: the
// candidates, then every skipped peer with its reason and evidence, each
// with the traced apps that reached it. Host names, app labels and the
// evidence quoting them come from the traces, so they pass through
// termsafe: a compromised server's comm must not drive the terminal.
func WriteText(w io.Writer, p Proposal) error {
	b := bufio.NewWriter(w)
	_, _ = fmt.Fprintf(b, "expand: %d to deploy, %d skipped (hop limit %d, %d of %d nodes registered)\n",
		len(p.Deploy), len(p.Skipped), p.Limits.MaxHops, p.Nodes, p.Limits.MaxNodes)
	if len(p.Deploy) > 0 {
		_, _ = fmt.Fprintln(b, "\ndeploy")
	}
	for _, c := range p.Deploy {
		_, _ = fmt.Fprintf(b, "  %-21s hop %d  %s  %s\n", addrPort(c.Peer), c.Hop, peerConns(c.Peer), c.Rule)
		writeVia(b, c.Via)
	}
	if len(p.Skipped) > 0 {
		_, _ = fmt.Fprintln(b, "\nskipped")
	}
	for _, s := range p.Skipped {
		_, _ = fmt.Fprintf(b, "  %-21s %-16s %s  %s\n", addrPort(s.Peer), s.Reason, peerConns(s.Peer),
			termsafe.String(s.Evidence))
		writeVia(b, s.Via)
	}
	return b.Flush()
}

// writeVia lists the apps that reached a peer, one per line.
func writeVia(b *bufio.Writer, via []Via) {
	for _, v := range via {
		app := v.App
		if app == "" {
			app = "-"
		}
		_, _ = fmt.Fprintf(b, "      via %s %s (%s)\n", termsafe.String(v.Host), termsafe.String(app),
			conns(v.Conns))
	}
}

// addrPort prints a peer as addr:port ([v6]:port); a half that recorded
// no peer prints as "-".
func addrPort(p Peer) string {
	if !p.Addr.IsValid() {
		return "-"
	}
	return netip.AddrPortFrom(p.Addr, p.Port).String()
}

// peerConns is a peer's connection count, with those folded in from
// per-connection skips (Peer.Other) named apart.
func peerConns(p Peer) string {
	if p.Other == 0 {
		return conns(p.Conns)
	}
	return fmt.Sprintf("%s (+%d set aside)", conns(p.Conns), p.Other)
}

func conns(n int) string {
	if n == 1 {
		return "1 conn"
	}
	return fmt.Sprintf("%d conns", n)
}
