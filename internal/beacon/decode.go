package beacon

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

// ErrBodyTooLarge is returned by DecodeWriteRequest when the request body
// exceeds maxBytes. Named and exported so a caller (internal/agentapi) can
// map it to a specific status code (413) rather than a generic 400 — an
// oversized body and a malformed one are different failures with different
// remediations.
var ErrBodyTooLarge = errors.New("beacon: request body exceeds the configured size cap")

// ErrRemoteWriteV2 is returned for a Remote-Write 2.0 body. The 2.0 wire
// format interns label strings in a symbol table; decoding it with a 1.0
// prompb.WriteRequest unmarshaler yields empty/garbage labels rather than an
// error — a silent wrong answer this package refuses to produce. Verified
// against real Alloy v1.18.1 (internal/simsvc/capture_prom.go carries the
// same check, independently arrived at for the same reason): Alloy sends 1.0
// by default and the version header names anything else explicitly.
var ErrRemoteWriteV2 = errors.New("beacon: remote write 2.0 is not supported")

// ErrDecodedTooLarge is returned when a body is within the size cap but
// DECLARES a decompressed size beyond MaxDecodedRatio × that cap.
//
// This is a distinct control from ErrBodyTooLarge, and the size cap does not
// subsume it. snappy.Decode reads the decoded length out of the payload's own
// header and allocates it up front — `dst = make([]byte, dLen)` in
// golang/snappy@v1.0.0 decode.go:65 — *before* decoding validates anything.
// So a few hundred compressed bytes declaring a multi-gigabyte length is a
// single allocation of that size, and the compression ratio never enters
// into it. Capping the declared length is the only place this can be caught,
// because by the time Decode returns the allocation has already happened.
var ErrDecodedTooLarge = errors.New("beacon: declared decompressed size exceeds the allowed ratio")

// MaxDecodedRatio bounds declared decompressed size as a multiple of the
// body-size cap. Real payloads here are protobuf-encoded label sets, which
// snappy compresses well but not extravagantly; 20× is generous for a
// legitimate sender and still bounds the worst case to a bounded allocation
// rather than an attacker-chosen one.
const MaxDecodedRatio = 20

// DecodeWriteRequest turns a prometheus.remote_write request body into the
// prompb.WriteRequest it carries: snappy block format wrapping a protobuf
// message. maxBytes bounds how many raw (pre-decompression) bytes are ever
// read — this is the library-level half of the body-size cap; see doc.go and
// internal/agentapi's handler for the HTTP-level half
// (http.MaxBytesReader), which this call independently re-enforces rather
// than trusting.
//
// The returned *prompb.WriteRequest is the last point in this package's call
// graph where a raw sample value is reachable at all — see Project, which is
// the only function permitted to read it.
func DecodeWriteRequest(header http.Header, body io.Reader, maxBytes int64) (*prompb.WriteRequest, error) {
	if v := header.Get("X-Prometheus-Remote-Write-Version"); v == "2.0.0" {
		return nil, ErrRemoteWriteV2
	}

	// Read at most maxBytes+1: if the (n+1)th byte exists, the body was over
	// the cap and this returns ErrBodyTooLarge rather than silently decoding
	// a truncated — and therefore corrupt — snappy frame. A plain
	// io.LimitReader(body, maxBytes) would truncate silently instead of
	// refusing, which is exactly the "configured but not enforced" shape
	// this package exists to avoid.
	raw, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("beacon: reading request body: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, ErrBodyTooLarge
	}

	// Check the DECLARED decompressed length before decoding: snappy allocates
	// it before validating the payload, so this is the last point at which an
	// attacker-chosen allocation can still be refused. See ErrDecodedTooLarge.
	declared, err := snappy.DecodedLen(raw)
	if err != nil {
		return nil, fmt.Errorf("beacon: reading declared decompressed length: %w", err)
	}
	if int64(declared) > maxBytes*MaxDecodedRatio {
		return nil, fmt.Errorf("%w: body declares %d decompressed bytes, cap is %d (%d × the %d-byte body cap)",
			ErrDecodedTooLarge, declared, maxBytes*MaxDecodedRatio, MaxDecodedRatio, maxBytes)
	}

	decoded, err := snappy.Decode(nil, raw)
	if err != nil {
		return nil, fmt.Errorf("beacon: snappy decode: %w", err)
	}

	var req prompb.WriteRequest
	if err := req.Unmarshal(decoded); err != nil {
		return nil, fmt.Errorf("beacon: unmarshal remote_write request: %w", err)
	}
	return &req, nil
}
