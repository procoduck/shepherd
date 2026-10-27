// Command tenantsink is the recording sink of the strict tenant e2e suite
// (e2e/strict_tenant_test.go): a stand-in for Loki and Mimir that accepts
// every push and records, per request, the tenant it was sent under (the
// X-Scope-OrgID header) and the label sets it carried. Standard library only,
// so its image builds without the main module: the Loki push and Prometheus
// remote-write bodies (snappy-compressed protobuf) are decoded by hand, just
// far enough to read the labels.
//
//	POST /loki/api/v1/push  Loki push (PushRequest), one label set per stream
//	POST /api/v1/push       remote write (WriteRequest), one per series
//	POST anything else      recorded with no label sets
//	GET  /records           every record so far, as JSON
//	GET  /healthz           200
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Record is one request the sink received.
type Record struct {
	Path string `json:"path"`
	// Tenant is the X-Scope-OrgID header ("" when there was none).
	Tenant string `json:"tenant"`
	// Labels are the label sets of the streams (or series) it carried.
	Labels []map[string]string `json:"labels"`
	// Error is why the body could not be decoded, if it could not.
	Error string `json:"error,omitempty"`
}

type sink struct {
	mu      sync.Mutex
	records []Record
}

func (s *sink) push(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rec := Record{Path: r.URL.Path, Tenant: r.Header.Get("X-Scope-OrgID")}
	var decode func([]byte) ([]map[string]string, error)
	switch r.URL.Path {
	case "/loki/api/v1/push":
		decode = lokiLabels
	case "/api/v1/push":
		decode = remoteWriteLabels
	}
	if decode != nil {
		raw, err := snappyDecode(body)
		if err == nil {
			rec.Labels, err = decode(raw)
		}
		if err != nil {
			rec.Error = err.Error()
		}
	}
	s.mu.Lock()
	s.records = append(s.records, rec)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *sink) list(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := append([]Record{}, s.records...)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out) //nolint:errcheck // best effort
}

func main() {
	listen := flag.String("listen", envOr("LISTEN", ":3100"), "listen address")
	health := flag.Bool("health-check", false, "probe the running sink's /healthz and exit")
	flag.Parse()
	if *health {
		c := http.Client{Timeout: 2 * time.Second}
		resp, err := c.Get("http://127.0.0.1" + *listen + "/healthz")
		if err != nil {
			os.Exit(1)
		}
		_ = resp.Body.Close() //nolint:errcheck // exiting either way
		if resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}
	s := &sink{}
	mux := http.NewServeMux()
	mux.HandleFunc("/records", s.list)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/", s.push)
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("tenantsink listening on %s", *listen)
	log.Fatal(srv.ListenAndServe())
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// lokiLabels reads a Loki PushRequest: streams = 1 (StreamAdapter), each
// with labels = 1, the Prometheus text form of its label set.
func lokiLabels(b []byte) ([]map[string]string, error) {
	var out []map[string]string
	err := fields(b, func(num int, v []byte) error {
		if num != 1 {
			return nil
		}
		return fields(v, func(n int, lv []byte) error {
			if n != 1 {
				return nil
			}
			ls, err := parseLabels(string(lv))
			if err != nil {
				return err
			}
			out = append(out, ls)
			return nil
		})
	})
	return out, err
}

// remoteWriteLabels reads a Prometheus WriteRequest: timeseries = 1, each
// with labels = 1 (Label: name = 1, value = 2).
func remoteWriteLabels(b []byte) ([]map[string]string, error) {
	var out []map[string]string
	err := fields(b, func(num int, v []byte) error {
		if num != 1 {
			return nil
		}
		ls := map[string]string{}
		err := fields(v, func(n int, lv []byte) error {
			if n != 1 {
				return nil
			}
			var name, value string
			if err := fields(lv, func(k int, x []byte) error {
				switch k {
				case 1:
					name = string(x)
				case 2:
					value = string(x)
				}
				return nil
			}); err != nil {
				return err
			}
			ls[name] = value
			return nil
		})
		out = append(out, ls)
		return err
	})
	return out, err
}

// fields calls fn for every length-delimited field of a protobuf message
// (wire type 2) and skips the others.
func fields(b []byte, fn func(num int, v []byte) error) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return errors.New("bad field key")
		}
		b = b[n:]
		num, typ := int(key>>3), key&7
		switch typ {
		case 0:
			_, n := binary.Uvarint(b)
			if n <= 0 {
				return errors.New("bad varint")
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return errors.New("short fixed64")
			}
			b = b[8:]
		case 5:
			if len(b) < 4 {
				return errors.New("short fixed32")
			}
			b = b[4:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return errors.New("bad length")
			}
			if err := fn(num, b[n:n+int(l)]); err != nil {
				return err
			}
			b = b[n+int(l):]
		default:
			return errors.New("unsupported wire type " + strconv.Itoa(int(typ)))
		}
	}
	return nil
}

// parseLabels reads `{a="b", c="d"}`.
func parseLabels(s string) (map[string]string, error) {
	out := map[string]string{}
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return nil, errors.New("labels not in braces: " + s)
	}
	s = s[1 : len(s)-1]
	for {
		s = strings.TrimLeft(s, ", ")
		if s == "" {
			return out, nil
		}
		eq := strings.Index(s, "=")
		if eq < 0 {
			return nil, errors.New("label without =")
		}
		name := strings.TrimSpace(s[:eq])
		s = strings.TrimSpace(s[eq+1:])
		if !strings.HasPrefix(s, `"`) {
			return nil, errors.New("label value not quoted")
		}
		end := 1
		for end < len(s) && s[end] != '"' {
			if s[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(s) {
			return nil, errors.New("unterminated label value")
		}
		v, err := strconv.Unquote(s[:end+1])
		if err != nil {
			return nil, err
		}
		out[name] = v
		s = s[end+1:]
	}
}

// snappyDecode decodes a snappy block (the framing Loki and remote write
// use): a varint length, then literals and back-references.
func snappyDecode(src []byte) ([]byte, error) {
	dLen, n := binary.Uvarint(src)
	if n <= 0 || dLen > 64<<20 {
		return nil, errors.New("snappy: bad length")
	}
	src = src[n:]
	dst := make([]byte, 0, dLen)
	for len(src) > 0 {
		tag := src[0]
		switch tag & 3 {
		case 0: // literal
			l := int(tag >> 2)
			src = src[1:]
			if l >= 60 {
				extra := l - 59
				if len(src) < extra {
					return nil, errors.New("snappy: short literal length")
				}
				l = 0
				for i := 0; i < extra; i++ {
					l |= int(src[i]) << (8 * i)
				}
				src = src[extra:]
			}
			l++
			if len(src) < l {
				return nil, errors.New("snappy: short literal")
			}
			dst = append(dst, src[:l]...)
			src = src[l:]
			continue
		case 1:
			if len(src) < 2 {
				return nil, errors.New("snappy: short copy1")
			}
			l := 4 + int(tag>>2)&7
			off := int(tag&0xe0)<<3 | int(src[1])
			src = src[2:]
			if err := copyBack(&dst, off, l); err != nil {
				return nil, err
			}
		case 2:
			if len(src) < 3 {
				return nil, errors.New("snappy: short copy2")
			}
			l := 1 + int(tag>>2)
			off := int(binary.LittleEndian.Uint16(src[1:3]))
			src = src[3:]
			if err := copyBack(&dst, off, l); err != nil {
				return nil, err
			}
		case 3:
			if len(src) < 5 {
				return nil, errors.New("snappy: short copy4")
			}
			l := 1 + int(tag>>2)
			off := int(binary.LittleEndian.Uint32(src[1:5]))
			src = src[5:]
			if err := copyBack(&dst, off, l); err != nil {
				return nil, err
			}
		}
	}
	if uint64(len(dst)) != dLen {
		return nil, errors.New("snappy: length mismatch")
	}
	return dst, nil
}

func copyBack(dst *[]byte, off, l int) error {
	if off <= 0 || off > len(*dst) {
		return errors.New("snappy: bad offset")
	}
	start := len(*dst) - off
	for i := 0; i < l; i++ {
		*dst = append(*dst, (*dst)[start+i])
	}
	return nil
}
