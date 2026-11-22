package routemap

import (
	"errors"
	"testing"
)

// TestPathTemplates: the static map's path_templates compile into the
// table, where they win over the built-in folding; a discovery
// snapshot's are ignored (discovery never writes any, and the operator's
// rules belong to the operator's file); no map is the built-in folding.
func TestPathTemplates(t *testing.T) {
	m, err := Parse("paths", []byte("path_templates:\n  - /shops/{shop}/orders/{order}\n  - /status/404\n"))
	if err != nil {
		t.Fatal(err)
	}
	tab := Compile(m, &Map{PathTemplates: []string{"/x/{y}"}}, nil)
	for in, want := range map[string]string{
		"/shops/acme/orders/7": "/shops/{shop}/orders/{order}",
		"/status/404":          "/status/404",
		"/status/500":          "/status/{id}",
		"/x/abc":               "/x/abc",
	} {
		if got := tab.Paths().Template(in); got != want {
			t.Errorf("Template(%q) = %q, want %q", in, got, want)
		}
	}
	if !tab.Empty() {
		t.Error("a map of path templates only names no route: the table should be empty")
	}
	var none *Table
	if got := none.Paths().Template("/orders/1"); got != "/orders/{id}" {
		t.Errorf("no table: %q, want the built-in fold", got)
	}
}

// TestPathTemplatesRejected: a bad template fails the map's load like
// any bad entry, so a typo never silently stops folding.
func TestPathTemplatesRejected(t *testing.T) {
	for _, doc := range []string{
		"path_templates:\n  - orders/{id}\n",
		"path_templates:\n  - /orders/{id\n",
		"path_templates:\n  - /a/{x}\n  - /a/{x}\n",
	} {
		if _, err := Parse("bad", []byte(doc)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err %v, want ErrInvalid", doc, err)
		}
	}
}
