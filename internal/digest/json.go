package digest

import (
	"encoding/json"
	"os"
	"reflect"
)

// PrintJSON is the one way any --json output reaches stdout. An unset Go
// slice encodes as null; callers expect [] for "nothing found", so empty
// lists are filled in here rather than at every place a list is built.
func PrintJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if v == nil {
		return enc.Encode(v)
	}
	return enc.Encode(emptyLists(reflect.ValueOf(v)).Interface())
}

// emptyLists returns a copy of v with every nil slice replaced by an empty one,
// so the caller's data is never mutated.
func emptyLists(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return v
		}
		if v.Kind() == reflect.Pointer {
			p := reflect.New(v.Type().Elem())
			p.Elem().Set(emptyLists(v.Elem()))
			return p
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(emptyLists(v.Elem()))
		return out
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return v // []byte encodes as a base64 string, not a list
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(emptyLists(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			out.SetMapIndex(it.Key(), emptyLists(it.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := range v.NumField() {
			if f := out.Field(i); f.CanSet() {
				f.Set(emptyLists(v.Field(i)))
			}
		}
		return out
	}
	return v
}
