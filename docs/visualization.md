# Visualization — exploring a consolidated mesh

How the result of a consolidation run is shown. Companion to
[milestones.md](milestones.md) (M4, M5) and
[decisions.md](decisions.md) (#11); this doc is the design.

A *consolidation run* is `iormeshctl build`: it merges the
collected trace segments and rebuilds the graph. (`collect` only
syncs segments from the servers; `refresh` runs collect, then
build.) Every build ends with three things to look at, all made
from the same data: the local site (HTML/JS/SVG, `iormeshctl
open`), the terminal UI (Bubble Tea, `iormeshctl tui`) and plain
text (`iormeshctl show …`). How to use them, on a real trace, is
the guide's [7. Read the mesh](guide/07-read-the-mesh.md); this
doc is how they are built.

`build` itself prints the plain-text summary and the path of the
site, so there is always something on screen when it finishes.
The sketch below is the design, not the output: as built,
`refresh` prints collect's per-host `N new files` lines, then
build's summary (`built`, `merged`, `health`, … — see V1 under
[Build order](visualization/builds.md#build-order)) and the graph, model, report and
site paths; the TUI is not in that last line.

```
$ iormeshctl refresh --project shop
collected  3 hosts · 41 segments · 1.2M events · 12:00:00–12:07:31
merged     4 services · 7 edges (6 fused, 1 unresolved)
health     0 drops · 2.1% unattributed · clock bounds ≤ 180µs
unresolved orders → 10.20.3.7:5432  (try: iormeshctl expand)

site  ~/.local/share/iormesh/builds/shop/sites/20261118T120731Z-3f9c…/index.html
      iormeshctl open   ·   iormeshctl tui   ·   iormeshctl show
```

## Parts

This file is the index: each part below is its own file. Read the summaries,
then open only the part you need.

- [One view model, three renderers](visualization/view-model.md) — the shared
  view model.
- [The local site, the terminal UI and plain text](visualization/renderers.md)
  — the three renderers.
  - [The local site](visualization/renderers.md#the-local-site)
  - [The terminal UI](visualization/renderers.md#the-terminal-ui)
  - [Plain text](visualization/renderers.md#plain-text)
- [Builds and build order](visualization/builds.md) — namespace and windowed
  builds, the build order.
  - [Namespace and windowed
    builds](visualization/builds.md#namespace-and-windowed-builds)
  - [Build order](visualization/builds.md#build-order)
- [Routed edges and example output](visualization/routed-edges.md) — routed
  edges (M7), a real trace's output.
  - [Routed edges (M7)](visualization/routed-edges.md#routed-edges-m7)
  - [Example output (a real
    trace)](visualization/routed-edges.md#example-output-a-real-trace)

## See also

- [Output contract](output-contract.md) — the data underneath.
- [Implementation guide](implementation.md) — M4 and M5.
