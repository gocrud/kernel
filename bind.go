package kernel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// flatten converts a nested map into a dotted-key map: {"app":{"port":80}}
// becomes {"app.port":80}. Slices and scalars are kept as-is.
func flatten(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		mm, ok := v.(map[string]any)
		if !ok {
			out[prefix] = v
			return
		}
		for k, vv := range mm {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			walk(key, vv)
		}
	}
	walk("", m)
	return out
}

// mergeInto overlays incoming dotted keys onto target: same key later wins.
func mergeInto(target map[string]any, incoming map[string]any) {
	for k, v := range incoming {
		target[k] = v
	}
}

// sectionValue extracts the subtree addressed by section from a flattened
// dotted-key map. An empty section selects the whole map. Returns ok=false when
// the section has no values at all.
func sectionValue(merged map[string]any, section string) (any, bool) {
	if section == "" {
		if len(merged) == 0 {
			return nil, false
		}
		return merged, true
	}
	if v, ok := merged[section]; ok {
		return v, true
	}
	prefix := section + "."
	sub := make(map[string]any)
	for k, v := range merged {
		if strings.HasPrefix(k, prefix) {
			sub[strings.TrimPrefix(k, prefix)] = v
		}
	}
	if len(sub) == 0 {
		return nil, false
	}
	return sub, true
}

// nest converts a dotted-key map back into a nested map. Keys are processed in
// sorted order so a scalar at "a" and children at "a.b" resolve deterministically.
func nest(m map[string]any) map[string]any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]any)
	for _, k := range keys {
		parts := strings.Split(k, ".")
		cur := out
		for i, p := range parts {
			if i == len(parts)-1 {
				cur[p] = m[k]
				break
			}
			next, ok := cur[p].(map[string]any)
			if !ok {
				next = make(map[string]any)
				cur[p] = next
			}
			cur = next
		}
	}
	return out
}

// fillValue fills dst from src, converting types as needed. path is used for
// error messages.
func fillValue(dst reflect.Value, src any, path string) error {
	if src == nil {
		return nil
	}
	if dst.Kind() == reflect.Struct {
		m, ok := src.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object, got %T", path, src)
		}
		return fillStruct(dst, m, path)
	}
	cv, err := convertTo(dst.Type(), src)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	dst.Set(cv)
	return nil
}

func fillStruct(dst reflect.Value, m map[string]any, path string) error {
	t := dst.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		key := fieldKey(f)
		if key == "-" {
			continue
		}
		val, present := m[key]
		if !present {
			continue
		}
		if err := fillValue(dst.Field(i), val, path+"."+key); err != nil {
			return err
		}
	}
	return nil
}

// fieldKey returns the binding key of a struct field: `config` tag first,
// then `json` tag, then the snake_cased field name.
func fieldKey(f reflect.StructField) string {
	if tag := f.Tag.Get("config"); tag != "" {
		if k, _, _ := strings.Cut(tag, ","); k != "" {
			return k
		}
	}
	if tag := f.Tag.Get("json"); tag != "" {
		if k, _, _ := strings.Cut(tag, ","); k != "" {
			return k
		}
	}
	return toSnake(f.Name)
}

// toSnake converts CamelCase names to snake_case.
func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				prev := rune(s[i-1])
				nextLower := i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z'
				if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') ||
					(prev >= 'A' && prev <= 'Z' && nextLower) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// convertTo converts src into a value of type t.
func convertTo(t reflect.Type, src any) (reflect.Value, error) {
	if src == nil {
		return reflect.Zero(t), nil
	}
	sv := reflect.ValueOf(src)
	if sv.IsValid() && sv.Type().AssignableTo(t) {
		return sv, nil
	}
	if t.Kind() == reflect.Interface && sv.IsValid() {
		return sv, nil
	}
	if t.Kind() == reflect.Ptr {
		pv := reflect.New(t.Elem())
		if err := fillValue(pv.Elem(), src, ""); err != nil {
			return reflect.Value{}, err
		}
		return pv, nil
	}
	if t == reflect.TypeFor[time.Duration]() {
		d, err := toDuration(src)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(d), nil
	}

	switch t.Kind() {
	case reflect.String:
		if sv.Kind() == reflect.String {
			return sv.Convert(t), nil
		}
		return reflect.ValueOf(fmt.Sprint(src)).Convert(t), nil
	case reflect.Bool:
		b, err := toBool(src)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(b), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := toInt64(src)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(t).Elem()
		if out.OverflowInt(n) {
			return reflect.Value{}, fmt.Errorf("value %d overflows %s", n, t)
		}
		out.SetInt(n)
		return out, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := toUint64(src)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(t).Elem()
		if out.OverflowUint(n) {
			return reflect.Value{}, fmt.Errorf("value %d overflows %s", n, t)
		}
		out.SetUint(n)
		return out, nil
	case reflect.Float32, reflect.Float64:
		f, err := toFloat64(src)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(t).Elem()
		if out.OverflowFloat(f) {
			return reflect.Value{}, fmt.Errorf("value %v overflows %s", f, t)
		}
		out.SetFloat(f)
		return out, nil
	case reflect.Slice:
		av, ok := src.([]any)
		if !ok {
			return reflect.Value{}, fmt.Errorf("expected array, got %T", src)
		}
		out := reflect.MakeSlice(t, len(av), len(av))
		for i, item := range av {
			ev, err := convertTo(t.Elem(), item)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("[%d]: %w", i, err)
			}
			out.Index(i).Set(ev)
		}
		return out, nil
	case reflect.Map:
		m, ok := src.(map[string]any)
		if !ok {
			return reflect.Value{}, fmt.Errorf("expected object, got %T", src)
		}
		out := reflect.MakeMapWithSize(t, len(m))
		for k, v := range m {
			ev, err := convertTo(t.Elem(), v)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("%s: %w", k, err)
			}
			kv := reflect.ValueOf(k).Convert(t.Key())
			out.SetMapIndex(kv, ev)
		}
		return out, nil
	case reflect.Struct:
		m, ok := src.(map[string]any)
		if !ok {
			return reflect.Value{}, fmt.Errorf("expected object, got %T", src)
		}
		pv := reflect.New(t)
		if err := fillStruct(pv.Elem(), m, ""); err != nil {
			return reflect.Value{}, err
		}
		return pv.Elem(), nil
	}
	return reflect.Value{}, fmt.Errorf("cannot convert %T to %s", src, t)
}

func toBool(src any) (bool, error) {
	switch v := src.(type) {
	case bool:
		return v, nil
	case string:
		return strconv.ParseBool(v)
	case json.Number:
		return strconv.ParseBool(v.String())
	}
	return false, fmt.Errorf("cannot convert %T to bool", src)
}

func toInt64(src any) (int64, error) {
	switch v := src.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, nil
		}
		if f, err := v.Float64(); err == nil {
			return int64(f), nil
		}
		return 0, fmt.Errorf("cannot convert %q to int", v.String())
	case string:
		if n, err := strconv.ParseInt(v, 0, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return int64(f), nil
		}
		return 0, fmt.Errorf("cannot convert %q to int", v)
	}
	return 0, fmt.Errorf("cannot convert %T to int", src)
}

func toUint64(src any) (uint64, error) {
	switch v := src.(type) {
	case uint:
		return uint64(v), nil
	case uint8:
		return uint64(v), nil
	case uint16:
		return uint64(v), nil
	case uint32:
		return uint64(v), nil
	case uint64:
		return v, nil
	case int:
		return uint64(v), nil
	case int8:
		return uint64(v), nil
	case int16:
		return uint64(v), nil
	case int32:
		return uint64(v), nil
	case int64:
		return uint64(v), nil
	case float64:
		return uint64(v), nil
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return uint64(n), nil
		}
		if f, err := v.Float64(); err == nil {
			return uint64(f), nil
		}
		return 0, fmt.Errorf("cannot convert %q to uint", v.String())
	case string:
		if n, err := strconv.ParseUint(v, 0, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return uint64(f), nil
		}
		return 0, fmt.Errorf("cannot convert %q to uint", v)
	}
	return 0, fmt.Errorf("cannot convert %T to uint", src)
}

func toFloat64(src any) (float64, error) {
	switch v := src.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int8:
		return float64(v), nil
	case int16:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint8:
		return float64(v), nil
	case uint16:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case json.Number:
		return v.Float64()
	case string:
		return strconv.ParseFloat(v, 64)
	}
	return 0, fmt.Errorf("cannot convert %T to float", src)
}

func toDuration(src any) (time.Duration, error) {
	switch v := src.(type) {
	case string:
		return time.ParseDuration(v)
	case int:
		return time.Duration(v), nil
	case int64:
		return time.Duration(v), nil
	case float64:
		return time.Duration(v), nil
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return time.Duration(n), nil
		}
		return 0, fmt.Errorf("cannot convert %q to duration", v.String())
	}
	return 0, fmt.Errorf("cannot convert %T to duration", src)
}
