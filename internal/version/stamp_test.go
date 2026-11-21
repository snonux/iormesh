package version

import (
	"errors"
	"strings"
	"testing"
)

func TestStamped(t *testing.T) {
	if got := Stamped(); got != Version {
		t.Fatalf("Stamped() = %q, want %q", got, Version)
	}
	if !strings.HasPrefix(Stamp, "\x00"+stampTag) || !strings.HasSuffix(Stamp, "\x00") {
		t.Fatalf("Stamp %q is not NUL-delimited behind the tag", Stamp)
	}
}

func TestEmbedded(t *testing.T) {
	stamp := func(v string) string { return "\x00" + stampTag + v + "\x00" }
	tests := []struct {
		name, data, want string
		err              error // nil: want; ErrNoStamp; errAny: any other
	}{
		{"one stamp", "\x7fELF..." + stamp("1.2.3") + "more", "1.2.3", nil},
		{"own stamp", "x" + Stamp + "y", Version, nil},
		{"repeated", stamp("0.4.0") + "abc" + stamp("0.4.0"), "0.4.0", nil},
		// The tag spelled in Embedded itself, followed by another string,
		// is skipped; so is a stamp that does not parse.
		{"tag then junk", "\x00" + stampTag + "Premain-Class\x00" + stamp("0.2.0"), "0.2.0", nil},
		{"tag then nothing", stamp("0.2.0") + "\x00" + stampTag, "0.2.0", nil},
		{"bad version skipped", stamp("v1.0.0") + stamp("01.0.0"), "", ErrNoStamp},
		{"unterminated", "\x00" + stampTag + "1.2.3", "", ErrNoStamp},
		{"too long", "\x00" + stampTag + strings.Repeat("1", maxStampValue+1) + "\x00", "", ErrNoStamp},
		{"no NUL before tag", "x" + stampTag + "1.2.3\x00", "", ErrNoStamp},
		{"none", "\x7fELF", "", ErrNoStamp},
		{"two versions", stamp("0.1.0") + stamp("0.2.0"), "", errAny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Embedded([]byte(tt.data))
			switch {
			case tt.err == nil && (err != nil || got != tt.want):
				t.Fatalf("Embedded = %q, %v; want %q", got, err, tt.want)
			case tt.err == errAny && (err == nil || errors.Is(err, ErrNoStamp)):
				t.Fatalf("Embedded = %q, %v; want a conflict error", got, err)
			case tt.err == ErrNoStamp && !errors.Is(err, ErrNoStamp):
				t.Fatalf("Embedded = %q, %v; want ErrNoStamp", got, err)
			}
		})
	}
}

var errAny = errors.New("any error")
