// Package graph provides a Microsoft Graph API client for group lookups.
package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2/clientcredentials"
)

// Group represents a minimal Graph group.
type Group struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// Client calls the Microsoft Graph API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a Graph client using client credentials (app mode).
// baseURL defaults to https://graph.microsoft.com if empty.
func New(ctx context.Context, tenantID, clientID, clientSecret, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://graph.microsoft.com"
	}
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)
	cc := clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		Scopes:       []string{"https://graph.microsoft.com/.default"},
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: cc.Client(ctx),
	}
}

// TransitiveMemberOf fetches all group IDs for a user using their access token (delegated).
// The passed client should carry the user's Bearer token.
func TransitiveMemberOf(ctx context.Context, baseURL, accessToken string) ([]string, error) {
	if baseURL == "" {
		baseURL = "https://graph.microsoft.com"
	}
	type graphResponse struct {
		Value    []Group `json:"value"`
		NextLink string  `json:"@odata.nextLink"`
	}

	var ids []string
	nextURL := baseURL + "/v1.0/me/transitiveMemberOf/microsoft.graph.group?$select=id"
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("graph transitiveMemberOf: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close() //nolint:errcheck // close error on a body we're discarding due to a non-OK status is not actionable
			return nil, fmt.Errorf("graph transitiveMemberOf: status %d", resp.StatusCode)
		}

		var gr graphResponse
		if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
			resp.Body.Close() //nolint:errcheck // close error on a body we're discarding after a decode failure is not actionable
			return nil, fmt.Errorf("graph transitiveMemberOf: decoding: %w", err)
		}
		resp.Body.Close() //nolint:errcheck // body already fully read via json.Decode
		for _, g := range gr.Value {
			ids = append(ids, g.ID)
		}
		nextURL = gr.NextLink
	}
	return ids, nil
}
