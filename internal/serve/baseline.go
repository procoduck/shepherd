package serve

import (
	"strings"

	"shepherd/internal/beacon"
	"shepherd/internal/config"
)

// BeaconBaseline derives D6's baseline configuration (Deps.BeaconBaseline)
// from the server configuration. It is the ONE place that decision is made:
// every serve path — internal/agentapi's lazy GetConfig recompute, the
// PipelineService internal/mgmtapi.MountRPC builds, and the one
// internal/server builds for gitsync's eager recompute — takes its baseline
// from here, so they cannot disagree about it (#369: the eager paths once
// passed server.base_url even with beacon_disabled set, the lazy path passed
// nothing, and a beacon-disabled collector's bytes flipped with whichever
// path recomputed last).
//
// The result's RemoteWriteURL is empty — beacon.AppendBaseline's documented
// no-op, the served config carries no baseline at all — when
// server.beacon_disabled is set or server.base_url is empty. Otherwise it
// points at base_url + beacon.WritePath, authenticating with oidc.beacon_auth's
// mode (agent-token basic_auth, or the collector's OAuth2 client credentials).
func BeaconBaseline(cfg *config.Config) beacon.BaselineConfig {
	if cfg == nil || cfg.Server.BeaconDisabled || cfg.Server.BaseURL == "" {
		return beacon.NewBaselineConfig("")
	}
	b := beacon.NewBaselineConfig(strings.TrimSuffix(cfg.Server.BaseURL, "/") + beacon.WritePath)
	b.OAuth2 = beacon.OAuth2ForBeacon(cfg.OIDC.BeaconAuth, cfg.OIDC.AgentTokenURL, cfg.OIDC.AgentScopes)
	return b
}
