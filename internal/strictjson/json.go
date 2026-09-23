package strictjson

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"reflect"
	"strings"
)

const maxDepth = 64

var ErrInvalid = errors.New("invalid native JSON")

func Decode[T any](data []byte) (T, error) {
	var value T
	if err := json.Unmarshal(data, &value, json.RejectUnknownMembers(true)); err != nil {
		var zero T
		return zero, ErrInvalid
	}

	if err := required(data, reflect.TypeFor[T](), 0); err != nil {
		var zero T
		return zero, err
	}

	return value, nil
}

func required(data []byte, typ reflect.Type, depth int) error {
	if depth > maxDepth || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return ErrInvalid
	}

	if reflect.PointerTo(typ).Implements(reflect.TypeFor[json.UnmarshalerFrom]()) {
		return nil
	}

	switch typ.Kind() {
	case reflect.Struct:
		return requiredFields(data, typ, depth)
	case reflect.Slice:
		return requiredElements(data, typ, depth)
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.UnsafePointer:
		return ErrInvalid
	}

	return ErrInvalid
}

func requiredFields(data []byte, typ reflect.Type, depth int) error {
	var members map[string]jsontext.Value
	if err := json.Unmarshal(data, &members); err != nil {
		return ErrInvalid
	}

	for field := range typ.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")

		raw, ok := members[name]
		if !ok {
			return ErrInvalid
		}

		if err := required(raw, field.Type, depth+1); err != nil {
			return err
		}
	}

	return nil
}

func requiredElements(data []byte, typ reflect.Type, depth int) error {
	if typ.Elem().Kind() == reflect.Uint8 {
		return nil
	}

	var elements []jsontext.Value
	if err := json.Unmarshal(data, &elements); err != nil {
		return ErrInvalid
	}

	for _, raw := range elements {
		if err := required(raw, typ.Elem(), depth+1); err != nil {
			return err
		}
	}

	return nil
}
