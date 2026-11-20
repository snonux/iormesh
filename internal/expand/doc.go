// Package expand decides how the mesh grows (PLAN.md M6 "Discover",
// "Rounds"): which unresolved peers of a merged build the controller may
// enroll next, and why every other peer stays where it is.
//
// Plan is a pure function of a merge (internal/federate Result), the
// registry and its expand: section. It never touches the network, never
// resolves a name and never edits the registry; `iormeshctl expand`
// prints its answer, and expand --apply (task tb4) acts on it.
//
// The rules, each a fact read off the traces rather than a judgment:
//
//   - Only a client half the merge left no_match reveals a candidate:
//     it connected out, carried a join key, and no trace holds the other
//     end. Its peer address and port are where a server listens.
//     ambiguous, no_key, no_role and key_conflict halves are listed as
//     unjoined — their far end may well be traced, and the merge could
//     not tell — and an unresolved server half is inbound: its peer is a
//     client's ephemeral port, nothing to bind a tracer to.
//   - A peer is the address the client reached, never a name: a DNS name
//     the client looked up is a guess about which machine answered, so
//     names are neither proposed nor matched by the allowlist.
//   - A peer already in the mesh is enrolled, not a candidate: a
//     registry host written as that address, or an address a traced host
//     owns (the local end of one of its own connections). This visited
//     set is what makes a cycle A→B→A terminate.
//   - A peer that a fused connection proves to be NAT in front of a
//     traced server (the client's peer differs from the server's local
//     address) is nat: what answers there is a translation, and which
//     machine is behind it for any other connection would be a guess.
//   - Loopback, link-local, unspecified and multicast peers are local,
//     IPv6 peers wait for an ssh_config alias (the registry takes no IPv6
//     literal), and a half from a host no longer registered reveals
//     nothing (via_unregistered): only the registry's hosts grow the mesh.
//   - What remains is matched against the allowlist (not_allowed), the
//     hop limit (hop_limit: a peer is one hop further than the nearest
//     registered host that reached it) and the node cap (node_cap: the
//     registry's hosts plus the new ones), in that order.
//
// Output is sorted, so the same build always plans the same bytes.
//
// It is cgo-free: iormeshctl runs it on the operator's laptop.
package expand
