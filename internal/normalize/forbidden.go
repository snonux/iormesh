package normalize

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// checkForbidden scans every string in the trace — every string field of
// every event, nested ones included, and every node and edge of the graph
// — for each must_not_appear entry. It walks the values by reflection
// rather than searching marshaled JSON, because JSON escaping (<, >, &,
// non-ASCII) would hide a planted secret from a plain substring search.
// Fields appended to the schema later are covered without touching this.
func checkForbidden(got Trace, forbidden []string) []Violation {
	if len(forbidden) == 0 {
		return nil
	}
	type hit struct {
		count int
		first string
	}
	hits := make([]hit, len(forbidden))
	visit := func(where, s string) {
		for i, f := range forbidden {
			if strings.Contains(s, f) {
				if hits[i].count == 0 {
					hits[i].first = where
				}
				hits[i].count++
			}
		}
	}
	for _, e := range got.Events {
		walkStrings(reflect.ValueOf(e), fmt.Sprintf("event[seq=%d]", e.Seq), visit)
	}
	walkStrings(reflect.ValueOf(got.Graph), "graph", visit)
	var vs []Violation
	for i, h := range hits {
		if h.count > 0 {
			vs = append(vs, Violation{ViolationMustNotAppear, fmt.Sprintf(
				"%q appears %d time(s), first in %s", forbidden[i], h.count, h.first)})
		}
	}
	return vs
}

// walkStrings calls visit for every string reachable from v, with a
// dotted path naming where it was found.
func walkStrings(v reflect.Value, where string, visit func(where, s string)) {
	switch v.Kind() {
	case reflect.String:
		visit(where, v.String())
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkStrings(v.Elem(), where, visit)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			walkStrings(v.Field(i), where+"."+fieldName(v.Type().Field(i)), visit)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walkStrings(v.Index(i), fmt.Sprintf("%s[%d]", where, i), visit)
		}
	case reflect.Map:
		// Sorted keys: the reported first location must not depend on
		// map iteration order.
		keys := v.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int {
			return cmp.Compare(fmt.Sprint(a), fmt.Sprint(b))
		})
		for _, k := range keys {
			walkStrings(k, where+"{key}", visit)
			walkStrings(v.MapIndex(k), fmt.Sprintf("%s[%v]", where, k), visit)
		}
	}
}

// fieldName is the field's JSON name, the name a reader of the trace
// knows it by.
func fieldName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}
	return name
}
