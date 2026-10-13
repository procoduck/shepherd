package beacon

import (
	"bytes"
	"errors"
	"net/http"
	"testing"

	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

// FuzzDecodeWriteRequest feeds arbitrary collector-supplied remote_write
// bodies (snappy-framed protobuf) through the decoder the beacon ingest
// handler runs. Plain `go test` runs only the seed corpus (the f.Add calls,
// plus testdata/fuzz/FuzzDecodeWriteRequest/ once a failing input is saved
// there as a regression seed); fuzz with
// `go test -run='^$' -fuzz=FuzzDecodeWriteRequest -fuzztime=60s ./internal/beacon`.
//
// Properties: never a panic; a body over the cap is refused with
// ErrBodyTooLarge; a body whose declared decoded length exceeds the ratio is
// refused with ErrDecodedTooLarge before snappy allocates it; and whatever
// decodes also passes through Project without a panic.
func FuzzDecodeWriteRequest(f *testing.F) {
	seed := func(wr *prompb.WriteRequest) []byte {
		raw, err := wr.Marshal()
		if err != nil {
			f.Fatalf("marshal seed: %v", err)
		}
		return snappy.Encode(nil, raw)
	}
	f.Add(seed(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series([]prompb.Label{lbl("__name__", runningComponentsMetric)}, 1),
	}}), uint16(1<<12))
	f.Add(seed(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series([]prompb.Label{lbl("__name__", runningComponentsMetric), lbl("padding", string(bytes.Repeat([]byte("x"), 200)))}, 1),
	}}), uint16(64))
	f.Add([]byte{0x80, 0x80, 0x80, 0x80, 0x04}, uint16(1<<12)) // declares 1 GiB, carries nothing
	f.Add(snappy.Encode(nil, []byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0x0f}), uint16(1<<12))
	f.Add([]byte{}, uint16(0))

	f.Fuzz(func(t *testing.T, body []byte, maxBytes uint16) {
		limit := int64(maxBytes)
		wr, err := DecodeWriteRequest(http.Header{}, bytes.NewReader(body), limit)
		if int64(len(body)) > limit {
			if !errors.Is(err, ErrBodyTooLarge) {
				t.Fatalf("%d-byte body over a %d-byte cap: err = %v, want ErrBodyTooLarge", len(body), limit, err)
			}
			return
		}
		if n, lenErr := snappy.DecodedLen(body); lenErr == nil && int64(n) > limit*MaxDecodedRatio {
			if !errors.Is(err, ErrDecodedTooLarge) {
				t.Fatalf("body declaring %d decoded bytes under a %d-byte cap: err = %v, want ErrDecodedTooLarge", n, limit, err)
			}
			return
		}
		if err != nil {
			if wr != nil {
				t.Fatalf("error %v with a non-nil request", err)
			}
			return
		}
		// Project may refuse what decoded (no collector id, say); it must not
		// panic on it.
		if _, _, _, err := Project(wr); err != nil {
			return
		}
	})
}
