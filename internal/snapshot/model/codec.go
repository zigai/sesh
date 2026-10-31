package model

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"unicode/utf8"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/strictjson"
)

const bytesBase64Member = "base64"

var (
	ErrInvalid    = errors.New("invalid native snapshot")
	ErrVersion    = errors.New("unsupported native snapshot version")
	ErrIncomplete = errors.New("snapshot parity is incomplete")
)

// Bytes encodes valid UTF-8 as a JSON string and anything else as {"base64": "..."}; each
// sequence has exactly one accepted encoding.
//
//nolint:recvcheck // Value marshaling and pointer decoding preserve the byte-slice JSON method contracts.
type Bytes []byte

func (b Bytes) MarshalJSONTo(out *jsontext.Encoder) error {
	if utf8.Valid(b) {
		return wrapBytesEncoding(out.WriteToken(jsontext.String(string(b))))
	}

	for _, token := range []jsontext.Token{
		jsontext.BeginObject,
		jsontext.String(bytesBase64Member),
		jsontext.String(base64.StdEncoding.EncodeToString(b)),
		jsontext.EndObject,
	} {
		if err := out.WriteToken(token); err != nil {
			return wrapBytesEncoding(err)
		}
	}

	return nil
}

func (b *Bytes) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	token, err := in.ReadToken()
	if err != nil {
		return ErrInvalid
	}

	if token.Kind() == '"' {
		*b = Bytes(token.String())

		return nil
	}

	if token.Kind() != '{' {
		return ErrInvalid
	}

	value, err := decodeBase64Bytes(in)
	if err != nil {
		return err
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

	var header struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	// Manifests of another schema fail strict decoding, so read the version first to report it.
	if json.Unmarshal(data, &header) == nil && header.Format == "sesh" && header.Version != Schema {
		return Manifest{}, oops.In("snapshot.model").Code("snapshot_version_unsupported").With("operation", "decode", "phase", "version").Wrap(ErrVersion)
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

func decodeBase64Bytes(in *jsontext.Decoder) (Bytes, error) {
	member, err := in.ReadToken()
	if err != nil || member.Kind() != '"' || member.String() != bytesBase64Member {
		return nil, ErrInvalid
	}

	encoded, err := in.ReadToken()
	if err != nil || encoded.Kind() != '"' {
		return nil, ErrInvalid
	}

	value, err := base64.StdEncoding.Strict().DecodeString(encoded.String())
	if err != nil || base64.StdEncoding.EncodeToString(value) != encoded.String() || utf8.Valid(value) {
		return nil, ErrInvalid
	}

	end, err := in.ReadToken()
	if err != nil || end.Kind() != '}' {
		return nil, ErrInvalid
	}

	return value, nil
}

func wrapBytesEncoding(err error) error {
	if err == nil {
		return nil
	}

	return oops.In("snapshot.model").With("operation", "encode_bytes").Wrapf(err, "encode bytes")
}
