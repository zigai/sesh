package model

import (
	"errors"
	"testing"

	"github.com/samber/oops"
)

func TestValidationDiagnosticsIdentifyFailedDomain(t *testing.T) {
	m := fixture()
	m.Links[0].Window = "private-missing-window"

	err := m.Validate()
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("lost invalid-snapshot identity")
	}

	diagnostic, ok := oops.AsOops(err)
	if !ok || diagnostic.Code() != "snapshot_invalid" {
		t.Fatal("missing structured validation code")
	}

	fields := diagnostic.Context()
	if len(fields) != 2 || fields["operation"] != "validate" || fields["phase"] != "links" {
		t.Fatal("missing validation phase or private fields captured")
	}

	if diagnostic.Stacktrace() == "" {
		t.Fatal("missing diagnostic stack")
	}

	m = fixture()
	m.Version++
	_, err = Encode(m)

	diagnostic, ok = oops.AsOops(err)
	if !ok || !errors.Is(err, ErrVersion) || diagnostic.Code() != "snapshot_version_unsupported" {
		t.Fatal("lost version classification")
	}
}
