package simulate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// This file is the Shepherd-side client for the shepherd-simulator control
// API (internal/simsvc). The wire types below are a deliberate, independent
// copy of internal/simsvc's StartRequest/Run/runView/APIError JSON shapes —
// not an import of that package — because internal/simsvc is a separate
// container's implementation (it binds capture/OTLP/syslog listeners and
// runs the sandboxed Alloy), and RunWorker only needs to speak its documented
// JSON contract, not link its code.

// ClientStartRequest is the control API's POST /v1/runs body.
type ClientStartRequest struct {
	// Config is rendered Alloy text. Never logged.
	Config          string            `json:"config"`
	DurationSeconds int               `json:"duration_seconds"`
	LogFixtures     []string          `json:"log_fixtures"`
	LogEmitInterval int               `json:"log_emit_interval_ms"`
	ComponentIndex  map[string]string `json:"component_index"`
}

// ClientRunError is the control API's per-run terminal error.
type ClientRunError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ClientSeries mirrors simsvc.Series.
type ClientSeries struct {
	Name             string            `json:"name"`
	Labels           map[string]string `json:"labels"`
	SampleCount      int               `json:"sample_count"`
	FirstValue       float64           `json:"first_value"`
	LastValue        float64           `json:"last_value"`
	FirstTimestampMS int64             `json:"first_timestamp_ms"`
	LastTimestampMS  int64             `json:"last_timestamp_ms"`
}

// ClientLogLine mirrors simsvc.LogLine.
type ClientLogLine struct {
	Labels      map[string]string `json:"labels"`
	Line        string            `json:"line"`
	TimestampMS int64             `json:"timestamp_ms"`
}

// ClientComponentHealth mirrors simsvc.ComponentHealth. Health is NOT
// evidence of delivery — see simsvc.ComponentHealth's own doc comment.
type ClientComponentHealth struct {
	LocalID   string `json:"local_id"`
	NodeID    string `json:"node_id"`
	Health    string `json:"health"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updated_at"`
}

// ClientResults mirrors simsvc.Results (OTLP/Other counts omitted: the run
// API's SimulateRun message has no slot for them yet — captured_series and
// captured_log_lines are the fields §6.4 step 4 renders).
type ClientResults struct {
	Series            []ClientSeries          `json:"series"`
	SeriesTruncated   bool                    `json:"series_truncated"`
	LogLines          []ClientLogLine         `json:"log_lines"`
	LogLinesTruncated bool                    `json:"log_lines_truncated"`
	Components        []ClientComponentHealth `json:"components"`
	StderrTail        []string                `json:"stderr_tail"`
	StderrTruncated   bool                    `json:"stderr_truncated"`
}

// ClientRun mirrors simsvc's runView (Run + remaining_seconds).
type ClientRun struct {
	ID               string          `json:"run_id"`
	State            string          `json:"state"`
	DurationSeconds  int             `json:"duration_seconds"`
	DurationClamped  bool            `json:"duration_clamped"`
	Error            *ClientRunError `json:"error,omitempty"`
	Results          *ClientResults  `json:"results,omitempty"`
	RemainingSeconds int             `json:"remaining_seconds"`
}

// clientTerminalStates are simsvc's terminal RunStates.
var clientTerminalStates = map[string]bool{ //nolint:gochecknoglobals // static lookup table
	"completed": true, "failed": true, "expired": true, "canceled": true,
}

// ClientTerminal reports whether state is one of the simulator's terminal states.
func ClientTerminal(state string) bool { return clientTerminalStates[state] }

// ClientAPIError is the control API's typed error body (simsvc.APIError),
// carrying the HTTP status it arrived with.
type ClientAPIError struct {
	HTTPStatus int
	Code       string `json:"code"`
	Message    string `json:"message"`
	RetryAfter string
}

func (e *ClientAPIError) Error() string {
	return fmt.Sprintf("simulator: %s (%s, HTTP %d)", e.Message, e.Code, e.HTTPStatus)
}

// ErrSimulatorUnreachable wraps a dial failure or a non-API-error non-2xx
// response — anything that isn't a well-formed ClientAPIError body.
var ErrSimulatorUnreachable = errors.New("simulator: unreachable")

// Client talks the shepherd-simulator control API documented in the run-API
// spec: POST /v1/runs, GET /v1/runs/{id}. It carries no session, no org and
// no user — the same posture as the server it calls (internal-only network,
// shared bearer token).
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a Client. baseURL is scheme+host+port with no trailing
// slash (config.SimulatorConfig.ControlURL). httpClient defaults to
// http.DefaultClient's shape with a bounded per-call timeout when nil.
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

// Start submits a run. It returns the initial ClientRun (state typically
// "queued" or "starting") or a *ClientAPIError / ErrSimulatorUnreachable.
func (c *Client) Start(ctx context.Context, req ClientStartRequest) (ClientRun, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ClientRun{}, fmt.Errorf("simulator: encode start request: %w", err)
	}
	var run ClientRun
	err = c.do(ctx, http.MethodPost, "/v1/runs", body, http.StatusAccepted, &run)
	return run, err
}

// Get polls a run's current state.
func (c *Client) Get(ctx context.Context, id string) (ClientRun, error) {
	var run ClientRun
	err := c.do(ctx, http.MethodGet, "/v1/runs/"+id, nil, http.StatusOK, &run)
	return run, err
}

// do performs one control-API call and decodes either the success body (into
// out, on wantStatus) or the APIError body (on any other response).
func (c *Client) do(ctx context.Context, method, path string, body []byte, wantStatus int, out any) error {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("simulator: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSimulatorUnreachable, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body; nothing actionable on close failure

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%w: reading response: %w", ErrSimulatorUnreachable, err)
	}

	if resp.StatusCode != wantStatus {
		apiErr := &ClientAPIError{HTTPStatus: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
		if jsonErr := json.Unmarshal(raw, apiErr); jsonErr != nil || apiErr.Code == "" {
			// Not a well-formed APIError body (e.g. a proxy's plain-text 502) —
			// this is the "non-2xx" case the run-API spec maps to Unavailable.
			return fmt.Errorf("%w: HTTP %d: %s", ErrSimulatorUnreachable, resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return apiErr
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("simulator: decode response: %w", err)
		}
	}
	return nil
}
