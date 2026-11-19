package agentspool

import (
	"fmt"
	"strings"
)

// Symbol returns the JNI symbol of the native the hook's method reaches —
// the function the tracer's uprobe for this hook attaches to (the Java
// mark, task td4). The hook table names the native by its method name,
// qualified as "owner.name" when it is another class's (NioSocketImpl.accept
// reaches sun/nio/ch/Net.accept); the symbol is JNI's short name for it,
// Java_<class>_<method>, mangled as the JNI specification says. The short
// name is what the JDK exports: none of the hooked natives is overloaded,
// which would need the long name with its signature.
func (h Hook) Symbol() string {
	class, method := h.Class, h.Native
	if i := strings.LastIndexByte(h.Native, '.'); i >= 0 {
		class, method = h.Native[:i], h.Native[i+1:]
	}
	return "Java_" + jniMangle(class) + "_" + jniMangle(method)
}

// jniMangle escapes a class's internal name or a method name the way JNI
// does for symbol names: '/' separates packages as '_', and every character
// that would be ambiguous or is not ASCII alphanumeric is escaped ('_' as
// "_1", ';' as "_2", '[' as "_3", anything else as "_0" and four hex digits
// of its UTF-16 code unit).
func jniMangle(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/':
			b.WriteByte('_')
		case r == '_':
			b.WriteString("_1")
		case r == ';':
			b.WriteString("_2")
		case r == '[':
			b.WriteString("_3")
		case r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			writeUTF16Escape(&b, r)
		}
	}
	return b.String()
}

// writeUTF16Escape writes r as JNI's "_0xxxx" escapes, one per UTF-16 code
// unit (a surrogate pair for a rune outside the basic plane).
func writeUTF16Escape(b *strings.Builder, r rune) {
	if r < 0x10000 {
		fmt.Fprintf(b, "_0%04x", r)
		return
	}
	r -= 0x10000
	fmt.Fprintf(b, "_0%04x_0%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
}
