package generate

import (
	"strings"
	"testing"
)

// TestParseCTypesInputRejectsUnparsedMembers holds the types.h parser to its
// contract (task em4): a member line inside a struct it cannot parse is an
// error naming the line, never silently dropped. A dropped member shifts
// every later field of both the direct decoder and the binary.Read fallback.
func TestParseCTypesInputRejectsUnparsedMembers(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"bitfield", "__u32 flags : 3;"},
		{"bitfield packed", "__u32 flags:3;"},
		{"nested struct member", "struct inner nested;"},
		{"multi-word type", "unsigned int x;"},
		{"pointer", "char *name;"},
		{"union", "union {"},
		{"nested struct definition", "struct inner {"},
		{"unclosed comment", "__u32 x; /* open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := "struct bad_event {\n    __u32 event_type;\n    " +
				tc.line + "\n    __u32 tid;\n};\n"
			_, _, err := ParseCTypesInput(strings.NewReader(input))
			if err == nil {
				t.Fatalf("%q parsed without error", tc.line)
			}
			for _, want := range []string{"line 3", "bad_event", tc.line} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

// TestParseCTypesInputStripsTrailingComments: a member with a trailing
// inline comment is a member, parsed as if the comment were not there.
func TestParseCTypesInputStripsTrailingComments(t *testing.T) {
	input := `struct c_event {
    __u32 event_type; // the kind
    __u64 time;  /* ns */
    char name[MAX_NAME]; // the name; may be cut
};
`
	structs, _, err := ParseCTypesInput(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []CMember{
		{TypeName: "__u32", FieldName: "event_type"},
		{TypeName: "__u64", FieldName: "time"},
		{TypeName: "char", FieldName: "name", ArraySize: "MAX_NAME"},
	}
	if len(structs) != 1 || len(structs[0].Members) != len(want) {
		t.Fatalf("structs = %+v", structs)
	}
	for i, m := range want {
		if structs[0].Members[i] != m {
			t.Errorf("member %d = %+v, want %+v", i, structs[0].Members[i], m)
		}
	}
}

// TestParseCTypesInputSkipsLocalStructs: the generated C declares local
// structs inside handler bodies ("struct __ior_timespec { ... } ts = {};").
// A block closed by anything but "};" declares a variable, not a record
// type: it is dropped, and the code after it is not read as members.
func TestParseCTypesInputSkipsLocalStructs(t *testing.T) {
	input := `int handler(void *ctx) {
        struct __ior_timespec {
            __s64 tv_sec;
            __s64 tv_nsec;
        } ts = {};
        if (bpf_probe_read_user(&ts, sizeof(ts), ptr) == 0) {
            ev->timeout_ns = ts.tv_sec;
        }
}
struct null_event {
    __u32 event_type;
};
`
	structs, _, err := ParseCTypesInput(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(structs) != 1 || structs[0].Name != "null_event" || len(structs[0].Members) != 1 {
		t.Fatalf("structs = %+v, want only null_event", structs)
	}
}

// TestParseCTypesInputRejectsUnterminatedStruct: input that ends inside a
// struct is an error rather than a struct silently left out.
func TestParseCTypesInputRejectsUnterminatedStruct(t *testing.T) {
	_, _, err := ParseCTypesInput(strings.NewReader("struct open_event {\n    __u32 event_type;\n"))
	if err == nil || !strings.Contains(err.Error(), "open_event") {
		t.Fatalf("err = %v, want an unterminated open_event error", err)
	}
}
