package generate

import (
	"fmt"
	"strconv"
	"strings"
)

// Direct record decoders (task am4). The generated New<Event> constructors
// used to fill the Go struct with reflective binary.Read, which costs a
// reflection walk and two allocations per record: on the 232-byte
// sock_addr_event of a netlink-heavy load it was 40 % of the tracer's CPU
// (task ak4). The generator knows every field's type, array length and
// offset, so it emits plain little-endian loads at fixed offsets instead.
//
// The decode keeps binary.Read's contract exactly: fields are read packed,
// in declaration order (binary.Read never skips padding), a payload shorter
// than the field sum yields nil and bytes past it (the kernel's tail padding
// to the struct's alignment) are ignored. The packed offsets equal the C
// offsets only when the C struct has no internal padding; the generator
// checks that against the natural alignment of every C member and keeps the
// reflective decoder for a struct where they differ, so a layout change that
// introduces a hole between parsed members cannot silently shift a direct
// read. That check sees only the members the types.h parser returns, so
// ParseCTypesInput fails on any member line it cannot parse (a bitfield, a
// nested struct or union, a multi-word type, a pointer) instead of dropping
// it (task em4); a dropped member would shift the direct read and the
// binary.Read fallback alike. A member line's trailing comment is stripped.

// decodeField is one Go struct field of a direct decoder: its element type
// and size, its array length (0 for a scalar) and its packed offset.
type decodeField struct {
	goField  string
	goType   string
	elemSize int
	count    int
	offset   int
}

// goElemSizes are the Go element types a direct decoder can load, by size.
var goElemSizes = map[string]int{
	"byte": 1, "int32": 4, "uint32": 4, "EventType": 4, "TraceId": 4,
	"int64": 8, "uint64": 8,
}

// planDirectDecode lays out members (the C members plus any compatibility
// fields) packed, as binary.Read reads them, and returns the fields with
// their offsets and the packed size. ok is false when a member has a type or
// array length the generator cannot resolve, or when a C member's natural
// C offset differs from its packed offset (internal padding).
func planDirectDecode(cMembers, members []CMember, consts map[string]string) ([]decodeField, int, bool) {
	fields := make([]decodeField, 0, len(members))
	packed, cOffset := 0, 0
	for i, m := range members {
		goType := goElemType(m)
		size, known := goElemSizes[goType]
		if !known {
			return nil, 0, false
		}
		count := 0
		if m.ArraySize != "" {
			n, ok := resolveArraySize(m.ArraySize, consts)
			if !ok {
				return nil, 0, false
			}
			count = n
		}
		if i < len(cMembers) {
			cOffset = alignUp(cOffset, size)
			if cOffset != packed {
				return nil, 0, false
			}
			cOffset += size * max(count, 1)
		}
		fields = append(fields, decodeField{goField: snakeToCamel(m.FieldName),
			goType: goType, elemSize: size, count: count, offset: packed})
		packed += size * max(count, 1)
	}
	return fields, packed, true
}

// goElemType is the Go element type goMemberDef gives a member.
func goElemType(m CMember) string {
	switch snakeToCamel(m.FieldName) {
	case "TraceId":
		return "TraceId"
	case "EventType":
		return "EventType"
	}
	return cTypeToGoType(m.TypeName)
}

// resolveArraySize turns an array length (a literal or a #define name,
// possibly naming another #define) into a number.
func resolveArraySize(expr string, consts map[string]string) (int, bool) {
	for range 8 {
		if n, err := strconv.Atoi(expr); err == nil {
			return n, n > 0
		}
		v, ok := consts[expr]
		if !ok {
			return 0, false
		}
		expr = v
	}
	return 0, false
}

func alignUp(off, align int) int {
	return (off + align - 1) / align * align
}

// writeDirectDecode emits the body of New<goName> after any record-specific
// validation: the length check, the pooled value and one load per field.
func writeDirectDecode(b *strings.Builder, goName, selfRef string, fields []decodeField, size int) {
	fmt.Fprintf(b, "\tif len(raw) < %d {\n\t\treturn nil\n\t}\n", size)
	fmt.Fprintf(b, "\t%s := poolOf%ss.Get().(*%s)\n", selfRef, goName, goName)
	for _, f := range fields {
		ref := selfRef + "." + f.goField
		switch {
		case f.count > 0 && f.elemSize == 1:
			fmt.Fprintf(b, "\tcopy(%s[:], raw[%d:%d])\n", ref, f.offset, f.offset+f.count)
		case f.count > 0:
			fmt.Fprintf(b, "\tfor i := range %s {\n", ref)
			fmt.Fprintf(b, "\t\t%s[i] = %s\n", ref, loadExpr(f, fmt.Sprintf("%d+i*%d", f.offset, f.elemSize)))
			b.WriteString("\t}\n")
		default:
			fmt.Fprintf(b, "\t%s = %s\n", ref, loadExpr(f, strconv.Itoa(f.offset)))
		}
	}
	fmt.Fprintf(b, "\treturn %s\n}\n\n", selfRef)
}

// loadExpr is the little-endian load of one element of f at offset off (a
// Go expression), converted to the field's Go type.
func loadExpr(f decodeField, off string) string {
	var load string
	switch f.elemSize {
	case 1:
		return fmt.Sprintf("raw[%s]", off)
	case 4:
		load = fmt.Sprintf("binary.LittleEndian.Uint32(raw[%s:])", off)
		if f.goType == "uint32" {
			return load
		}
	default:
		load = fmt.Sprintf("binary.LittleEndian.Uint64(raw[%s:])", off)
		if f.goType == "uint64" {
			return load
		}
	}
	return fmt.Sprintf("%s(%s)", f.goType, load)
}

// writeReflectiveDecode emits the binary.Read body, kept for a struct whose
// C layout has internal padding or a member the direct decoder cannot plan.
func writeReflectiveDecode(b *strings.Builder, goName, selfRef string) {
	fmt.Fprintf(b, "\t%s := poolOf%ss.Get().(*%s)\n", selfRef, goName, goName)
	fmt.Fprintf(b, "\tif err := binary.Read(bytes.NewReader(raw), binary.LittleEndian, %s); err != nil {\n", selfRef)
	fmt.Fprintf(b, "\t\t*%s = %s{}\n", selfRef, goName)
	fmt.Fprintf(b, "\t\tpoolOf%ss.Put(%s)\n", goName, selfRef)
	b.WriteString("\t\treturn nil\n\t}\n")
	fmt.Fprintf(b, "\treturn %s\n}\n\n", selfRef)
}
