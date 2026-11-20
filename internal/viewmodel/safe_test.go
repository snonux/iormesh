package viewmodel

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
)

// injected is what a compromised server would plant in every string it
// controls: a CSI that clears the screen, a C1 CSI and a bidi override.
const injected = "x\x1b[2J\u009b‮"

// maxDepth bounds the reflection walk; the model is a tree a few levels
// deep, so this only stops a recursive type added by mistake.
const maxDepth = 12

// TestSafeCoversEveryString fills every exported string of a Model —
// through structs, slices, maps and pointers, each slice with two
// elements — with an escape sequence, runs Safe and requires every
// string to be printable. Safe lists the fields by hand (safe.go), so a
// field added to the model later without a line there fails here
// instead of reaching the terminal (the TUI, the text views).
func TestSafeCoversEveryString(t *testing.T) {
	var m Model
	fill(reflect.ValueOf(&m).Elem(), 0)
	if len(m.Conversations) == 0 || len(m.Conversations[0].Steps) == 0 ||
		!strings.Contains(m.Conversations[0].Steps[0].Label, "\x1b") {
		t.Fatal("fill did not reach the nested strings")
	}
	n := 0
	check(t, reflect.ValueOf(Safe(m)), "Model", 0, &n)
	if n < 50 {
		t.Fatalf("only %d strings checked: the walk missed the model", n)
	}
}

// fill sets every settable string under v to injected.
func fill(v reflect.Value, depth int) {
	if depth > maxDepth {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(injected)
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fill(v.Elem(), depth+1)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 2, 2))
		for i := range v.Len() {
			fill(v.Index(i), depth+1)
		}
	case reflect.Map:
		fillMap(v, depth)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), depth+1)
			}
		}
	}
}

// fillMap gives a map one entry, key and value filled.
func fillMap(v reflect.Value, depth int) {
	k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
	fill(k, depth+1)
	fill(e, depth+1)
	v.Set(reflect.MakeMap(v.Type()))
	v.SetMapIndex(k, e)
}

// check fails on any exported string under v holding a non-printable
// rune (termsafe's rule), naming its path; n counts the strings seen.
func check(t *testing.T, v reflect.Value, path string, depth int, n *int) {
	t.Helper()
	if depth > maxDepth {
		return
	}
	switch v.Kind() {
	case reflect.String:
		*n++
		if strings.IndexFunc(v.String(), func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			t.Errorf("%s = %q: not terminal-safe (add it to safe.go)", path, v.String())
		}
	case reflect.Pointer:
		if !v.IsNil() {
			check(t, v.Elem(), path, depth+1, n)
		}
	case reflect.Slice:
		for i := range v.Len() {
			check(t, v.Index(i), path+"[]", depth+1, n)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			check(t, k, path+"{key}", depth+1, n)
			check(t, v.MapIndex(k), path+"{}", depth+1, n)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Type().Field(i); f.IsExported() {
				check(t, v.Field(i), path+"."+f.Name, depth+1, n)
			}
		}
	}
}
