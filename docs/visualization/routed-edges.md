# Routed edges and example output

Part of [Visualization — exploring a consolidated mesh](../visualization.md).

## Routed edges (M7)

The route map (milestones.md M7 "Ingress route mapping", decision #15)
adds `routes` and `health.route_flags` to the model, and `build`
prints them as `routed` and `route flag` lines. As built by task
bf4, the three surfaces draw them too. A routed edge is one
picture — client → FQDN → ingress → Service → workload (→ pod),
the M0 ingress scenario's "one routed edge" — never a set of
graph nodes: no tracer ran on the FQDN, the ingress or the
workload, so the merged graph has no node for them, and the
hops live only in the routed edge.

- **graph.svg**: a band below the host columns, one row per
  routed edge — a box per hop (class `app`, `fqdn`, `ingress`,
  `service`, `workload`, `pod`), arrows between them, then its
  basis and connections ("2 endpoints, pod unknown" for an
  untraced Service of several). An edge the map completed
  (basis `service` or `route`) is drawn dashed (`routed`); only a
  `fused` one, both ends traced, is solid. The route flags follow,
  one red line each. Each list keeps 200 rows and counts the rest;
  a hop's label is cut at 40 runes, a flag line at 120. Every new
  class is on the renderer's fixed list.
- **Site**: the same band below the mesh, in the same canvas.
  Clicking a row opens `#/mesh/route/N` — its hops, basis,
  counts, the map it came from (`static` or `discovered`), when it
  was seen, a warning that the hops past the client were not
  traced, and the flags about it (a flag whose subject is one of
  its hops). A warning line above the mesh counts the flags and
  links `#/mesh/flag/N`; the mesh's edge list adds the routed
  edges and the flags; Health adds a route-flag table. The text
  filter dims rows whose hops do not match. The basis becomes a
  class only from a fixed list; every name goes through
  `textContent`.
- **TUI**: the edges screen lists the routed edges after the
  traced ones (client, `=>`, last hop, `routed BASIS`) and each
  flag as a row (`flag`, subject, kind); the detail spells out the chain,
  what the basis means, and the flags about it. A service's
  detail lists the routed edges leaving it; the title counts them;
  the health screen's summary has the `routed` and `route flag`
  lines. `⏎` on a routed edge or flag says why nothing opens.

A model without routes or flags renders exactly as before on all
three: no band, no extra style, no extra rows or title text — the
existing goldens hold. Tests: `internal/render`
(`testdata/graph_routes.golden`, hostile and capped names, no
routes unchanged), `internal/tui` (`testdata/routes_edges.golden`,
hostile names at every size, empty lists unchanged),
`internal/site` (the routes through the data block and graph.svg,
the browser selftest rendering every routed edge and flag, and
checking that `#/mesh/route/999`, `#/mesh/flag/-1` and
`#/mesh/route/__proto__` land on their "no such …" guard) and
`internal/ctl` (`testdata/routes_graph_svg.golden` from the
ClusterIP build).


## Example output (a real trace)

One real run, the test workload's microservices scenario built
and shown with the `iormeshctl` of the same commit (task wk4), is
walked through on all three surfaces in the guide:
[7. Read the mesh](../guide/07-read-the-mesh.md) has the site's
screenshots, every `show` view, the TUI captures, and (with
[8. Java code flow and query](../guide/08-java-query.md)) a step's
raw event. The run's files are in [examples/](../examples/): the
whole site folder ([site/index.html](../examples/site/index.html),
openable from `file://`), its static
[graph.svg](../examples/site/graph.svg) and
[sequence/0002.svg](../examples/site/sequence/0002.svg), the
full [show sequence 0002](../examples/show-sequence-0002.txt) and
the run's http events as
[query -event http](../examples/query-http.ndjson).
