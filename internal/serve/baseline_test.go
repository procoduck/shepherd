package serve_test

import (
	"testing"

	"shepherd/internal/beacon"
	"shepherd/internal/config"
	"shepherd/internal/serve"
)

// TestBeaconBaseline pins the one derivation every serve path takes its D6
// baseline from (#369): disabled, or no base_url, means no baseline at all.
func TestBeaconBaseline(t *testing.T) {
	const base = "https://shepherd.example.test/"
	tests := []struct {
		name      string
		cfg       *config.Config
		wantURL   string
		wantOAuth bool
	}{
		{name: "nil config", cfg: nil},
		{name: "no base_url", cfg: &config.Config{}},
		{
			name: "beacon disabled with base_url set",
			cfg:  &config.Config{Server: config.ServerConfig{BaseURL: base, BeaconDisabled: true}},
		},
		{
			name:    "enabled, agent-token basic_auth",
			cfg:     &config.Config{Server: config.ServerConfig{BaseURL: base}},
			wantURL: "https://shepherd.example.test" + beacon.WritePath,
		},
		{
			name: "enabled, oauth2",
			cfg: &config.Config{
				Server: config.ServerConfig{BaseURL: base},
				OIDC:   config.OIDCConfig{BeaconAuth: "oauth2", AgentTokenURL: "https://idp.example.test/token"},
			},
			wantURL:   "https://shepherd.example.test" + beacon.WritePath,
			wantOAuth: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serve.BeaconBaseline(tt.cfg)
			if got.RemoteWriteURL != tt.wantURL {
				t.Fatalf("RemoteWriteURL = %q, want %q", got.RemoteWriteURL, tt.wantURL)
			}
			if (got.OAuth2 != nil) != tt.wantOAuth {
				t.Fatalf("OAuth2 set = %v, want %v", got.OAuth2 != nil, tt.wantOAuth)
			}
			content, err := beacon.AppendBaseline("x", got)
			if err != nil {
				t.Fatalf("AppendBaseline: %v", err)
			}
			if tt.wantURL == "" && content != "x" {
				t.Fatalf("a disabled baseline must leave content unchanged, got %q", content)
			}
		})
	}
}
