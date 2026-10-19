package session

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"

	"github.com/samber/oops"
)

var ErrInvalidRawBytes = errors.New("invalid raw bytes encoding")

// RawBytes always encodes as strict canonical base64, so session JSON and the peer protocol stay
// independent of the snapshot manifest's readable encoding.
//
//nolint:recvcheck // Value marshaling and pointer decoding preserve the byte-slice JSON method contracts.
type RawBytes []byte

func (b RawBytes) MarshalJSONTo(out *jsontext.Encoder) error {
	if err := out.WriteToken(jsontext.String(base64.StdEncoding.EncodeToString(b))); err != nil {
		return oops.In("session").With("operation", "encode_raw_bytes").Wrapf(err, "encode raw bytes")
	}

	return nil
}

func (b *RawBytes) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	token, err := in.ReadToken()
	if err != nil || token.Kind() != '"' {
		return ErrInvalidRawBytes
	}

	value, err := base64.StdEncoding.Strict().DecodeString(token.String())
	if err != nil || base64.StdEncoding.EncodeToString(value) != token.String() {
		return ErrInvalidRawBytes
	}

	*b = value

	return nil
}
