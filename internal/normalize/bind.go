package normalize

import (
	"maps"
	"regexp"
	"strings"

	"iormesh/internal/schema"
)

// bindings maps expectation symbols to collected values. It is a
// bijection per symbol kind: a symbol keeps the value it first bound,
// and two symbols of one kind never share a value — $fd1 and $fd2 are
// declared as different fd instances, so one instance cannot satisfy
// both. A trail of bound symbols lets the matcher undo a failed attempt
// (a candidate event or thread) without copying the maps.
type bindings struct {
	sym   map[string]string // symbol → value
	owner map[string]string // kind + "\x00" + value → symbol
	trail []string
	// methods resolves java.stack ids for java declarations (Trace.Methods).
	methods map[uint64]schema.Method
}

func newBindings() *bindings {
	return &bindings{sym: map[string]string{}, owner: map[string]string{}}
}

// bind binds sym to val, or reports whether an existing binding agrees.
func (b *bindings) bind(sym, val string) bool {
	if v, ok := b.sym[sym]; ok {
		return v == val
	}
	k := symbolKind(sym) + "\x00" + val
	if _, taken := b.owner[k]; taken {
		return false
	}
	b.sym[sym], b.owner[k] = val, sym
	b.trail = append(b.trail, sym)
	return true
}

// mark returns a point undo can roll back to.
func (b *bindings) mark() int { return len(b.trail) }

// undo removes every binding made after mark m.
func (b *bindings) undo(m int) {
	for len(b.trail) > m {
		sym := b.trail[len(b.trail)-1]
		b.trail = b.trail[:len(b.trail)-1]
		delete(b.owner, symbolKind(sym)+"\x00"+b.sym[sym])
		delete(b.sym, sym)
	}
}

// clone copies the bindings, for keeping the best partial match as a
// diagnostic after the search has undone it.
func (b *bindings) clone() *bindings {
	return &bindings{sym: maps.Clone(b.sym), owner: maps.Clone(b.owner),
		trail: append([]string(nil), b.trail...)}
}

// matchRef matches a symbol (binding it) or a literal against actual.
func (b *bindings) matchRef(r Ref, actual string) bool {
	if r.symbol() {
		return b.bind(r.v, actual)
	}
	return r.v == actual
}

// matchTemplate matches a node-id template such as "file:$h1:$tmp/a.dat"
// against actual, binding its unbound symbols. Bound symbols are
// substituted first, so only the unbound ones are captured. On failure
// nothing stays bound.
func (b *bindings) matchTemplate(tmpl, actual string) bool {
	locs := symbolRE.FindAllStringIndex(tmpl, -1)
	if locs == nil {
		return tmpl == actual
	}
	var re strings.Builder
	var captured []string
	last := 0
	re.WriteString("^")
	for _, l := range locs {
		re.WriteString(regexp.QuoteMeta(tmpl[last:l[0]]))
		sym := tmpl[l[0]:l[1]]
		if v, ok := b.sym[sym]; ok {
			re.WriteString(regexp.QuoteMeta(v))
		} else {
			re.WriteString("(.+?)")
			captured = append(captured, sym)
		}
		last = l[1]
	}
	re.WriteString(regexp.QuoteMeta(tmpl[last:]) + "$")
	return b.bindCaptures(re.String(), captured, actual)
}

// bindCaptures runs the template regexp and binds each captured symbol;
// a symbol captured twice must capture the same value both times.
func (b *bindings) bindCaptures(expr string, syms []string, actual string) bool {
	m := regexp.MustCompile(expr).FindStringSubmatch(actual)
	if m == nil {
		return false
	}
	mk := b.mark()
	for i, sym := range syms {
		if !b.bind(sym, m[i+1]) {
			b.undo(mk)
			return false
		}
	}
	return true
}
