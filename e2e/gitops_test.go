//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gossh "golang.org/x/crypto/ssh"
)

// Scenario 5 — GitOps sync against a real git provider (F9,
// docs/git-provider-design.md §4). It replaces the old scenario that
// drove mockmsft's hand-written /__fixture ADO mock: Shepherd now speaks
// real git to a real Gitea instance (e2e/docker-compose.e2e.yaml), and
// mockmsft's only remaining git-related role is standing in for the two
// OAuth-shaped token endpoints the ado_sp/github_app strategies exchange a
// long-lived secret for a short-lived one against — see e2e/mockmsft.
//
// Full create/update/error/recover lifecycle (§4 "E2E scenario 5,
// rewritten" steps 1-4) runs once, over the `pat` auth kind, since that
// path is auth-kind-independent once a repo is readable at all. Steps 5
// ("repeat step 1 per auth kind") then runs just the initial-sync half
// again over `ssh` and `github_app` — the two kinds this harness can
// exercise cheaply without a real Azure DevOps or GitHub tenant.
//
// ado_sp is deliberately not in this table: docs/git-provider-design.md
// §4 says a mock Entra token endpoint should be enough to prove the
// ado_sp strategy's acquisition + plumbing, and mockmsft implements one
// (POST /{tenant}/oauth2/v2.0/token) for exactly that. But
// internal/gitsync.Reconciler.buildAuth's ado_sp case
// (internal/gitsync/reconciler.go) builds gitrepo.AdoSPAuth without ever
// setting TokenURL, so it always mints against the real
// login.microsoftonline.com — there is no credential- or config-level way
// to redirect it at a mock, unlike github_app's api_base_url
// (provider_config, already a normal per-credential field). Wiring that
// through — e.g. threading config.ADOConfig.BaseURL (already present,
// already unused) into the TokenURL the reconciler builds — is a change
// to internal/gitsync/reconciler.go, which this task does not own; noted
// for a follow-up rather than made here.
func gitOpsScenario5() {
	var collectorID string

	It("finds the e2e collector that repo links will target", func() {
		Expect(orgID).NotTo(BeEmpty(), "org must be claimed first (scenario 1)")
		Eventually(func() bool {
			var collectors struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			adminClient.mustRPC("FleetService", "ListCollectors", map[string]string{"org_id": orgID}, &collectors)
			if len(collectors.Items) == 0 {
				return false
			}
			collectorID = collectors.Items[0].ID
			return true
		}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(BeTrue())
	})

	// --- 5a. full lifecycle, pat ---------------------------------------
	const (
		patRepoName     = "e2e-gitops-pat"
		patPipelineFile = "gitopspat.alloy"
		patPipelineName = "gitopspat"
	)
	var (
		patRepoInternalURL string
		patPusher          *pusher
		patPipelineID      string
		patHashAfterV2     string
	)

	It("pat: seeding a valid file makes a source=git pipeline appear and Alloy apply the merge", func() {
		ctx := context.Background()

		_, hostURL, internalURL := mustCreateRepo(ctx, patRepoName)
		patRepoInternalURL = internalURL
		token := mustCreateToken(ctx, patRepoName+"-token")

		p, err := newPusher(ctx, hostURL, giteaAdminUser, giteaAdminPass)
		Expect(err).NotTo(HaveOccurred())
		patPusher = p
		_, err = p.commitAndPush(ctx, map[string]string{
			patPipelineFile: `prometheus.exporter.self "e2e_git_pat_v1" { }` + "\n",
		}, "v1: initial fixture")
		Expect(err).NotTo(HaveOccurred())

		credID := createGitCredential(map[string]any{
			"name": patRepoName, "kind": "pat",
			"username": giteaAdminUser, "client_secret": token,
		})
		createRepoLink(map[string]any{
			"collector_id": collectorID, "credential_id": credID,
			"repo_url": patRepoInternalURL, "branch": "main", "path": "/",
			"poll_interval_seconds": 3,
		})

		var pl gitPipeline
		Eventually(func() bool {
			var ok bool
			pl, ok = findPipelineByName(patPipelineName)
			return ok && pl.Source == "git"
		}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(BeTrue())
		patPipelineID = pl.ID

		eventuallyServedConfigContains(collectorID, "e2e_git_pat_v1")
		_, hash := servedConfig(collectorID)
		Expect(hash).NotTo(BeEmpty())
	})

	It("pat: pushing a second commit updates the pipeline and records a revision", func() {
		// This is the path that was silently broken until 0194541 (repo_links
		// were never re-synced past their first commit) and had never been
		// covered end to end — docs/git-provider-design.md §4.
		Expect(patPipelineID).NotTo(BeEmpty(), "initial sync must have run")

		beforeCount := revisionCount(patPipelineID)

		ctx := context.Background()
		_, err := patPusher.commitAndPush(ctx, map[string]string{
			patPipelineFile: `prometheus.exporter.self "e2e_git_pat_v2" { }` + "\n",
		}, "v2: change content")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() string {
			content, _ := servedConfig(collectorID)
			return content
		}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(ContainSubstring("e2e_git_pat_v2"))

		Eventually(func() int {
			return revisionCount(patPipelineID)
		}).WithTimeout(15*time.Second).WithPolling(time.Second).Should(BeNumerically(">", beforeCount),
			"a new pipeline_revisions row must be recorded for the second commit")

		_, patHashAfterV2 = servedConfig(collectorID)
		Expect(patHashAfterV2).NotTo(BeEmpty())
	})

	It("pat: pushing an invalid file marks sync_status=error and keeps serving the last good config", func() {
		Expect(patHashAfterV2).NotTo(BeEmpty(), "v2 sync must have completed")

		linkID := findRepoLinkByCollector(collectorID, patRepoInternalURL)
		Expect(linkID).NotTo(BeEmpty())

		ctx := context.Background()
		_, err := patPusher.commitAndPush(ctx, map[string]string{
			patPipelineFile: "prometheus.scrape { missing closing\n",
		}, "v3: break it")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() string {
			return repoLinkSyncStatus(linkID)
		}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(Equal("error"))

		// The hash must not move: the broken commit must never reach the
		// served config, and the last known-good content must stay served.
		content, hash := servedConfig(collectorID)
		Expect(hash).To(Equal(patHashAfterV2), "served hash must stay pinned to the last good sync while sync_status=error")
		Expect(content).To(ContainSubstring("e2e_git_pat_v2"))
	})

	// W2-S1 consumed layer: gitsync's Reconciler.validator was injected at
	// construction but never referenced, so a file that PARSES (stage 1
	// passes) but fails `alloy validate` (stage 2 — here, a component that
	// does not exist) used to sync "ok" anyway, same as the two production
	// stage3Check callers this closes the gap with. Unlike the syntax-error
	// case above (caught by stage 1 alone, already covered before this
	// step), this spec is only red before W2-S1's fix.
	It("pat: pushing a file that parses but fails alloy validate (stage 2) marks sync_status=error and keeps serving the last good config", func() {
		Expect(patHashAfterV2).NotTo(BeEmpty(), "v2 sync must have completed")

		linkID := findRepoLinkByCollector(collectorID, patRepoInternalURL)
		Expect(linkID).NotTo(BeEmpty())

		ctx := context.Background()
		_, err := patPusher.commitAndPush(ctx, map[string]string{
			patPipelineFile: "nonexistent.component \"x\" {\n  forward_to = []\n}\n",
		}, "v3b: fails stage 2")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() string {
			return repoLinkSyncStatus(linkID)
		}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(Equal("error"))

		// The broken commit must never reach the served config, same
		// contract as the stage-1 failure above.
		content, hash := servedConfig(collectorID)
		Expect(hash).To(Equal(patHashAfterV2), "served hash must stay pinned to the last good sync while sync_status=error")
		Expect(content).To(ContainSubstring("e2e_git_pat_v2"))
	})

	It("pat: fixing the file recovers — sync_status returns to ok and the new content is served", func() {
		linkID := findRepoLinkByCollector(collectorID, patRepoInternalURL)
		Expect(linkID).NotTo(BeEmpty())

		ctx := context.Background()
		_, err := patPusher.commitAndPush(ctx, map[string]string{
			patPipelineFile: `prometheus.exporter.self "e2e_git_pat_v3" { }` + "\n",
		}, "v4: fix it")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() string {
			return repoLinkSyncStatus(linkID)
		}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(Equal("ok"))

		Eventually(func() string {
			content, _ := servedConfig(collectorID)
			return content
		}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(ContainSubstring("e2e_git_pat_v3"))

		_, hash := servedConfig(collectorID)
		Expect(hash).NotTo(Equal(patHashAfterV2), "hash must move again once the fix lands")
	})

	// --- 5b. initial sync, table-driven across the remaining cheap auth
	// kinds. Each case only proves step 1 (seed a valid file -> a
	// source=git pipeline appears) — the update/error/recover path above
	// is auth-kind-independent once gitrepo can read the repo at all, so
	// repeating all four steps per kind would just re-test the same
	// reconciler code path four times over.
	for _, tc := range []struct {
		kind, repoName, fileName, pipelineName string
		setup                                  func(ctx context.Context, repoName string) (credentialBody map[string]any, repoURL string)
	}{
		{
			kind: "ssh", repoName: "e2e-gitops-ssh", fileName: "gitopsssh.alloy", pipelineName: "gitopsssh",
			setup: sshAuthSetup,
		},
		{
			kind: "github_app", repoName: "e2e-gitops-ghapp", fileName: "gitopsghapp.alloy", pipelineName: "gitopsghapp",
			setup: githubAppAuthSetup,
		},
	} {
		It(fmt.Sprintf("%s: seeding a valid file makes a source=git pipeline appear", tc.kind), func() {
			ctx := context.Background()

			_, internalURL := mustPrepareRepoWithFixture(ctx, tc.repoName, tc.fileName)
			credBody, repoURL := tc.setup(ctx, tc.repoName)
			if repoURL == "" {
				repoURL = internalURL
			}

			credID := createGitCredential(credBody)
			createRepoLink(map[string]any{
				"collector_id": collectorID, "credential_id": credID,
				"repo_url": repoURL, "branch": "main", "path": "/",
				"poll_interval_seconds": 3,
			})

			Eventually(func() bool {
				pl, ok := findPipelineByName(tc.pipelineName)
				return ok && pl.Source == "git"
			}).WithTimeout(30*time.Second).WithPolling(time.Second).Should(BeTrue(), "%s: pipeline never appeared", tc.kind)

			// The serve cache recomputes lazily on the agent's next poll (spec 6.3).
			eventuallyServedConfigContains(collectorID, tc.pipelineName)
			_, hash := servedConfig(collectorID)
			Expect(hash).NotTo(BeEmpty())
		})
	}
}

// mustCreateRepo is giteaCreateRepo with e2e-level Gomega assertions
// instead of a returned error, matching the terse call sites in this file.
func mustCreateRepo(ctx context.Context, name string) (branch, hostURL, internalURL string) {
	GinkgoHelper()
	branch, hostURL, internalURL, err := giteaCreateRepo(ctx, name)
	Expect(err).NotTo(HaveOccurred())
	return branch, hostURL, internalURL
}

func mustCreateToken(ctx context.Context, name string) string {
	GinkgoHelper()
	token, err := giteaCreateToken(ctx, name)
	Expect(err).NotTo(HaveOccurred())
	return token
}

// mustPrepareRepoWithFixture creates repoName with an initial commit, then
// pushes one .alloy file (named for the pipeline it should become) as a
// second commit — the shape every 5b case needs before wiring up its own
// credential kind.
func mustPrepareRepoWithFixture(ctx context.Context, repoName, fileName string) (hostURL, internalURL string) {
	GinkgoHelper()
	_, hostURL, internalURL = mustCreateRepo(ctx, repoName)
	p, err := newPusher(ctx, hostURL, giteaAdminUser, giteaAdminPass)
	Expect(err).NotTo(HaveOccurred())
	pipelineName := fileName[:len(fileName)-len(".alloy")]
	_, err = p.commitAndPush(ctx, map[string]string{
		fileName: fmt.Sprintf(`prometheus.exporter.self %q { }`+"\n", pipelineName),
	}, "initial fixture")
	Expect(err).NotTo(HaveOccurred())
	return hostURL, internalURL
}

// sshAuthSetup registers a fresh deploy key on repoName, fetches Gitea's
// real SSH host key to build a correct known_hosts entry (proving the
// mandatory known_hosts verification actually works, not just that it's
// configured), and returns the credential body plus the ssh:// repo URL —
// pointed at the compose network's internal address, since that's what
// shepherd itself dials.
func sshAuthSetup(ctx context.Context, repoName string) (map[string]any, string) {
	GinkgoHelper()
	privatePEM, pubAuthorized, err := generateSSHKeyPair()
	Expect(err).NotTo(HaveOccurred())
	Expect(giteaAddDeployKey(ctx, giteaAdminUser, repoName, repoName+"-deploy-key", pubAuthorized)).To(Succeed())

	signer, err := gossh.ParsePrivateKey(privatePEM)
	Expect(err).NotTo(HaveOccurred())
	hostKey, err := fetchSSHHostKey(ctx, giteaHostSSHAddr, signer)
	Expect(err).NotTo(HaveOccurred())
	knownHosts := knownHostsLine(giteaInternalSSHAddr, hostKey)

	body := map[string]any{
		"name": repoName, "kind": "ssh",
		"username": "git", "client_secret": string(privatePEM),
		"ssh_known_hosts": knownHosts,
	}
	repoURL := fmt.Sprintf("ssh://git@%s/%s/%s.git", giteaInternalSSHAddr, giteaAdminUser, repoName)
	return body, repoURL
}

// githubAppAuthSetup seeds mockmsft's mock GitHub App installation-token
// endpoint (verifies the RS256 JWT it receives, per
// docs/git-provider-design.md §4) to hand back a real Gitea PAT once
// called, then builds a github_app credential pointed at that mock
// (api_base_url) rather than the real api.github.com.
func githubAppAuthSetup(ctx context.Context, repoName string) (map[string]any, string) {
	GinkgoHelper()
	privatePEM, publicPEM, err := generateRSAKeyPair()
	Expect(err).NotTo(HaveOccurred())
	token := mustCreateToken(ctx, repoName+"-installation-token")

	const appID = "e2e-github-app-1"
	fixture("github_app", map[string]any{
		"app_id": appID, "public_key_pem": publicPEM, "token": token,
	})

	body := map[string]any{
		"name": repoName, "kind": "github_app",
		"client_secret": string(privatePEM),
		"provider_config": map[string]any{
			"app_id": appID, "installation_id": "1",
			"api_base_url": "http://mockmsft:9090",
		},
	}
	return body, "" // repoURL: caller falls back to internalURL
}

// gitPipeline is the subset of the pipeline JSON shape scenario 5 asserts on.
type gitPipeline struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

func findPipelineByName(name string) (gitPipeline, bool) {
	GinkgoHelper()
	var list struct {
		Items []gitPipeline `json:"items"`
	}
	adminClient.mustRPC("PipelineService", "ListPipelines", map[string]string{"org_id": orgID}, &list)
	for _, p := range list.Items {
		if p.Name == name {
			return p, true
		}
	}
	return gitPipeline{}, false
}

func findRepoLinkByCollector(collectorID, repoURL string) string {
	GinkgoHelper()
	var list struct {
		Items []struct {
			ID          string `json:"id"`
			CollectorID string `json:"collectorId"`
			RepoURL     string `json:"repoUrl"`
		} `json:"items"`
	}
	adminClient.mustRPC("GitOpsService", "ListRepoLinks", map[string]string{"org_id": orgID}, &list)
	for _, l := range list.Items {
		if l.CollectorID == collectorID && l.RepoURL == repoURL {
			return l.ID
		}
	}
	return ""
}

func repoLinkSyncStatus(id string) string {
	GinkgoHelper()
	var list struct {
		Items []struct {
			ID         string `json:"id"`
			SyncStatus string `json:"syncStatus"`
		} `json:"items"`
	}
	adminClient.mustRPC("GitOpsService", "ListRepoLinks", map[string]string{"org_id": orgID}, &list)
	for _, l := range list.Items {
		if l.ID == id {
			return l.SyncStatus
		}
	}
	return ""
}

// eventuallyServedConfigContains waits for the collector's served config to carry want.
// The serve cache is recomputed LAZILY inside GetConfig (spec section 6.3), so marking it
// dirty is not enough — the assertion must wait for the agent's next poll to rebuild it.
func eventuallyServedConfigContains(collectorID, want string) string {
	GinkgoHelper()
	var content string
	Eventually(func() string {
		content, _ = servedConfig(collectorID)
		return content
	}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(ContainSubstring(want))
	return content
}

func servedConfig(collectorID string) (content, hash string) {
	GinkgoHelper()
	var served struct {
		Content string `json:"content"`
		Hash    string `json:"hash"`
	}
	adminClient.mustRPC("FleetService", "GetServedConfig", map[string]string{"org_id": orgID, "id": collectorID}, &served)
	return served.Content, served.Hash
}

// revisionCount is how many pipeline_revisions rows the pipeline has.
func revisionCount(pipelineID string) int {
	GinkgoHelper()
	var list struct {
		Items []struct {
			Revision int `json:"revision"`
		} `json:"items"`
	}
	adminClient.mustRPC("PipelineService", "ListRevisions", map[string]string{"org_id": orgID, "id": pipelineID}, &list)
	return len(list.Items)
}

// createGitCredential and createRepoLink take the request message's proto
// field names; org_id is filled in here. Connect answers a create with 200.
func createGitCredential(body map[string]any) string {
	GinkgoHelper()
	var resp struct {
		ID string `json:"id"`
	}
	body["org_id"] = orgID
	status := adminClient.rpc("GitOpsService", "CreateCredential", body, &resp)
	Expect(status).To(Equal(http.StatusOK), "creating git credential kind=%v", body["kind"])
	Expect(resp.ID).NotTo(BeEmpty())
	return resp.ID
}

func createRepoLink(body map[string]any) string {
	GinkgoHelper()
	var resp struct {
		ID string `json:"id"`
	}
	body["org_id"] = orgID
	status := adminClient.rpc("GitOpsService", "CreateRepoLink", body, &resp)
	Expect(status).To(Equal(http.StatusOK), "creating repo link for %v", body["repo_url"])
	Expect(resp.ID).NotTo(BeEmpty())
	return resp.ID
}
