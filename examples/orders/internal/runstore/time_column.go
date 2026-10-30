package runstore

// Stored times keep their instant, zone offset and zone name for every value
// the reference store accepts. time.Time.MarshalBinary alone cannot encode a
// -60 second offset (it reserves -1 minute for UTC), so only the UTC instant
// uses it and the zone is stored beside it. Prompt text is gob-encoded
// because JSON would replace invalid UTF-8 that the reference store keeps.

import (
	"bytes"
	"database/sql/driver"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"time"
)

type (
	// binaryTime stores a time.Time column as a kind byte, the length of
	// the UTC instant's MarshalBinary encoding and that encoding, followed for
	// any location other than UTC by the zone offset in seconds as a varint
	// and the zone name as the remaining bytes. Every offset and name a
	// time.Time can hold round-trips; a non-UTC location is returned as a
	// fixed zone with the same name and offset.
	binaryTime time.Time
)

const (
	// timeUTC marks a binaryTime encoding whose location is UTC.
	timeUTC byte = 0
	// timeZoned marks a binaryTime encoding that carries an offset and name.
	timeZoned byte = 1
)

// Value encodes the time as described by binaryTime.
func (v binaryTime) Value() (driver.Value, error) {
	value := time.Time(v)
	instant, err := value.UTC().MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("encode time: %w", err)
	}
	data := append([]byte{timeUTC, byte(len(instant))}, instant...) //nolint:gosec // A UTC MarshalBinary encoding is at most 16 bytes.
	if value.Location() == time.UTC {
		return data, nil
	}
	name, offset := value.Zone()
	data[0] = timeZoned
	data = binary.AppendVarint(data, int64(offset))
	return append(data, name...), nil
}

// Scan decodes a column written by Value.
func (v *binaryTime) Scan(src any) error {
	data, ok := src.([]byte)
	if !ok || len(data) < 2 || len(data) < 2+int(data[1]) {
		return fmt.Errorf("time column holds %T, want encoded bytes", src)
	}
	zoned, instantBytes, rest := data[0] == timeZoned, data[2:2+int(data[1])], data[2+int(data[1]):]
	var instant time.Time
	if err := instant.UnmarshalBinary(instantBytes); err != nil {
		return fmt.Errorf("decode time column: %w", err)
	}
	if zoned {
		offset, size := binary.Varint(rest)
		if size <= 0 {
			return errors.New("time column zone offset is invalid")
		}
		instant = instant.In(time.FixedZone(string(rest[size:]), int(offset)))
	}
	*v = binaryTime(instant)
	return nil
}

// gobEncode encodes one stored value with gob, which keeps strings byte for
// byte.
func gobEncode(value any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := gob.NewEncoder(&buffer).Encode(value); err != nil {
		return nil, fmt.Errorf("encode stored value: %w", err)
	}
	return buffer.Bytes(), nil
}
