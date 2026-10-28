package simulate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/prometheus/prometheus/model/value"
)

// Wire spellings of the non-finite sample values. The first three are the
// spellings Prometheus itself uses in its text exposition format; the stale
// marker gets its own because it is a distinct NaN bit pattern with its own
// meaning (the series ended), not "the value was NaN".
const (
	sampleNaN      = "NaN"
	samplePosInf   = "+Inf"
	sampleNegInf   = "-Inf"
	sampleStaleNaN = "StaleNaN"
)

// SampleValue is a captured Prometheus sample value on the simulator control
// API's JSON wire. encoding/json refuses NaN and ±Inf outright, and real
// Alloy self-metrics carry them (an empty summary's quantiles are NaN), so a
// plain float64 here turned a perfectly good run into an encode failure.
//
// A finite value encodes as a JSON number, unchanged from the plain-float64
// shape. NaN, +Inf and -Inf encode as the strings "NaN", "+Inf" and "-Inf",
// and the Prometheus stale marker as "StaleNaN". Decoding is the exact
// inverse: the stale marker comes back with its own bit pattern
// (value.IsStaleNaN holds), any other NaN as math.NaN().
type SampleValue float64

// MarshalJSON implements json.Marshaler.
func (v SampleValue) MarshalJSON() ([]byte, error) {
	f := float64(v)
	switch {
	case value.IsStaleNaN(f):
		return []byte(`"` + sampleStaleNaN + `"`), nil
	case math.IsNaN(f):
		return []byte(`"` + sampleNaN + `"`), nil
	case math.IsInf(f, 1):
		return []byte(`"` + samplePosInf + `"`), nil
	case math.IsInf(f, -1):
		return []byte(`"` + sampleNegInf + `"`), nil
	}
	return json.Marshal(f)
}

// UnmarshalJSON implements json.Unmarshaler. It accepts a JSON number or one
// of the four non-finite spellings MarshalJSON writes; anything else is an
// error rather than a silent zero.
func (v *SampleValue) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return nil // encoding/json convention: null leaves the value as it was
	}
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("sample value: %w", err)
		}
		switch s {
		case sampleNaN:
			*v = SampleValue(math.NaN())
		case samplePosInf:
			*v = SampleValue(math.Inf(1))
		case sampleNegInf:
			*v = SampleValue(math.Inf(-1))
		case sampleStaleNaN:
			*v = SampleValue(math.Float64frombits(value.StaleNaN))
		default:
			return fmt.Errorf("sample value: unrecognised string %q", s)
		}
		return nil
	}
	f, err := strconv.ParseFloat(string(data), 64)
	if err != nil {
		return fmt.Errorf("sample value: %w", err)
	}
	*v = SampleValue(f)
	return nil
}
