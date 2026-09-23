package model

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/strictjson"
)

var (
	ErrInvalid    = errors.New("invalid native snapshot")
	ErrVersion    = errors.New("unsupported native snapshot version")
	ErrIncomplete = errors.New("snapshot parity is incomplete")
)

//nolint:recvcheck // Value marshaling and pointer decoding preserve the byte-slice JSON method contracts.
type Bytes []byte

func (b Bytes) MarshalJSONTo(out *jsontext.Encoder) error {
	if err := out.WriteToken(jsontext.String(base64.StdEncoding.EncodeToString(b))); err != nil {
		return oops.In("snapshot.model").With("operation", "encode_bytes").Wrapf(err, "encode bytes")
	}

	return nil
}

func (b *Bytes) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	token, err := in.ReadToken()
	if err != nil || token.Kind() != '"' {
		return ErrInvalid
	}

	value, err := base64.StdEncoding.Strict().DecodeString(token.String())
	if err != nil || base64.StdEncoding.EncodeToString(value) != token.String() {
		return ErrInvalid
	}

	*b = value

	return nil
}

func Encode(m Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	data, err := json.Marshal(m)
	if err != nil {
		return nil, oops.In("snapshot.model").With("operation", "encode_manifest").Wrapf(err, "encode manifest")
	}

	if len(data) > MaxManifestBytes {
		return nil, oops.In("snapshot.model").Code("snapshot_invalid").With("operation", "encode", "phase", "size").Wrap(ErrInvalid)
	}

	return data, nil
}

func Decode(data []byte) (Manifest, error) {
	if len(data) > MaxManifestBytes {
		return Manifest{}, oops.In("snapshot.model").Code("snapshot_invalid").With("operation", "decode", "phase", "size").Wrap(ErrInvalid)
	}

	m, err := strictjson.Decode[Manifest](data)
	if err != nil {
		return Manifest{}, oops.In("snapshot.model").Code("snapshot_invalid").With("operation", "decode", "phase", "json").Wrapf(errors.Join(ErrInvalid, err), "decode manifest")
	}

	if err = m.Validate(); err != nil {
		return Manifest{}, err
	}

	return m, nil
}
