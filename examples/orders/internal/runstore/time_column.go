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
	// binaryTime stores a time.Time column as its UTC binary encoding plus,
	// for any location other than UTC, the zone offset in seconds and the zone
	// name. Every representable instant and offset round-trips; a non-UTC
	// location is returned as a fixed zone with the same name and offset.
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
	if value.Location() == time.UTC {
		return append([]byte{timeUTC}, instant...), nil
	}
	name, offset := value.Zone()
	data := []byte{timeZoned}
	data = binary.BigEndian.AppendUint32(data, uint32(int32(offset))) //nolint:gosec // Zone offsets fit in int32 seconds.
	data = binary.BigEndian.AppendUint16(data, uint16(len(name)))     //nolint:gosec // Zone names are short abbreviations.
	data = append(data, name...)
	return append(data, instant...), nil
}

// Scan decodes a column written by Value.
func (v *binaryTime) Scan(src any) error {
	data, ok := src.([]byte)
	if !ok || len(data) == 0 {
		return fmt.Errorf("time column holds %T, want encoded bytes", src)
	}
	var (
		zoned  = data[0] == timeZoned
		name   string
		offset int32
	)
	data = data[1:]
	if zoned {
		if len(data) < 6 {
			return errors.New("time column zone is truncated")
		}
		offset = int32(binary.BigEndian.Uint32(data)) //nolint:gosec // Value wrote an int32 offset.
		size := int(binary.BigEndian.Uint16(data[4:]))
		if len(data) < 6+size {
			return errors.New("time column zone name is truncated")
		}
		name, data = string(data[6:6+size]), data[6+size:]
	}
	var instant time.Time
	if err := instant.UnmarshalBinary(data); err != nil {
		return fmt.Errorf("decode time column: %w", err)
	}
	if zoned {
		instant = instant.In(time.FixedZone(name, int(offset)))
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
