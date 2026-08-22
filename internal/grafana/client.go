package grafana

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds every Client network call when a caller does not
// pick its own. It is applied INSIDE Client.do via context.WithTimeout,
// which always yields the earlier of the caller's own deadline (if any)
// and this one — so a caller that passes context.Background() (no
// deadline at all, the case a hung Grafana is most dangerous for) still
// cannot hang past this bound. "Every network call takes a context and a
// timeout" (D7's build brief) is enforced by the client, never merely
// requested of the caller.
const DefaultTimeout = 10 * time.Second

// Client talks to a single Grafana instance's HTTP API, scoped to exactly
// the calls D7 needs: query a datasource's data (QueryDatasource) and list
// datasources without their secrets (ListDatasources). It deliberately has
// no method for creating, updating or deleting a dashboard, alert rule, or
// datasource — plan §5's "must not grow into dashboard or alert-rule
// management" is enforced by this type simply not exposing the calls that
// would let it.
type Client struct {
	baseURL *url.URL
	token   string
	hc      *http.Client
	timeout time.Duration
}

// NewClient constructs a Client for baseURL, authenticating with token as a
// Grafana service-account Bearer token. Both are required: a Client with no
// token would either send unauthenticated requests (silently degrading
// security) or panic on first use, and this package's house rule is that a
// silent failure is a bug — see internal/gateway/apply.go's ErrPending
// wrapping for the same discipline applied to a different silent-failure
// class. Callers that do not have a baseURL/token (Grafana not configured
// for this org) should never reach NewClient at all — see VerifyForOrg,
// which checks ConnectionStore first and short-circuits to OutcomeUnknown
// without constructing a Client.
func NewClient(baseURL, token string, timeout time.Duration) (*Client, error) {
	if token == "" {
		return nil, errors.New("grafana: NewClient: token is required")
	}
	u, err := validateBaseURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("grafana: NewClient: %w", err)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		baseURL: u,
		token:   token,
		hc:      &http.Client{},
		timeout: timeout,
	}, nil
}

// validateBaseURL is the one place a Grafana base URL is checked for
// shape, shared by NewClient and ConnectionStore.Set (connection.go) so
// the two enforcement points — Go-level here, and the CHECK constraint on
// grafana_connections.base_url (0011_grafana_connections.up.sql) — agree
// on exactly the same rule rather than each restating it slightly
// differently.
func validateBaseURL(baseURL string) (*url.URL, error) {
	if baseURL == "" {
		return nil, errors.New("baseURL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing baseURL %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("baseURL %q must be http:// or https://, found scheme %q", baseURL, u.Scheme)
	}
	return u, nil
}

// String redacts the token. fmt's default struct formatting (%v, %+v) DOES
// print unexported fields' actual values — relying on token being
// unexported is not sufficient on its own to keep it out of a log line a
// future change adds — so Client defines its own Stringer rather than
// leaving that to chance. TestClientStringRedactsToken pins this.
//
// VALUE receiver, deliberately. With a pointer receiver, fmt selects the
// method only for a *Client; a dereferenced value — fmt.Sprintf("%+v",
// *client), which is easy to write by accident — would fall back to default
// struct formatting and print the token. A value receiver covers both forms.
// Review caught this as the last place the redaction had a seam.
func (c Client) String() string {
	host := ""
	if c.baseURL != nil {
		host = c.baseURL.Host
	}
	return fmt.Sprintf("grafana.Client{host: %q, token: \"[REDACTED]\"}", host)
}

// GoString mirrors String so %#v (go-syntax formatting, fmt's other
// full-value verb) redacts too.
func (c Client) GoString() string { return c.String() }

// maxErrorBodyBytes bounds how much of a non-2xx response body do() reads
// into an error message — enough to be useful, small enough that a
// misbehaving Grafana returning a huge body cannot make a failed request
// consume unbounded memory on top of already having failed.
const maxErrorBodyBytes = 4096

// do issues method/path against c.baseURL, encoding body as the JSON
// request payload (nil for none) and decoding a JSON response into out
// (nil to discard the body). Every call is bounded by c.timeout regardless
// of ctx's own deadline — see DefaultTimeout's doc comment.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("grafana: encoding request body for %s %s: %w", method, path, err)
		}
		reqBody = bytes.NewReader(encoded)
	}

	target := *c.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path

	req, err := http.NewRequestWithContext(ctx, method, target.String(), reqBody)
	if err != nil {
		return fmt.Errorf("grafana: building request for %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		// Deliberately not including err's own text verbatim beyond %w's
		// wrapping: net/http error strings can embed the request URL
		// (never the Authorization header, which Go's http client does
		// not echo into errors) but never the token itself, so wrapping
		// here is safe. Verify (verify.go) is what turns this into
		// OutcomeUnknown rather than a hard failure.
		return fmt.Errorf("grafana: %s %s: request failed: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only response body, close error is not actionable

	if resp.StatusCode/100 != 2 {
		limited := io.LimitReader(resp.Body, maxErrorBodyBytes)
		msg, _ := io.ReadAll(limited) //nolint:errcheck // best-effort: report the status code even if the body can't be read
		return fmt.Errorf("grafana: %s %s: unexpected status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("grafana: %s %s: decoding response: %w", method, path, err)
	}
	return nil
}

// dsQueryRequest is the POST /api/ds/query request body, verified against
// Grafana's own published HTTP API reference (data source query endpoint,
// grafana.com/docs/grafana/latest/developer-resources/api-reference/http-api,
// checked 2026-08-22 — this repo has no vendored Grafana artifact to pin
// against, so upstream docs are the source of truth D7's brief requires
// over memory) rather than recalled from training data.
type dsQueryRequest struct {
	From    string            `json:"from,omitempty"`
	To      string            `json:"to,omitempty"`
	Queries []dsQueryRequestQ `json:"queries"`
}

type dsQueryRequestQ struct {
	RefID      string          `json:"refId"`
	Datasource dsDatasourceRef `json:"datasource"`
	Extra      map[string]any  `json:"-"`
}

type dsDatasourceRef struct {
	UID string `json:"uid"`
}

// MarshalJSON flattens Extra's fields alongside refId/datasource, since
// Grafana expects one query object per queries[] entry with the
// datasource-specific fields (e.g. Prometheus's "expr", Loki's "expr") as
// siblings of refId/datasource, not nested under a wrapper key.
func (q dsQueryRequestQ) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(q.Extra)+2)
	for k, v := range q.Extra {
		m[k] = v
	}
	m["refId"] = q.RefID
	m["datasource"] = q.Datasource
	return json.Marshal(m)
}

// dsQueryResponse is POST /api/ds/query's response body: a map from refId
// to that query's result, each carrying an HTTP-style status, an optional
// error string, and zero or more data frames — the same three fields
// verified against Grafana's own API reference and issue tracker
// (grafana/grafana#58637, grafana/grafana#40444) on 2026-08-22.
type dsQueryResponse struct {
	Results map[string]dsQueryResult `json:"results"`
}

type dsQueryResult struct {
	Status int            `json:"status"`
	Error  string         `json:"error"`
	Frames []dsQueryFrame `json:"frames"`
}

type dsQueryFrame struct {
	Data struct {
		// Values is one []T per field (e.g. [timestamps][measurements]);
		// json.RawMessage per element defers decoding a value's actual
		// type, since this package only needs to know whether ANY value
		// was returned, never the value itself (this package must never
		// become a place raw sample data flows through — the same
		// structural discipline internal/beacon's ComponentObservation
		// applies, one level removed: verification asks "how many
		// points", never "what value").
		Values []json.RawMessage `json:"values"`
	} `json:"data"`
}

// QueryDatasourceRequest is one POST /api/ds/query call: a single query
// against a single datasource. D7 is bounded to verification, so this
// package only ever needs one query per call — a caller wanting to verify
// multiple datasources issues multiple calls, each independently timed out
// and independently degradable to OutcomeUnknown.
type QueryDatasourceRequest struct {
	// DatasourceUID is the Grafana datasource's uid (GET /api/datasources
	// returns this per-datasource, see Datasource.UID).
	DatasourceUID string
	// From and To are Grafana's own relative-or-absolute time strings
	// (e.g. "now-5m", "now", or epoch milliseconds as a string).
	From, To string
	// Query carries the datasource-specific query fields, e.g.
	// {"expr": "up{job=\"myapp\"}"} for Prometheus or
	// {"expr": "{app=\"myapp\"}"} for Loki. Shepherd does not validate
	// this against any particular datasource's query language — that is
	// exactly the kind of per-datasource surface plan §5 keeps this
	// package out of.
	Query map[string]any
}

// queryRefID is fixed rather than caller-supplied: QueryDatasourceRequest
// carries exactly one query, so there is nothing for a second refId to
// disambiguate, and fixing it removes one field a caller could get wrong.
const queryRefID = "A"

// QueryDatasource issues req against ds/query and reports whether ANY data
// point came back. It never returns a value it read from the response —
// only whether points existed — for the same reason dsQueryFrame's Values
// field stays a json.RawMessage: this package answers "did data arrive",
// never "what is the data".
func (c *Client) QueryDatasource(ctx context.Context, req QueryDatasourceRequest) (hasData bool, err error) {
	if req.DatasourceUID == "" {
		return false, errors.New("grafana: QueryDatasource: DatasourceUID is required")
	}
	body := dsQueryRequest{
		From: req.From,
		To:   req.To,
		Queries: []dsQueryRequestQ{{
			RefID:      queryRefID,
			Datasource: dsDatasourceRef{UID: req.DatasourceUID},
			Extra:      req.Query,
		}},
	}
	var resp dsQueryResponse
	if err := c.do(ctx, http.MethodPost, "/api/ds/query", body, &resp); err != nil {
		return false, err
	}
	result, ok := resp.Results[queryRefID]
	if !ok {
		return false, fmt.Errorf("grafana: QueryDatasource: response had no result for refId %q", queryRefID)
	}
	if result.Error != "" {
		return false, fmt.Errorf("grafana: QueryDatasource: datasource %s: %s", req.DatasourceUID, result.Error)
	}
	for _, frame := range result.Frames {
		for _, values := range frame.Data.Values {
			if values != nil {
				// values is itself a JSON array (one field's worth of
				// points); decode only its length, never its contents.
				var points []json.RawMessage
				if err := json.Unmarshal(values, &points); err != nil {
					continue // malformed field, not this package's problem to interpret further
				}
				if len(points) > 0 {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// Datasource is the "destination import" half of D7: exactly the fields a
// caller needs to reference a datasource in a later QueryDatasource call
// or an ExploreURL — never a credential. Grafana's own GET /api/datasources
// response cannot carry one either (secrets live under secureJsonFields as
// booleans, verified against the upstream API reference on 2026-08-22),
// but this type is deliberately narrow anyway: it has no field a future
// change could point at a secret even if Grafana's response grew one.
type Datasource struct {
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	URL       string `json:"url"`
	IsDefault bool   `json:"isDefault"`
}

// datasourceJSON is GET /api/datasources' response shape, narrowed at
// decode time to exactly Datasource's fields — see Datasource's doc
// comment for why that narrowing is deliberate. Any other field Grafana's
// response carries (password, user, jsonData, secureJsonFields, ...) is
// simply never unmarshaled into anything.
type datasourceJSON struct {
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	URL       string `json:"url"`
	IsDefault bool   `json:"isDefault"`
}

// ListDatasources returns every datasource Grafana knows about, endpoints
// and types only. Bounded per D7's brief ("destination import ... if
// trivial"): this is a straight GET with no pagination, no filtering, and
// no secret ever leaves Grafana for it to accidentally carry.
func (c *Client) ListDatasources(ctx context.Context) ([]Datasource, error) {
	var raw []datasourceJSON
	if err := c.do(ctx, http.MethodGet, "/api/datasources", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Datasource, len(raw))
	for i, d := range raw {
		out[i] = Datasource(d)
	}
	return out, nil
}
