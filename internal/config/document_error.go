package config

import (
	"errors"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	DocumentUnknownKey   DocumentErrorReason = "unknown_field"
	DocumentDuplicateKey DocumentErrorReason = "duplicate_key"
	DocumentInvalidType  DocumentErrorReason = "invalid_type"
	DocumentDecodeError  DocumentErrorReason = "decode_error"
)

var (
	utf8BOM         = []byte("\xef\xbb\xbf")
	decodedTOMLType = regexp.MustCompile(`cannot decode TOML (\w+) into`)
)

type DocumentErrorReason string

// DocumentError describes a TOML problem in a configuration file. Key and
// GotType are empty when the decoder does not report them.
type DocumentError struct {
	Line    int
	Column  int
	Key     string
	Reason  DocumentErrorReason
	GotType string
}

func AsDocumentError(err error) (DocumentError, bool) {
	if strictErr, ok := errors.AsType[*toml.StrictMissingError](err); ok && len(strictErr.Errors) > 0 {
		return newDocumentError(&strictErr.Errors[0], DocumentUnknownKey), true
	}

	decodeErr, ok := errors.AsType[*toml.DecodeError](err)
	if !ok {
		return DocumentError{Line: 0, Column: 0, Key: "", Reason: "", GotType: ""}, false
	}

	msg := decodeErr.Error()

	switch {
	case strings.Contains(msg, "already defined"), strings.Contains(msg, "already exists"):
		return newDocumentError(decodeErr, DocumentDuplicateKey), true
	case strings.Contains(msg, "cannot decode"), strings.Contains(msg, "cannot store"):
		docErr := newDocumentError(decodeErr, DocumentInvalidType)
		if match := decodedTOMLType.FindStringSubmatch(msg); match != nil {
			docErr.GotType = match[1]
		}

		return docErr, true
	default:
		return newDocumentError(decodeErr, DocumentDecodeError), true
	}
}

func newDocumentError(decodeErr *toml.DecodeError, reason DocumentErrorReason) DocumentError {
	line, column := decodeErr.Position()

	return DocumentError{
		Line:    line,
		Column:  column,
		Key:     strings.Join(decodeErr.Key(), "."),
		Reason:  reason,
		GotType: "",
	}
}
