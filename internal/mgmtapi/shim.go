package mgmtapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// MarshalOpts renders shepherd.mgmt.v1 proto responses as legacy-compatible
// JSON: snake_case field names (UseProtoNames) matching the hand-written REST
// structs, and every field always present (EmitUnpopulated) matching the
// hand-written REST structs' behavior of always serializing their fields
// regardless of zero value. REST shims marshal every Connect response
// through this so the wire bytes stay byte-compatible with today's JSON.
//
// EmitUnpopulated is a blunt instrument: several of the legacy hand-written
// structs this shim replaced marked specific fields `,omitempty` (e.g.
// Org.reader_group_id, Cluster.org_id, Pipeline.revision/revisions,
// RepoLink.sync_status/last_synced_at, AuditEntry.org_id,
// GetServedConfigResponse.computed_at on a cache miss) — those responses
// must render through writeProtoJSONOmit (below), naming the fields that
// need `,omitempty` semantics restored, not through MarshalOpts directly.
var MarshalOpts = protojson.MarshalOptions{ //nolint:gochecknoglobals // shared, read-only marshal config
	UseProtoNames:   true,
	EmitUnpopulated: true,
}

// jsonObjectEntry is one member of a JSON object, decoded in its original
// wire order with its value kept as unmodified raw bytes.
type jsonObjectEntry struct {
	key string
	raw json.RawMessage
}

// decodeJSONObject decodes a JSON object's members into an ordered slice,
// preserving each member's raw value bytes (no re-marshal, so nested string
// content is never HTML-escaped/reformatted) and the original key order.
func decodeJSONObject(obj []byte) ([]jsonObjectEntry, error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("decodeJSONObject: expected '{', got %v", tok)
	}
	var entries []jsonObjectEntry
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("decodeJSONObject: expected a string key, got %v", keyTok)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		entries = append(entries, jsonObjectEntry{key: key, raw: raw})
	}
	if _, err := dec.Token(); err != nil { // consume closing '}'
		return nil, err
	}
	return entries, nil
}

// encodeJSONObject writes entries back out as a compact JSON object in
// order, copying each raw value's bytes through untouched.
func encodeJSONObject(entries []jsonObjectEntry) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, e := range entries {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(e.key) //nolint:errcheck // e.key is always a valid UTF-8 proto JSON field name
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(e.raw)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// decodeJSONArray decodes a JSON array's elements into an ordered slice of
// raw values, without touching their bytes.
func decodeJSONArray(arr []byte) ([]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(arr))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("decodeJSONArray: expected '[', got %v", tok)
	}
	var out []json.RawMessage
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	if _, err := dec.Token(); err != nil { // consume closing ']'
		return nil, err
	}
	return out, nil
}

// encodeJSONArray writes items back out as a compact JSON array, copying
// each element's bytes through untouched.
func encodeJSONArray(items []json.RawMessage) []byte {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(it)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

// isZeroJSONLiteral reports whether v is the JSON literal MarshalOpts
// (EmitUnpopulated) emits for an unpopulated field of some proto kind: ""
// for a string, 0 for a number, [] for a repeated field, or null for an
// unset singular message (e.g. google.protobuf.Timestamp).
func isZeroJSONLiteral(v json.RawMessage) bool {
	switch string(bytes.TrimSpace(v)) {
	case `""`, `0`, `[]`, `null`:
		return true
	default:
		return false
	}
}

// stripZeroEntries removes any member of obj whose key is in drop and whose
// value is that field's zero-value JSON literal, recursing into an "items"
// array member (if present) to do the same per element — reproducing the
// `,omitempty` behavior of the legacy hand-written struct a given field came
// from, for messages that mix always-emitted fields with omitempty ones (so
// a single blanket EmitUnpopulated on/off toggle can't reproduce the shape).
func stripZeroEntries(obj []byte, drop map[string]bool) ([]byte, error) {
	entries, err := decodeJSONObject(obj)
	if err != nil {
		return nil, err
	}
	kept := entries[:0]
	for _, e := range entries {
		switch {
		case e.key == "items":
			items, err := decodeJSONArray(e.raw)
			if err != nil {
				return nil, err
			}
			for i := range items {
				items[i], err = stripZeroEntries(items[i], drop)
				if err != nil {
					return nil, err
				}
			}
			e.raw = encodeJSONArray(items)
		case drop[e.key] && isZeroJSONLiteral(e.raw):
			continue
		}
		kept = append(kept, e)
	}
	return encodeJSONObject(kept), nil
}

// writeProtoJSONOmit renders msg as legacy-compatible JSON (MarshalOpts),
// then drops the named top-level fields (and, for a list envelope, the same
// fields on each element of "items") when their value is that field's
// zero-value JSON literal — restoring the `,omitempty` behavior of the
// legacy hand-written struct the response replaced for fields that mix with
// always-emitted siblings in the same message. See MarshalOpts' doc comment
// for the concrete fields this exists for.
func writeProtoJSONOmit(w http.ResponseWriter, status int, msg proto.Message, keys ...string) {
	b, err := MarshalOpts.Marshal(msg)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to marshal response")
		return
	}
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	b, err = stripZeroEntries(b, drop)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to render response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b) //nolint:errcheck // response status/headers already committed
}

// ConnectCodeStatus maps a connect.Code to the HTTP status the legacy REST
// API has always returned for the equivalent failure
// (docs/api-contract-design.md, "Contract modeling rules" > Errors).
func ConnectCodeStatus(code connect.Code) int {
	switch code {
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeAlreadyExists:
		return http.StatusConflict
	case connect.CodeFailedPrecondition:
		return http.StatusUnprocessableEntity
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// WriteConnectError renders err — expected to be (or wrap) a *connect.Error
// returned by a shepherd.mgmt.v1 service implementation — as the legacy
// {"error":{"code","message"}} envelope, with the HTTP status the REST shims
// have always used for the equivalent failure. Per-service REST shim
// handlers call this instead of respondError when relaying an error from the
// Connect service call they delegate to.
func WriteConnectError(w http.ResponseWriter, err error) {
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		respondError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	respondError(w, ConnectCodeStatus(cerr.Code()), cerr.Code().String(), cerr.Message())
}
