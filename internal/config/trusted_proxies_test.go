package config

import (
	"encoding/base64"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{name: "empty", in: nil, want: []string{}},
		{name: "blank entries skipped", in: []string{"", "  "}, want: []string{}},
		{name: "ipv4 cidr", in: []string{"10.244.0.0/16"}, want: []string{"10.244.0.0/16"}},
		{name: "host bits masked", in: []string{"10.244.3.7/16"}, want: []string{"10.244.0.0/16"}},
		{name: "ipv6 cidr", in: []string{"fd00:10:244::/56"}, want: []string{"fd00:10:244::/56"}},
		{name: "ipv4 /8 is the widest accepted", in: []string{"10.0.0.0/8"}, want: []string{"10.0.0.0/8"}},
		{name: "ipv6 /16 is the widest accepted", in: []string{"fd00::/16"}, want: []string{"fd00::/16"}},
		{name: "ipv4 wider than /8 rejected", in: []string{"10.0.0.0/7"}, wantErr: "too broad (shorter than /8)"},
		{name: "ipv6 wider than /16 rejected", in: []string{"fd00::/8"}, wantErr: "too broad (shorter than /16)"},
		{name: "v4-mapped wider than /8 rejected", in: []string{"::ffff:0.0.0.0/100"}, wantErr: "too broad (shorter than /8)"},
		{name: "bare ipv4 is a /32", in: []string{"192.0.2.10"}, want: []string{"192.0.2.10/32"}},
		{name: "bare ipv6 is a /128", in: []string{"2001:db8::1"}, want: []string{"2001:db8::1/128"}},
		{name: "v4-mapped folds to v4", in: []string{"::ffff:10.0.0.0/104"}, want: []string{"10.0.0.0/8"}},
		{name: "whitespace trimmed", in: []string{" 10.0.0.0/8 "}, want: []string{"10.0.0.0/8"}},
		{name: "not an address", in: []string{"ingress-nginx"}, wantErr: "not a CIDR or IP address"},
		{name: "bad prefix length", in: []string{"10.0.0.0/33"}, wantErr: "not a CIDR"},
		{name: "ipv4 /0 rejected", in: []string{"0.0.0.0/0"}, wantErr: "trusts every address"},
		{name: "ipv6 /0 rejected", in: []string{"::/0"}, wantErr: "trusts every address"},
		{name: "v4-mapped /96 rejected", in: []string{"::ffff:0.0.0.0/96"}, wantErr: "trusts every address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTrustedProxies(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "server.trusted_proxies") {
					t.Fatalf("ParseTrustedProxies(%q) err = %v, want one naming server.trusted_proxies containing %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			gotS := make([]string, 0, len(got))
			for _, p := range got {
				gotS = append(gotS, p.String())
			}
			if !slices.Equal(gotS, tc.want) {
				t.Errorf("ParseTrustedProxies(%q) = %v, want %v", tc.in, gotS, tc.want)
			}
		})
	}
}

// TestTrustedProxiesLoad: the key reaches the config from both the chart's
// shepherd.yaml (a YAML list) and the env var (comma-separated), defaults to
// empty, and a bad entry fails startup instead of being silently dropped.
func TestTrustedProxiesLoad(t *testing.T) {
	setRequired := func(t *testing.T) {
		t.Helper()
		t.Setenv("SHEPHERD_DATABASE_URL", "postgres://example")
		t.Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	}

	t.Run("default empty", func(t *testing.T) {
		setRequired(t)
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Server.TrustedProxies) != 0 {
			t.Fatalf("trusted_proxies = %v, want empty by default", cfg.Server.TrustedProxies)
		}
	})

	t.Run("yaml list", func(t *testing.T) {
		setRequired(t)
		file := filepath.Join(t.TempDir(), "shepherd.yaml")
		body := "server:\n  trusted_proxies:\n    - 10.244.0.0/16\n    - fd00:10:244::/56\n"
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(file)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.Server.TrustedProxies, []string{"10.244.0.0/16", "fd00:10:244::/56"}) {
			t.Fatalf("trusted_proxies = %v", cfg.Server.TrustedProxies)
		}
	})

	t.Run("env comma-separated", func(t *testing.T) {
		setRequired(t)
		t.Setenv("SHEPHERD_SERVER_TRUSTED_PROXIES", "10.244.0.0/16,192.0.2.10")
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseTrustedProxies(cfg.Server.TrustedProxies)
		if err != nil {
			t.Fatal(err)
		}
		want := []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16"), netip.MustParsePrefix("192.0.2.10/32")}
		if !slices.Equal(got, want) {
			t.Fatalf("trusted_proxies = %v, want %v", got, want)
		}
	})

	t.Run("invalid entry fails startup", func(t *testing.T) {
		setRequired(t)
		t.Setenv("SHEPHERD_SERVER_TRUSTED_PROXIES", "0.0.0.0/0")
		if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "server.trusted_proxies") {
			t.Fatalf("Load with trusted_proxies=0.0.0.0/0: err = %v, want a server.trusted_proxies error", err)
		}
	})
}
