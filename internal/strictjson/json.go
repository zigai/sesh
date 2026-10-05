package strictjson

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
)

const maxDepth = 64

var ErrInvalid = errors.New("invalid native JSON")

var structFieldsCache sync.Map

type structFields struct {
	types  []reflect.Type
	byName map[string][]int
}

func Decode[T any](data []byte) (T, error) {
	var value T
	if err := json.Unmarshal(data, &value, json.RejectUnknownMembers(true)); err != nil {
		var zero T
		return zero, ErrInvalid
	}

	if err := required(jsontext.NewDecoder(bytes.NewReader(data)), reflect.TypeFor[T](), 0); err != nil {
		var zero T
		return zero, err
	}

	return value, nil
}

func required(dec *jsontext.Decoder, typ reflect.Type, depth int) error {
	if depth > maxDepth || dec.PeekKind() == 'n' {
		return ErrInvalid
	}

	if reflect.PointerTo(typ).Implements(reflect.TypeFor[json.UnmarshalerFrom]()) {
		return skip(dec)
	}

	switch typ.Kind() {
	case reflect.Struct:
		return requiredFields(dec, typ, depth)
	case reflect.Slice:
		return requiredElements(dec, typ, depth)
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return skip(dec)
	case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.UnsafePointer:
		return ErrInvalid
	}

	return ErrInvalid
}

func requiredFields(dec *jsontext.Decoder, typ reflect.Type, depth int) error {
	if dec.PeekKind() != '{' {
		return ErrInvalid
	}

	if _, err := dec.ReadToken(); err != nil {
		return ErrInvalid
	}

	fields := fieldsOf(typ)
	present := make([]bool, len(fields.types))

	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return ErrInvalid
		}

		indexes := fields.byName[name.String()]
		if err := requiredMember(dec, fields, indexes, depth); err != nil {
			return err
		}

		for _, index := range indexes {
			present[index] = true
		}
	}

	if _, err := dec.ReadToken(); err != nil || slices.Contains(present, false) {
		return ErrInvalid
	}

	return nil
}

func requiredMember(dec *jsontext.Decoder, fields *structFields, indexes []int, depth int) error {
	switch len(indexes) {
	case 0:
		return skip(dec)
	case 1:
		return required(dec, fields.types[indexes[0]], depth+1)
	default:
		raw, err := dec.ReadValue()
		if err != nil {
			return ErrInvalid
		}

		for _, index := range indexes {
			if err := required(jsontext.NewDecoder(bytes.NewReader(raw)), fields.types[index], depth+1); err != nil {
				return err
			}
		}

		return nil
	}
}

func requiredElements(dec *jsontext.Decoder, typ reflect.Type, depth int) error {
	if typ.Elem().Kind() == reflect.Uint8 {
		return skip(dec)
	}

	if dec.PeekKind() != '[' {
		return ErrInvalid
	}

	if _, err := dec.ReadToken(); err != nil {
		return ErrInvalid
	}

	for dec.PeekKind() != ']' {
		if err := required(dec, typ.Elem(), depth+1); err != nil {
			return err
		}
	}

	if _, err := dec.ReadToken(); err != nil {
		return ErrInvalid
	}

	return nil
}

func fieldsOf(typ reflect.Type) *structFields {
	if cached, ok := structFieldsCache.Load(typ); ok {
		if fields, ok := cached.(*structFields); ok {
			return fields
		}
	}

	fields := &structFields{types: nil, byName: make(map[string][]int)}

	for field := range typ.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		fields.byName[name] = append(fields.byName[name], len(fields.types))
		fields.types = append(fields.types, field.Type)
	}

	structFieldsCache.Store(typ, fields)

	return fields
}

func skip(dec *jsontext.Decoder) error {
	if err := dec.SkipValue(); err != nil {
		return ErrInvalid
	}

	return nil
}
