// Package graph provides a Microsoft Graph API client for group lookups.
package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

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
	cacheMu    sync.Mutex
	cache      map[string]cacheEntry
}

type cacheEntry struct {
	groups []Group
	expiry time.Time
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
		cache:      make(map[string]cacheEntry),
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
			_ = resp.Body.Close()
			return nil, fmt.Errorf("graph transitiveMemberOf: status %d", resp.StatusCode)
		}

		var gr graphResponse
		if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("graph transitiveMemberOf: decoding: %w", err)
		}
		_ = resp.Body.Close()
		for _, g := range gr.Value {
			ids = append(ids, g.ID)
		}
		nextURL = gr.NextLink
	}
	return ids, nil
}

// SearchGroups searches groups by display name prefix (app-mode, cached 5 min).
func (c *Client) SearchGroups(ctx context.Context, q string) ([]Group, error) {
	c.cacheMu.Lock()
	if e, ok := c.cache[q]; ok && time.Now().Before(e.expiry) {
		c.cacheMu.Unlock()
		return e.groups, nil
	}
	c.cacheMu.Unlock()

	endpoint := c.baseURL + "/v1.0/groups?$select=id,displayName&$top=20&$filter=" +
		url.QueryEscape(fmt.Sprintf("startswith(displayName,'%s')", q))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graph search groups: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graph search groups: status %d", resp.StatusCode)
	}

	var gr struct {
		Value []Group `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, fmt.Errorf("graph search groups: decoding: %w", err)
	}

	c.cacheMu.Lock()
	c.cache[q] = cacheEntry{groups: gr.Value, expiry: time.Now().Add(5 * time.Minute)}
	c.cacheMu.Unlock()

	return gr.Value, nil
}
