package process

import (
	"strconv"
	"strings"
)

const (
	decimalBase      = 10
	bitSize64        = 64
	canonicalUUIDLen = 36
	uuidHyphenIndex1 = 8
	uuidHyphenIndex2 = 13
	uuidHyphenIndex3 = 18
	uuidHyphenIndex4 = 23
)

type Identity struct {
	BootID    string
	StartTime uint64
}

func (i Identity) String() string {
	if i.IsZero() {
		return ""
	}

	return i.BootID + ":" + strconv.FormatUint(i.StartTime, decimalBase)
}

func (i Identity) Bytes() []byte {
	return []byte(i.String())
}

func (i Identity) IsZero() bool {
	return i.BootID == "" && i.StartTime == 0
}

func ParseIdentity(s string) (Identity, error) {
	if s == "" {
		return Identity{BootID: "", StartTime: 0}, wrapError(ErrMalformedIdentity, "parse_identity")
	}

	bootID, startStr, ok := strings.Cut(s, ":")
	if !ok || !isCanonicalUUID(bootID) {
		return Identity{BootID: "", StartTime: 0}, wrapError(ErrMalformedIdentity, "parse_identity")
	}

	startTime, ok := parseCanonicalStartTime(startStr)
	if !ok {
		return Identity{BootID: "", StartTime: 0}, wrapError(ErrMalformedIdentity, "parse_identity")
	}

	return Identity{BootID: bootID, StartTime: startTime}, nil
}

func isCanonicalUUID(s string) bool {
	if len(s) != canonicalUUIDLen {
		return false
	}

	for i := range len(s) {
		c := s[i]
		switch i {
		case uuidHyphenIndex1, uuidHyphenIndex2, uuidHyphenIndex3, uuidHyphenIndex4:
			if c != '-' {
				return false
			}
		default:
			if !isHexDigit(c) {
				return false
			}
		}
	}

	return true
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

func parseCanonicalStartTime(s string) (uint64, bool) {
	if len(s) == 0 {
		return 0, false
	}

	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}

	val, err := strconv.ParseUint(s, decimalBase, bitSize64)
	if err != nil {
		return 0, false
	}

	return val, true
}
