// Package ado provides an Azure DevOps Git client that lists and downloads
// files from a repository using a service principal token obtained via
// client-credentials OIDC flow.
package ado

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2/clientcredentials"
)

// GitItem represents a file or folder entry from the ADO Items API.
type GitItem struct {
	Path      string `json:"path"`
	IsFolder  bool   `json:"isFolder"`
	CommitID  string `json:"commitId"`
	ContentID string `json:"contentId"`
}

// Client accesses Azure DevOps Git repositories via a service principal.
type Client struct {
	baseURL    string // e.g. https://dev.azure.com or mock URL
	orgURL     string // full org URL, e.g. https://dev.azure.com/myorg
	httpClient *http.Client
}

// New creates an ADO Client using client credentials (service principal).
// tenantID, clientID, clientSecret identify the SP.
// orgURL is the ADO org URL (e.g. https://dev.azure.com/myorg).
// baseURL overrides the ADO base for testing; pass "" to use orgURL.
func New(ctx context.Context, tenantID, clientID, clientSecret, orgURL, baseURL string) *Client {
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)
	cc := clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		Scopes:       []string{"499b84ac-1321-427f-aa17-267ca6975798/.default"}, // ADO scope
	}

	effective := baseURL
	if effective == "" {
		effective = orgURL
	}

	return &Client{
		baseURL:    effective,
		orgURL:     orgURL,
		httpClient: cc.Client(ctx),
	}
}

// NewWithHTTPClient creates an ADO Client with a pre-configured HTTP client (for tests).
func NewWithHTTPClient(orgURL, baseURL string, hc *http.Client) *Client {
	if baseURL == "" {
		baseURL = orgURL
	}
	return &Client{baseURL: baseURL, orgURL: orgURL, httpClient: hc}
}

// ListFiles returns all .alloy files at or below path in the given branch.
func (c *Client) ListFiles(ctx context.Context, project, repo, branch, path string) ([]GitItem, error) {
	endpoint := fmt.Sprintf("%s/%s/_apis/git/repositories/%s/items?version=%s&versionType=branch&recursionLevel=Full&scopePath=%s&api-version=7.1",
		c.baseURL, url.PathEscape(project), url.PathEscape(repo),
		url.QueryEscape(branch), url.QueryEscape(path))

	var result struct {
		Value []GitItem `json:"value"`
	}
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, err
	}

	var alloy []GitItem
	for _, item := range result.Value {
		if !item.IsFolder && len(item.Path) > 6 && item.Path[len(item.Path)-6:] == ".alloy" {
			alloy = append(alloy, item)
		}
	}
	return alloy, nil
}

// DownloadFile fetches the raw contents of a file.
func (c *Client) DownloadFile(ctx context.Context, project, repo, branch, filePath string) (string, error) {
	endpoint := fmt.Sprintf("%s/%s/_apis/git/repositories/%s/items?version=%s&versionType=branch&path=%s&api-version=7.1&$format=text",
		c.baseURL, url.PathEscape(project), url.PathEscape(repo),
		url.QueryEscape(branch), url.QueryEscape(filePath))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ado download file: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // close error on a body we already fully read/discarded is not actionable

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ado download file: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ado download file: reading body: %w", err)
	}
	return string(body), nil
}

// GetLatestCommit returns the latest commit SHA for a branch.
func (c *Client) GetLatestCommit(ctx context.Context, project, repo, branch string) (string, error) {
	endpoint := fmt.Sprintf("%s/%s/_apis/git/repositories/%s/commits?searchCriteria.itemVersion.version=%s&searchCriteria.itemVersion.versionType=branch&$top=1&api-version=7.1",
		c.baseURL, url.PathEscape(project), url.PathEscape(repo), url.QueryEscape(branch))

	var result struct {
		Value []struct {
			CommitID string `json:"commitId"`
		} `json:"value"`
	}
	if err := c.get(ctx, endpoint, &result); err != nil {
		return "", err
	}
	if len(result.Value) == 0 {
		return "", fmt.Errorf("ado: no commits found on branch %q", branch)
	}
	return result.Value[0].CommitID, nil
}

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ado GET: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // close error on a body we already fully read/discarded is not actionable

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) //nolint:errcheck // error details are best-effort
		return fmt.Errorf("ado GET %s: status %d: %s", endpoint, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
