package beacon

import (
	"bytes"
	"errors"
	"net/http"
	"testing"

	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

func encodeWriteRequest(t *testing.T, wr *prompb.WriteRequest) []byte {
	t.Helper()
	raw, err := wr.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return snappy.Encode(nil, raw)
}

func TestDecodeWriteRequest_RoundTrip(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series([]prompb.Label{lbl("__name__", runningComponentsMetric)}, 1),
	}}
	body := encodeWriteRequest(t, wr)

	got, err := DecodeWriteRequest(http.Header{}, bytes.NewReader(body), 1<<20)
	if err != nil {
		t.Fatalf("DecodeWriteRequest: %v", err)
	}
	if len(got.Timeseries) != 1 {
		t.Fatalf("got %d timeseries, want 1", len(got.Timeseries))
	}
}

func TestDecodeWriteRequest_RemoteWriteV2Refused(t *testing.T) {
	h := http.Header{}
	h.Set("X-Prometheus-Remote-Write-Version", "2.0.0")
	_, err := DecodeWriteRequest(h, bytes.NewReader(nil), 1<<20)
	if !errors.Is(err, ErrRemoteWriteV2) {
		t.Fatalf("err = %v, want ErrRemoteWriteV2", err)
	}
}

// TestDecodeWriteRequest_BodyTooLarge is the library-level red run for the
// body-size cap (doc.go): a body of exactly maxBytes decodes; one byte more
// is refused with ErrBodyTooLarge, never silently truncated and decoded.
//
// Red run: change `io.LimitReader(body, maxBytes+1)` to
// `io.LimitReader(body, maxBytes)` in decode.go (removing the +1 headroom
// this test relies on to detect an over-cap body). This test then fails
// because the too-large body is read down to exactly maxBytes bytes,
// decodes as if it were a valid (truncated) frame or a generic decode
// error, and ErrBodyTooLarge is never returned — the cap silently stops
// being distinguishable from "malformed body".
func TestDecodeWriteRequest_BodyTooLarge(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series([]prompb.Label{lbl("__name__", runningComponentsMetric), lbl("padding", string(bytes.Repeat([]byte("x"), 2000)))}, 1),
	}}
	body := encodeWriteRequest(t, wr)

	if len(body) < 100 {
		t.Fatalf("test body too small to exercise a cap below it: %d bytes", len(body))
	}

	// Cap set below the actual body size: must be refused.
	_, err := DecodeWriteRequest(http.Header{}, bytes.NewReader(body), int64(len(body)-1))
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge for a body one byte over the cap", err)
	}

	// Cap set to exactly the body size: must succeed.
	if _, err := DecodeWriteRequest(http.Header{}, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("DecodeWriteRequest at exactly the cap: %v", err)
	}
}

// TestDecodeWriteRequest_DeclaredDecompressedSizeCapped covers the allocation
// the body-size cap cannot see. snappy.Decode allocates the length declared in
// the payload header before validating any of it, so a body that passes
// maxBytes can still ask for an arbitrary allocation. The payload below is a
// hand-built snappy header — a varint length and nothing else — which is
// exactly the shape an attacker would send: tiny on the wire, enormous when
// believed.
//
// Red run, executed: deleting the DecodedLen check in decode.go leaves this
// test failing with `want ErrDecodedTooLarge, got beacon: snappy decode:
// snappy: corrupt input` — i.e. the request is refused only AFTER snappy has
// already allocated the gigabyte the check exists to prevent.
func TestDecodeWriteRequest_DeclaredDecompressedSizeCapped(t *testing.T) {
	const maxBytes = 1 << 20

	// A snappy block is a varint decoded-length followed by the compressed
	// stream. Declaring 1 GiB costs 5 bytes here.
	var hdr []byte
	for v := uint64(1 << 30); ; {
		if v < 0x80 {
			hdr = append(hdr, byte(v))
			break
		}
		hdr = append(hdr, byte(v)|0x80)
		v >>= 7
	}

	declared, err := snappy.DecodedLen(hdr)
	if err != nil {
		t.Fatalf("building the probe payload: %v", err)
	}
	if declared != 1<<30 {
		t.Fatalf("probe declares %d bytes, want %d — the test would not exercise the cap", declared, 1<<30)
	}
	if int64(len(hdr)) > maxBytes {
		t.Fatalf("probe payload is %d bytes, which the size cap would reject first — "+
			"this test must exercise the DECODED-length cap, not the body cap", len(hdr))
	}

	_, err = DecodeWriteRequest(http.Header{}, bytes.NewReader(hdr), maxBytes)
	if !errors.Is(err, ErrDecodedTooLarge) {
		t.Fatalf("want ErrDecodedTooLarge for a body declaring %d decompressed bytes, got %v", declared, err)
	}
}
