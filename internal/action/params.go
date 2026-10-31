package action

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	ParamText ParamKind = iota
	ParamPath
	ParamBool
	ParamList
	ParamChoice
)

const (
	SuggestNone Suggestion = iota
	SuggestSessionDirectory
)

var (
	ErrUnknownParam  = errors.New("unknown action parameter")
	ErrMissingParam  = errors.New("required action parameter is missing")
	ErrInvalidParam  = errors.New("invalid action parameter value")
	ErrRepeatedParam = errors.New("action parameter accepts one value")
)

type (
	ParamKind  uint8
	Suggestion uint8
)

type Param struct {
	Name       string
	Kind       ParamKind
	Usage      string
	Short      string
	Default    string
	Choices    []string
	Required   bool
	Positional bool
	FromQuery  bool
	Suggest    Suggestion
	CreateWith string
}

type Values map[string][]string

func (values Values) Set(name string, value ...string) {
	values[name] = value
}

func (values Values) Has(name string) bool {
	_, ok := values[name]

	return ok
}

func (values Values) String(name string) string {
	if items := values[name]; len(items) > 0 {
		return items[0]
	}

	return ""
}

func (values Values) Strings(name string) []string {
	return slices.Clone(values[name])
}

func (values Values) Bool(name string) bool {
	parsed, _ := strconv.ParseBool(values.String(name))

	return parsed
}

func normalize(params []Param, values Values) (Values, error) {
	normalized := make(Values, len(params))

	for name := range values {
		if !slices.ContainsFunc(params, func(param Param) bool { return param.Name == name }) {
			return nil, Wrap(CodeInvalidArgument, fmt.Errorf("%w: %s", ErrUnknownParam, name))
		}
	}

	for _, param := range params {
		items, present := values[param.Name]
		if !present && param.Default != "" {
			items, present = []string{param.Default}, true
		}

		if !present {
			if param.Required {
				return nil, Wrap(CodeInvalidArgument, fmt.Errorf("%w: %s", ErrMissingParam, param.Name))
			}

			continue
		}

		checked, err := param.check(items)
		if err != nil {
			return nil, err
		}

		normalized[param.Name] = checked
	}

	return normalized, nil
}

func (param Param) check(items []string) ([]string, error) {
	if param.Kind == ParamList {
		cleaned := make([]string, 0, len(items))
		for _, item := range items {
			if item = strings.TrimSpace(item); item != "" {
				cleaned = append(cleaned, item)
			}
		}

		return cleaned, nil
	}

	if len(items) != 1 {
		return nil, Wrap(CodeInvalidArgument, fmt.Errorf("%w: %s", ErrRepeatedParam, param.Name))
	}

	value, err := param.checkScalar(items[0])
	if err != nil {
		return nil, err
	}

	return []string{value}, nil
}

func (param Param) checkScalar(value string) (string, error) {
	switch param.Kind {
	case ParamText, ParamPath:
		if param.Required && strings.TrimSpace(value) == "" {
			return "", Wrap(CodeInvalidArgument, fmt.Errorf("%w: %s", ErrMissingParam, param.Name))
		}

		return value, nil
	case ParamBool:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return "", Wrap(CodeInvalidArgument, fmt.Errorf("%w: %s must be true or false", ErrInvalidParam, param.Name))
		}

		return strconv.FormatBool(parsed), nil
	case ParamChoice:
		if !slices.Contains(param.Choices, value) {
			return "", Wrap(CodeInvalidArgument, fmt.Errorf("%w: %s must be one of %s", ErrInvalidParam, param.Name, strings.Join(param.Choices, ", ")))
		}

		return value, nil
	case ParamList:
		return value, nil
	default:
		return "", Wrap(CodeInvalidArgument, fmt.Errorf("%w: %s", ErrInvalidParam, param.Name))
	}
}

func newParam(name string, kind ParamKind, usage string) Param {
	return Param{
		Name: name, Kind: kind, Usage: usage, Short: "", Default: "", Choices: nil,
		Required: false, Positional: false, FromQuery: false, Suggest: SuggestNone, CreateWith: "",
	}
}

func (param Param) withShort(short string) Param {
	param.Short = short

	return param
}

func (param Param) withDefault(value string) Param {
	param.Default = value

	return param
}

func (param Param) withChoices(choices ...string) Param {
	param.Choices = choices

	return param
}

func (param Param) required() Param {
	param.Required = true

	return param
}

func (param Param) positional() Param {
	param.Positional = true

	return param
}

func (param Param) fromQuery() Param {
	param.FromQuery = true

	return param
}

func (param Param) suggest(suggestion Suggestion) Param {
	param.Suggest = suggestion

	return param
}

func (param Param) createWith(flag string) Param {
	param.CreateWith = flag

	return param
}
