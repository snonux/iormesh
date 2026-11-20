package project

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{"shop", true},
		{"Shop-2.v1_x", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"", false},
		{"-shop", false},
		{".shop", false},
		{"_shop", false},
		{"sh op", false},
		{"sh/op", false},
		{"sh'op", false},
		{"..", false},
	}
	for _, c := range cases {
		if got := Valid(c.label); got != c.want {
			t.Errorf("Valid(%q) = %v, want %v", c.label, got, c.want)
		}
	}
}
