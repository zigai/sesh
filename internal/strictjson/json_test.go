package strictjson_test

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zigai/sesh/internal/strictjson"
)

type item struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
}

type owner struct {
	Name string `json:"name"`
}

type document struct {
	Title string `json:"title"`
	Owner owner  `json:"owner"`
	Items []item `json:"items"`
	Data  []byte `json:"data"`
}

type opaque struct {
	Value string
}

func (o *opaque) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return fmt.Errorf("read opaque value: %w", err)
	}

	o.Value = string(raw)

	return nil
}

type withOpaque struct {
	Name  string `json:"name"`
	Extra opaque `json:"extra"`
}

type node struct {
	Children []node `json:"children"`
}

type withMap struct {
	Labels map[string]string `json:"labels"`
}

type withIgnored struct {
	Name    string `json:"name"`
	Ignored string `json:"-"`
}

type untagged struct {
	Name string
}

type withUnexported struct {
	Name   string `json:"name"`
	hidden string
}

type withOmitempty struct {
	Name string `json:"name,omitempty"`
}

func TestDecodeAcceptsCompleteNestedDocument(t *testing.T) {
	input := `{"title":"report","owner":{"name":"alice"},"items":[{"id":"a","count":1},{"id":"b","count":2}],"data":"aGVsbG8="}`

	got, err := strictjson.Decode[document]([]byte(input))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	want := document{
		Title: "report",
		Owner: owner{Name: "alice"},
		Items: []item{{ID: "a", Count: 1}, {ID: "b", Count: 2}},
		Data:  []byte("hello"),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Decode() mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeAcceptsEmptySlice(t *testing.T) {
	got, err := strictjson.Decode[document](
		[]byte(`{"title":"t","owner":{"name":"n"},"items":[],"data":""}`),
	)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("Items = %#v, want empty non-nil slice", got.Items)
	}
}

func TestDecodeDoesNotRequireMembersOfCustomUnmarshaler(t *testing.T) {
	got, err := strictjson.Decode[withOpaque]([]byte(`{"name":"n","extra":{"anything":1}}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	want := withOpaque{Name: "n", Extra: opaque{Value: `{"anything":1}`}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Decode() mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeRejectsInvalidDocument(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"missing top-level field", `{"title":"t","owner":{"name":"n"},"items":[]}`},
		{"missing nested struct field", `{"title":"t","owner":{},"items":[],"data":""}`},
		{
			"missing field in slice element",
			`{"title":"t","owner":{"name":"n"},"items":[{"id":"a","count":1},{"id":"b"}],"data":""}`,
		},
		{"null string", `{"title":null,"owner":{"name":"n"},"items":[],"data":""}`},
		{"null struct", `{"title":"t","owner":null,"items":[],"data":""}`},
		{"null slice", `{"title":"t","owner":{"name":"n"},"items":null,"data":""}`},
		{"unknown member", `{"title":"t","owner":{"name":"n"},"items":[],"data":"","extra":1}`},
		{
			"unknown nested member",
			`{"title":"t","owner":{"name":"n","age":3},"items":[],"data":""}`,
		},
		{"type mismatch", `{"title":5,"owner":{"name":"n"},"items":[],"data":""}`},
		{"trailing data", `{"title":"t","owner":{"name":"n"},"items":[],"data":""} {}`},
		{"malformed", `{"title":"t",`},
		{"empty input", ``},
		{"top-level null", `null`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := strictjson.Decode[document]([]byte(test.input))
			if !errors.Is(err, strictjson.ErrInvalid) {
				t.Fatalf("Decode() error = %v, want ErrInvalid", err)
			}

			if diff := cmp.Diff(document{}, got); diff != "" {
				t.Fatalf("Decode() returned non-zero value on failure (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeRejectsUnsupportedFieldKind(t *testing.T) {
	got, err := strictjson.Decode[withMap]([]byte(`{"labels":{"a":"b"}}`))
	if !errors.Is(err, strictjson.ErrInvalid) {
		t.Fatalf("Decode() error = %v, want ErrInvalid", err)
	}

	if got.Labels != nil {
		t.Fatalf("Labels = %#v, want nil on failure", got.Labels)
	}
}

func nestedNodes(levels int) string {
	return strings.Repeat(
		`{"children":[`,
		levels-1,
	) + `{"children":[]}` + strings.Repeat(
		`]}`,
		levels-1,
	)
}

func TestDecodeLimitsNestingDepth(t *testing.T) {
	tests := []struct {
		name      string
		levels    int
		wantError bool
	}{
		{"shallow", 3, false},
		{"64 JSON levels", 32, false},
		{"66 JSON levels", 33, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := strictjson.Decode[node]([]byte(nestedNodes(test.levels)))
			if test.wantError && !errors.Is(err, strictjson.ErrInvalid) {
				t.Fatalf("Decode() error = %v, want ErrInvalid", err)
			}

			if !test.wantError && err != nil {
				t.Fatalf("Decode() error = %v, want nil", err)
			}
		})
	}
}

func TestDecodeIgnoresDashTaggedField(t *testing.T) {
	got, err := strictjson.Decode[withIgnored]([]byte(`{"name":"n"}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if diff := cmp.Diff(withIgnored{Name: "n"}, got); diff != "" {
		t.Fatalf("Decode() mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeIgnoresUnexportedField(t *testing.T) {
	got, err := strictjson.Decode[withUnexported]([]byte(`{"name":"n"}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if got.Name != "n" || got.hidden != "" {
		t.Fatalf("Decode() = %+v, want Name n and empty hidden", got)
	}
}

func TestDecodeNamesFieldWithoutTagNameByGoName(t *testing.T) {
	got, err := strictjson.Decode[untagged]([]byte(`{"Name":"n"}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if diff := cmp.Diff(untagged{Name: "n"}, got); diff != "" {
		t.Fatalf("Decode() mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeRequiresUntaggedField(t *testing.T) {
	_, err := strictjson.Decode[untagged]([]byte(`{}`))
	if !errors.Is(err, strictjson.ErrInvalid) {
		t.Fatalf("Decode() error = %v, want ErrInvalid", err)
	}
}

func TestDecodeRequiresOmitemptyField(t *testing.T) {
	_, err := strictjson.Decode[withOmitempty]([]byte(`{}`))
	if !errors.Is(err, strictjson.ErrInvalid) {
		t.Fatalf("Decode() error = %v, want ErrInvalid", err)
	}
}
