package generate

import (
	"strings"
	"testing"
)

const directDecodeTypesH = `#define ADDR_LEN 16
#define PATH_LEN ADDR_LEN
#define DEPTH 4

struct flat_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __s32 fd;
    __u32 kind;
    __s64 ret;
    __u8 addr[ADDR_LEN];
    char path[PATH_LEN];
    __u64 frames[DEPTH];
    __u32 reserved;
};

struct holey_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __s32 fd;
    __s64 ret;
};

struct unknown_len_event {
    __u32 event_type;
    char name[NOT_DEFINED];
};
`

// TestGenerateTypesGoDirectDecode pins the generator side of task am4: a
// struct without internal padding decodes with fixed-offset little-endian
// loads (an array length resolved through a #define naming a #define), a
// struct whose C layout has a hole (an __s64 after an odd __s32) or an
// unresolvable array length keeps the reflective binary.Read, which reads
// packed and would disagree with fixed C offsets.
func TestGenerateTypesGoDirectDecode(t *testing.T) {
	structs, constants, err := ParseCTypesInput(strings.NewReader(directDecodeTypesH))
	if err != nil {
		t.Fatal(err)
	}
	output := GenerateTypesGo(structs, constants)
	flat := constructorBody(t, output, "FlatEvent")
	for _, want := range []string{
		"if len(raw) < 100 {",
		"f.EventType = EventType(binary.LittleEndian.Uint32(raw[0:]))",
		"f.TraceId = TraceId(binary.LittleEndian.Uint32(raw[4:]))",
		"f.Time = binary.LittleEndian.Uint64(raw[8:])",
		"f.Fd = int32(binary.LittleEndian.Uint32(raw[16:]))",
		"f.Kind = binary.LittleEndian.Uint32(raw[20:])",
		"f.Ret = int64(binary.LittleEndian.Uint64(raw[24:]))",
		"copy(f.Addr[:], raw[32:48])",
		"copy(f.Path[:], raw[48:64])",
		"f.Frames[i] = binary.LittleEndian.Uint64(raw[64+i*8:])",
		"f.Reserved = binary.LittleEndian.Uint32(raw[96:])",
	} {
		requireContains(t, flat, want)
	}
	requireNotContains(t, flat, "binary.Read")
	for _, name := range []string{"HoleyEvent", "UnknownLenEvent"} {
		requireContains(t, constructorBody(t, output, name), "binary.Read(bytes.NewReader(raw)")
	}
}

// constructorBody returns the generated New<goName> function.
func constructorBody(t *testing.T, output, goName string) string {
	t.Helper()
	start := strings.Index(output, "func New"+goName+"(raw []byte)")
	if start < 0 {
		t.Fatalf("New%s not generated", goName)
	}
	end := strings.Index(output[start:], "\n}\n")
	return output[start : start+end]
}
