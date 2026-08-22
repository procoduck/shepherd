package grafana

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// explorePaneID is the JSON object key panes uses for the single pane this
// package ever builds. Grafana's own schema (grafana.com/docs/grafana/latest/
// visualizations/explore/get-started-with-explore/, checked 2026-08-22)
// documents the pane id as an opaque map key with no meaning of its own —
// Grafana's UI normally generates a short random one, but any non-empty
// string round-trips identically, so a fixed literal here is exactly as
// valid and considerably more debuggable in a captured URL.
const explorePaneID = "shepherd-verify"

// exploreSchemaVersion is the schemaVersion query parameter Grafana's
// Explore share-URL scheme requires (same source as explorePaneID).
// Pinned to 1: this is the only version Grafana's docs describe, and this
// package has no way to detect the installation's actual Grafana version
// (no Grafana version pin exists anywhere in this repo — see
// deploy/versions.env, which has none — so there is nothing to condition
// on). If a future Grafana major changes this scheme, ExploreURL's tests
// will need a fresh check against upstream docs, per plan §8 rule 6.
const exploreSchemaVersion = "1"

// explorePane matches the "panes" JSON object's per-pane schema
// (datasource, queries[], range{from,to}) documented at the URL above.
type explorePane struct {
	Datasource string             `json:"datasource"`
	Queries    []explorePaneQuery `json:"queries"`
	Range      explorePaneRange   `json:"range"`
}

type explorePaneQuery struct {
	RefID      string          `json:"refId"`
	Datasource dsDatasourceRef `json:"datasource"`
	Extra      map[string]any  `json:"-"`
}

func (q explorePaneQuery) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(q.Extra)+2)
	for k, v := range q.Extra {
		m[k] = v
	}
	m["refId"] = q.RefID
	m["datasource"] = q.Datasource
	return json.Marshal(m)
}

type explorePaneRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ExploreURL builds a deep link into Grafana Explore, pre-filled with
// datasourceUID and query, over the [from, to] time range. Unlike every
// other function in this package, ExploreURL needs no token and makes no
// network call — it is a browser-navigation URL a human clicks, not an API
// request Shepherd issues — so it works identically whether or not this
// org has a configured ConnectionStore entry at all (D7: "deep links into
// Explore which need no token at all, and should exist regardless").
//
// baseURL is the Grafana instance's own base URL (the same value
// ConnectionStore stores per org, or one a caller supplies directly for an
// org with no stored connection — ExploreURL takes it as a plain
// parameter for exactly that reason, rather than requiring a
// *ConnectionStore lookup this function has no need for).
func ExploreURL(baseURL, datasourceUID string, query map[string]any, from, to string) (string, error) {
	u, err := validateBaseURL(baseURL)
	if err != nil {
		return "", fmt.Errorf("grafana: ExploreURL: %w", err)
	}
	if datasourceUID == "" {
		return "", errors.New("grafana: ExploreURL: datasourceUID is required")
	}
	if from == "" || to == "" {
		return "", errors.New("grafana: ExploreURL: from and to are both required")
	}

	panes := map[string]explorePane{
		explorePaneID: {
			Datasource: datasourceUID,
			Queries: []explorePaneQuery{{
				RefID:      queryRefID,
				Datasource: dsDatasourceRef{UID: datasourceUID},
				Extra:      query,
			}},
			Range: explorePaneRange{From: from, To: to},
		},
	}
	panesJSON, err := json.Marshal(panes)
	if err != nil {
		return "", fmt.Errorf("grafana: ExploreURL: encoding panes: %w", err)
	}

	// validateBaseURL parsed into a *url.URL already; copy it so mutating
	// Path/RawQuery below never touches a URL a concurrent caller might
	// also hold (baseURL is a plain string parameter, but this mirrors
	// Client.do's identical defensive copy for the same reason).
	target := *u
	target.Path = strings.TrimRight(target.Path, "/") + "/explore"
	q := url.Values{}
	q.Set("schemaVersion", exploreSchemaVersion)
	q.Set("panes", string(panesJSON))
	target.RawQuery = q.Encode()
	return target.String(), nil
}
