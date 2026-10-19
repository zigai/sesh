package action

import (
	"bytes"
	"encoding/json/v2"
	"errors"
)

var (
	ErrInvalidPayload = errors.New("invalid action payload")
	ErrInputType      = errors.New("action input has the wrong type")
)

// Definition is a registered action with its typed input and output. Inputs and outputs are
// the JSON contract shared by the CLI and the remote protocol.
type Definition[In, Out any] struct {
	Descriptor

	bind   func(Subject, Values) (In, error)
	report func(Out) string
}

func define[In, Out any](descriptor Descriptor, bind func(Subject, Values) (In, error), report func(Out) string) *Definition[In, Out] {
	if !descriptor.ID.Valid() {
		panic("action: invalid ID " + string(descriptor.ID))
	}

	if _, exists := registry[descriptor.ID]; exists {
		panic("action: duplicate ID " + string(descriptor.ID))
	}

	definition := &Definition[In, Out]{Descriptor: descriptor, bind: bind, report: report}
	registry[descriptor.ID] = definition
	order = append(order, descriptor.ID)

	return definition
}

func (definition *Definition[In, Out]) Bind(subject Subject, values Values) (In, error) {
	var zero In

	if err := definition.matchSubject(subject); err != nil {
		return zero, err
	}

	normalized, err := normalize(definition.Params, values)
	if err != nil {
		return zero, err
	}

	return definition.bind(subject, normalized)
}

func (definition *Definition[In, Out]) Report(output Out) string {
	return definition.report(output)
}

func (definition *Definition[In, Out]) Describe() Descriptor {
	return definition.Descriptor
}

func (definition *Definition[In, Out]) BindAny(subject Subject, values Values) (any, error) {
	return definition.Bind(subject, values)
}

func (definition *Definition[In, Out]) ReportAny(output any) string {
	typed, ok := output.(Out)
	if !ok {
		return ""
	}

	return definition.report(typed)
}

func (definition *Definition[In, Out]) DecodeInput(data []byte) (any, error) {
	return decodeStrict[In](data)
}

func (definition *Definition[In, Out]) EncodeInput(input any) ([]byte, error) {
	typed, ok := input.(In)
	if !ok {
		return nil, Wrap(CodeInvalidArgument, ErrInputType)
	}

	return encode(typed)
}

func (definition *Definition[In, Out]) DecodeOutput(data []byte) (any, error) {
	return decodeStrict[Out](data)
}

func (definition *Definition[In, Out]) EncodeOutput(output any) ([]byte, error) {
	typed, ok := output.(Out)
	if !ok {
		return nil, Wrap(CodeFailed, ErrOutputTypeMismatch)
	}

	return encode(typed)
}

func (definition *Definition[In, Out]) Input(input any) (In, error) {
	typed, ok := input.(In)
	if !ok {
		var zero In

		return zero, Wrap(CodeInvalidArgument, ErrInputType)
	}

	return typed, nil
}

func encode[T any](value T) ([]byte, error) {
	encoded, err := json.Marshal(value, json.Deterministic(true))
	if err != nil {
		return nil, Wrap(CodeInvalidArgument, errors.Join(ErrInvalidPayload, err))
	}

	return encoded, nil
}

func decodeStrict[T any](data []byte) (T, error) {
	var value T

	if err := json.Unmarshal(bytes.TrimSpace(data), &value, json.RejectUnknownMembers(true)); err != nil {
		var zero T

		return zero, Wrap(CodeInvalidArgument, ErrInvalidPayload)
	}

	return value, nil
}
