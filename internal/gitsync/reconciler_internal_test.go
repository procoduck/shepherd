package gitsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/wizard/wizardtest"
)

// giteaFixtureCounter makes repo/token names unique across every spec that
// shares the one Gitea container started in gitsync_suite_test.go.
var giteaFixtureCounter atomic.Int64

func nextGiteaFixtureName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, giteaFixtureCounter.Add(1))
}

var _ = Describe("Reconciler.reconcileLink", Label("integration"), func() {
	const pipelineName = "myservice"

	var (
		ctx           context.Context
		cancel        context.CancelFunc
		st            *store.Store
		enc           *crypto.Encryptor
		r             *Reconciler
		link          sqlc.RepoLink
		collectorID   pgtype.UUID
		repoCloneURL  string
		defaultBranch string
	)

	BeforeEach(func() {
		skipWithoutGitea()

		ctx, cancel = context.WithCancel(context.Background())
		dbURL := reconcilerSharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		key := make([]byte, 32)
		for i := range key {
			key[i] = byte(i)
		}
		enc, err = crypto.NewEncryptor(base64.StdEncoding.EncodeToString(key))
		Expect(err).NotTo(HaveOccurred())

		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "gitsync-org", DisplayName: "Gitsync Org", AdminGroupID: "admin-group",
		})
		Expect(err).NotTo(HaveOccurred())

		cluster, err := st.Queries.UpsertCluster(ctx, "gitsync-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		collectorID = collector.ID

		// Seed a serve_cache row so specs can assert it gets flipped dirty.
		_, err = st.Pool().Exec(ctx, `INSERT INTO serve_cache (collector_id, dirty) VALUES ($1, false)`, collector.ID)
		Expect(err).NotTo(HaveOccurred())

		// Real Gitea repo + PAT credential, per docs/git-provider-design.md §4:
		// the reconciler talks real git, authenticated the same way
		// production would for the `pat` kind (Gitea, GitHub, GitLab,
		// Bitbucket).
		repoName := nextGiteaFixtureName("gitsync-repo")
		defaultBranch, repoCloneURL, err = giteaCreateRepo(ctx, repoName)
		Expect(err).NotTo(HaveOccurred())

		token, err := giteaCreateToken(ctx, nextGiteaFixtureName("gitsync-token"))
		Expect(err).NotTo(HaveOccurred())

		tokenEnc, err := enc.Encrypt([]byte(token))
		Expect(err).NotTo(HaveOccurred())

		cred, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
			OrgID: org.ID, Name: "gitsync-cred", Kind: "pat",
			Username:        pgtype.Text{String: giteaAdminUser, Valid: true},
			ClientSecretEnc: tokenEnc,
			ProviderConfig:  []byte("{}"),
		})
		Expect(err).NotTo(HaveOccurred())

		link, err = st.Queries.CreateRepoLink(ctx, sqlc.CreateRepoLinkParams{
			OrgID: org.ID, CollectorID: collector.ID, CredentialID: cred.ID,
			RepoUrl: repoCloneURL, Branch: defaultBranch, Path: "",
			PollIntervalSeconds: 300,
		})
		Expect(err).NotTo(HaveOccurred())

		// A real validator, same shape production wires via validate.New
		// (server.go): without it, syncFile now fails closed (W2-S1) rather
		// than syncing on Stage 1 syntax alone. bin resolves to a real alloy
		// binary, an operator override, or a docker shim around the pinned
		// grafana/alloy image — see wizardtest.AlloyBinary's doc comment for
		// why "" must Fail here rather than silently skip: skipWithoutGitea
		// above already gates the Docker-unavailable case, so reaching this
		// point with no usable binary is a real environment problem, not an
		// expected local-dev gap.
		bin := wizardtest.AlloyBinary()
		if bin == "" {
			Fail("gitsync: no alloy binary and no usable docker image to run Stage 2/3 validation")
		}
		v := validate.New(&config.ValidateConfig{
			AlloyBinary:    bin,
			StabilityLevel: "experimental",
			Timeout:        30 * time.Second,
			Stage3Timeout:  30 * time.Second,
		})

		r = &Reconciler{
			store:      st,
			crypto:     enc,
			validator:  v,
			limits:     gitrepo.Limits{},
			tokenCache: gitrepo.NewTokenCache(),
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	dirtyFlag := func() bool {
		var dirty bool
		Expect(st.Pool().QueryRow(ctx, `SELECT dirty FROM serve_cache WHERE collector_id = $1`, collectorID).Scan(&dirty)).To(Succeed())
		return dirty
	}

	push := func(content string) {
		p, err := newPusher(ctx, repoCloneURL, giteaAdminUser, giteaAdminPass)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.commitAndPush(ctx, map[string]string{pipelineName + ".alloy": content}, "sync fixture")).To(Succeed())
	}

	It("first sync creates the pipeline", func() {
		push("// original content")

		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())
		Expect(pipeline.Contents).To(Equal("// original content"))
		Expect(pipeline.Source).To(Equal("git"))
		Expect(pipeline.CreatedBy).To(Equal("gitsync"))

		updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("ok"))
	})

	It("updates the pipeline, records revision 2, audits, and dirties the collector's serve cache when content changed", func() {
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())
		// The reconciler's create branch records revision 1 itself, so nothing is
		// seeded here — revision 2 below must come from the update path.

		// Re-fetch the link: reconcileLink above already recorded last_commit.
		link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())

		push("// updated content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		updated, err := st.Queries.GetPipelineByID(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Contents).To(Equal("// updated content"))
		Expect(updated.UpdatedBy).To(Equal("gitsync"))

		revs, err := st.Queries.ListPipelineRevisions(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(2))
		Expect(revs[0].Revision).To(Equal(int32(2)))
		Expect(revs[0].Contents).To(Equal("// updated content"))
		Expect(revs[0].ChangedBy).To(Equal("gitsync"))

		Expect(dirtyFlag()).To(BeTrue())

		var auditCount int
		Expect(st.Pool().QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE action = 'pipeline.update' AND actor = 'gitsync' AND resource_id = $1`,
			pipeline.ID.String(),
		).Scan(&auditCount)).To(Succeed())
		Expect(auditCount).To(Equal(1))

		updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("ok"))
	})

	It("is a no-op when the fetched commit tip is unchanged", func() {
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())

		link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(link.LastCommit.Valid).To(BeTrue())

		// Second reconcile against the same, unchanged tip.
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		revs, err := st.Queries.ListPipelineRevisions(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		// The first sync creates the pipeline and its revision 1 (mirroring the API's
		// create path); the point of this spec is that an unchanged tip adds NOTHING on
		// top of that, so the count must still be exactly 1.
		Expect(revs).To(HaveLen(1), "an unchanged commit tip must not create another revision")
	})

	It("is a no-op when the fetched content is unchanged despite a new commit", func() {
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())
		// The create branch already recorded revision 1; nothing is seeded here.
		// Re-baseline the serve cache too: creating the pipeline legitimately dirtied
		// it, and this spec is about the unchanged-content sync adding nothing.
		_, err = st.Pool().Exec(ctx, `UPDATE serve_cache SET dirty = false WHERE collector_id = $1`, link.CollectorID)
		Expect(err).NotTo(HaveOccurred())

		link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())

		// A new commit that re-pushes byte-identical content: the tip
		// changes, but the pipeline content does not.
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		updated, err := st.Queries.GetPipelineByID(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Contents).To(Equal("// original content"))

		revs, err := st.Queries.ListPipelineRevisions(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(1), "no new revision should be created for unchanged content")

		Expect(dirtyFlag()).To(BeFalse())

		var auditCount int
		Expect(st.Pool().QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE action = 'pipeline.update' AND resource_id = $1`,
			pipeline.ID.String(),
		).Scan(&auditCount)).To(Succeed())
		Expect(auditCount).To(Equal(0))
	})

	It("marks sync_status=error and leaves the last good pipeline when a file fails validation", func() {
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())

		link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())

		// Re-baseline the serve cache: the initial successful sync legitimately dirties
		// it (a new pipeline changes what the collector must be served). This spec is
		// about the FAILED sync not dirtying it, so start from a clean flag.
		_, err = st.Pool().Exec(ctx, `UPDATE serve_cache SET dirty = false WHERE collector_id = $1`, link.CollectorID)
		Expect(err).NotTo(HaveOccurred())
		Expect(dirtyFlag()).To(BeFalse(), "precondition: cache starts clean")

		push("this is not valid alloy syntax {{{")
		err = r.reconcileLink(ctx, link)
		Expect(err).To(HaveOccurred())

		updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("error"))
		Expect(updatedLink.SyncError.String).NotTo(BeEmpty())

		// The last good pipeline content is untouched.
		unchanged, err := st.Queries.GetPipelineByID(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(unchanged.Contents).To(Equal("// original content"))

		Expect(dirtyFlag()).To(BeFalse())
	})

	// W2-S1: r.validator was injected at construction but never referenced
	// inside syncFile, so every synced file passed on Stage 1 (syntax)
	// alone. This file parses fine (a component block is syntactically
	// valid) but references a component that does not exist, which only
	// Stage 2's real `alloy validate` catches.
	It("rejects a file that parses but fails alloy validate (stage 2)", func() {
		push(`nonexistent.component "x" {
  forward_to = []
}`)

		err := r.reconcileLink(ctx, link)
		Expect(err).To(HaveOccurred())

		_, lookupErr := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(lookupErr).To(HaveOccurred(), "a file that fails stage 2 must never be synced as a pipeline")

		updatedLink, linkErr := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(linkErr).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("error"))
		Expect(updatedLink.SyncError.String).To(ContainSubstring("validation errors"))
	})

	// W2-S1's stage-3 dry-run: this file is individually valid, but merging
	// it against the linked collector's other enabled pipelines produces a
	// declare-block-name collision — the same failure mode
	// internal/mgmtapi's stage3Check refuses to enable on. gitsync
	// previously never ran this check at all, so a file like this synced
	// "ok" and only surfaced as a merge failure later, on the serve path.
	It("rejects a file that merges into a duplicate block label", func() {
		// "my_service" and "my-service" both sanitize to declare block
		// "pipe_my_service" (merge.SanitizeName lowercases and folds "-" to
		// "_") — pre-seed the former as an already-enabled UI pipeline
		// matching this collector's labels.
		_, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: link.OrgID, Name: "my_service", Contents: "// pre-existing",
			Matchers: json.RawMessage(`["cluster=\"gitsync-cluster\""]`),
			Enabled:  true, Source: "ui",
			WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		p, err := newPusher(ctx, repoCloneURL, giteaAdminUser, giteaAdminPass)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.commitAndPush(ctx, map[string]string{
			"my-service.alloy": "// duplicate label candidate",
		}, "dup label fixture")).To(Succeed())

		err = r.reconcileLink(ctx, link)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("collision"))

		_, lookupErr := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: "my-service"})
		Expect(lookupErr).To(HaveOccurred(), "a file whose stage-3 merge dry-run fails must never be synced as a pipeline")

		updatedLink, linkErr := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(linkErr).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("error"))
	})
})

// A credential value must never reach repo_links.sync_error (shown to every
// org reader) or a log line, whatever a transport error chooses to echo
// (CodeQL go/clear-text-logging). Red run: making redactSecrets return err
// unchanged fails the first two specs.
var _ = Describe("secret scrubbing of recorded sync errors", func() {
	It("replaces every occurrence of a secret and drops the original wrapping", func() {
		err := fmt.Errorf("fetching files: %w", errors.New("https://svc:hunter2@git.example.com: 401 (hunter2)"))
		got := redactSecrets(err, []string{"hunter2"})
		Expect(got.Error()).To(Equal("fetching files: https://svc:[REDACTED]@git.example.com: 401 ([REDACTED])"))
		Expect(errors.Unwrap(got)).To(BeNil(), "a scrubbed error must not still wrap the unscrubbed one")
	})

	It("scrubs the values of every auth kind the reconciler builds", func() {
		for _, tc := range []struct {
			auth   gitrepo.Auth
			secret string
		}{
			{gitrepo.BasicAuth{Username: "u", Password: "pw-basic"}, "pw-basic"},
			{gitrepo.PATAuth{Username: "u", Token: "tok-pat"}, "tok-pat"},
			{gitrepo.SSHAuth{PrivateKeyPEM: []byte("KEYMATERIAL"), Passphrase: "pp-ssh"}, "pp-ssh"},
			{gitrepo.SSHAuth{PrivateKeyPEM: []byte("KEYMATERIAL"), Passphrase: "pp-ssh"}, "KEYMATERIAL"},
			{gitrepo.AdoSPAuth{ClientSecret: "cs-ado"}, "cs-ado"},
		} {
			scrub := newSecretScrubber(tc.auth)
			got := scrub(errors.New("boom " + tc.secret + " end"))
			Expect(got.Error()).To(Equal("boom [REDACTED] end"), "%T must scrub %q", tc.auth, tc.secret)
		}
	})

	It("leaves an error alone when no secret appears in it, wrapping intact", func() {
		inner := errors.New("dial tcp: connection refused")
		err := fmt.Errorf("getting latest commit: %w", inner)
		got := redactSecrets(err, []string{"hunter2", ""})
		Expect(got).To(BeIdenticalTo(err))
		Expect(errors.Is(got, inner)).To(BeTrue())
	})

	It("never treats an empty secret as a match", func() {
		err := errors.New("plain text")
		Expect(redactSecrets(err, []string{""}).Error()).To(Equal("plain text"))
		Expect(redactSecrets(nil, []string{"x"})).To(BeNil())
	})
})
