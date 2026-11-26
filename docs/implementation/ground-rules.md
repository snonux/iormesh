# Ground rules

Part of [Implementation guide](../implementation.md).

Work order: M0 → M1 → S3 → M2 → S1 → M3 → S2 → M4 → M5 → M6 → M7.
S1 and S2 touch nothing the earlier packages own, so they can start
any time after M1 if a second agent is available.

Before the first line of code, read in `../ior`: `AGENTS.md`
("Build/Test Commands", "Code Generation", "Generation host /
kernel", "Code Style") and `integrationtests/README.md`.

Conventions (ior's, kept):

- **Module**: `module iormesh`, imports as
  `"iormesh/internal/<pkg>"` — ior uses the bare name `ior` the
  same way. Go version: whatever ior's `go.mod` declares.
- **Build through Mage**: `mage build`, `mage test`, `mage vet`,
  `mage lint`, `mage generate`, `mage integrationTest`. Bare
  `go test ./...` fails on packages that import libbpfgo, because
  only Mage wires `CGO_CFLAGS`/`CGO_LDFLAGS`/`LIBBPFGO`.
- **Functions stay under 50 lines**; split into helpers of about
  30. `main.go` files stay thin — flag parsing and one call into
  `internal/`.
- **Comments say why**, and are updated with the code they
  describe.
- **Errors are returned**, compared with `errors.Is`; a
  deliberately dropped error is written `_ = f.Close()`.
  `//nolint` is banned (ior enforces this with a test — copy it).
- **Provenance header** on every file copied or adapted from ior:

  ```go
  // Adapted from ior internal/generate/codegen.go @ <ior commit>.
  // Changes: <one line>.
  ```

- **Docs** wrap at 79 columns; commits are `area: summary`,
  pushed after each change-set.

## The cgo boundary

The tracer needs cgo (libbpfgo). The controller must build with
`CGO_ENABLED=0` for darwin and linux. That splits the tree in two,
and a test keeps it split:

| cgo allowed (tracer only)        | cgo-free (shared)              |
|----------------------------------|--------------------------------|
| `internal/bpf` (loader, ringbuf) | `internal/schema`              |
| `internal/correlate`             | `internal/normalize`           |
| `cmd/iormesh`                    | `internal/federate`, `expand`  |
|                                  | `internal/servicedb`, `query`  |
|                                  | `internal/render`              |
|                                  | `cmd/iormeshctl`               |

```go
// internal/buildgate/cgofree_test.go
// TestControllerIsCgoFree fails when anything the controller
// imports pulls in cgo — the darwin cross-build would break long
// after the offending import was added.
func TestControllerIsCgoFree(t *testing.T) {
    out, err := exec.Command("go", "list", "-deps",
        "-f", "{{if .CgoFiles}}{{.ImportPath}}{{end}}",
        "./cmd/iormeshctl").Output()
    if err != nil {
        t.Fatal(err)
    }
    if pkgs := strings.Fields(string(out)); len(pkgs) > 0 {
        t.Fatalf("cgo packages in iormeshctl: %v", pkgs)
    }
}
```
