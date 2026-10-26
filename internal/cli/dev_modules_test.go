package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/schema"
	"shepherd/internal/signals"
	"shepherd/internal/store"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

var _ = Describe("the seeded module repository", func() {
	It("builds the same commits on every run, at the SHAs the pipelines pin", func() {
		_, first, err := buildSeedModuleRepo()
		Expect(err).NotTo(HaveOccurred())
		_, second, err := buildSeedModuleRepo()
		Expect(err).NotTo(HaveOccurred())
		Expect(second).To(Equal(first))
		Expect(first).To(HaveLen(2))
		Expect(first[0].String()).To(Equal(seedModuleCleanSHA))
		Expect(first[1].String()).To(Equal(seedModuleForeignSHA))
	})

	// The same commits pushed over a real transport to a bare repository,
	// then read back with the git CLI (when present): the objects are ones
	// git itself accepts, and the pushed main is commit 2 with commit 1 as
	// its parent.
	It("pushes a history git reads back with the same SHAs", func() {
		dir := filepath.Join(GinkgoT().TempDir(), "shepherd-modules.git")
		_, err := git.PlainInit(dir, true)
		Expect(err).NotTo(HaveOccurred())
		repo, _, err := buildSeedModuleRepo()
		Expect(err).NotTo(HaveOccurred())
		Expect(pushSeedModuleRepo(context.Background(), repo, "file://"+dir)).To(Succeed())

		// A second push of the same commits is a no-op, not an error: re-seeding.
		again, _, err := buildSeedModuleRepo()
		Expect(err).NotTo(HaveOccurred())
		Expect(pushSeedModuleRepo(context.Background(), again, "file://"+dir)).To(Succeed())

		bare, err := git.PlainOpen(dir)
		Expect(err).NotTo(HaveOccurred())
		ref, err := bare.Reference(plumbing.NewBranchReferenceName("main"), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(ref.Hash().String()).To(Equal(seedModuleForeignSHA))
		c, err := bare.CommitObject(ref.Hash())
		Expect(err).NotTo(HaveOccurred())
		Expect(c.ParentHashes).To(HaveLen(1))
		Expect(c.ParentHashes[0].String()).To(Equal(seedModuleCleanSHA))

		gitBin, err := exec.LookPath("git")
		if err != nil {
			return
		}
		run := func(args ...string) string {
			cmd := exec.Command(gitBin, append([]string{"-C", dir}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
			return strings.TrimSpace(string(out))
		}
		run("fsck", "--strict")
		Expect(run("rev-list", "main")).To(Equal(seedModuleForeignSHA + "\n" + seedModuleCleanSHA))
		Expect(run("show", seedModuleCleanSHA+":"+seedModuleCleanPath)).To(Equal(strings.TrimSpace(seedModuleClean)))
		Expect(run("show", seedModuleForeignSHA+":"+seedModuleForeignPath)).To(Equal(strings.TrimSpace(seedModuleForeign)))
	})

	It("is allowed under the normal form the pinned-import check reads", func() {
		key, err := pinnedimport.NormalizeRepository(seedModuleRepoURL)
		Expect(err).NotTo(HaveOccurred())
		Expect(key).NotTo(BeEmpty())
		Expect(seedModuleRepoURL).To(HavePrefix("http://gitea:3000/"))
	})

	// What the walkthrough shows once strict mode is on: the clean module
	// passes the tenant lint for platform-org, the foreign one is refused.
	It("holds one module strict mode accepts for platform-org and one it refuses", func() {
		platformOwns := validate.TenantPolicy{Strict: true, Owns: func(t string) bool { return t == seedOwnedTenant }}
		Expect(validate.Stage1(seedModuleClean).Valid).To(BeTrue())
		Expect(validate.Stage1(seedModuleForeign).Valid).To(BeTrue())
		Expect(validate.TenantLint(seedModuleClean, platformOwns)).To(BeEmpty())
		diags := validate.TenantLint(seedModuleForeign, platformOwns)
		Expect(diags).NotTo(BeEmpty())
		Expect(diags[0].Message).To(ContainSubstring(seedForeignTenant))
	})

	It("seeds an enabled singleton pipeline pinned to the clean commit, and a disabled foreign one", func() {
		items := modulePipelineItems()
		Expect(items).To(HaveLen(2))
		for _, item := range items {
			Expect(item.matchers).To(ConsistOf(`cluster="prod-eu-1"`, `role="singleton"`))
			Expect(validate.Stage1(item.contents).Valid).To(BeTrue(), item.name)
			Expect(validate.Stage1(validate.WrapForValidation(item.name, item.contents)).Valid).To(BeTrue(), item.name)
			Expect(item.contents).To(ContainSubstring(seedModuleRepoURL))
		}
		Expect(items[0].enabled).To(BeTrue())
		Expect(items[0].contents).To(ContainSubstring(seedModuleCleanSHA))
		Expect(items[0].contents).To(ContainSubstring(seedModuleCleanPath))
		Expect(items[1].enabled).To(BeFalse())
		Expect(items[1].contents).To(ContainSubstring(seedModuleForeignSHA))
		Expect(items[1].contents).To(ContainSubstring(seedModuleForeignPath))
	})
})

var _ = Describe("the seeded k8s-monitoring pipelines", func() {
	It("embed byte-for-byte copies of internal/signals/testdata's fixtures", func() {
		for _, name := range []string{"k8smon-metrics.alloy", "k8smon-logs.alloy"} {
			want, err := os.ReadFile(filepath.Join("..", "signals", "testdata", name))
			Expect(err).NotTo(HaveOccurred())
			got, err := seedFixtures.ReadFile("seedfixtures/" + name)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(string(want)), "%s drifted from internal/signals/testdata", name)
		}
	})

	It("are disabled, point at the seed's destinations and carry their role's signal", func() {
		items, err := k8smonPipelineItems()
		Expect(err).NotTo(HaveOccurred())
		Expect(items).To(HaveLen(2))
		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		Expect(err).NotTo(HaveOccurred())
		want := map[string]struct {
			role, url string
			sig       signals.Signal
		}{
			"k8smon-metrics": {"metrics", seedDestProdPromURL, signals.Metrics},
			"k8smon-logs":    {"logs", seedDestProdLokiURL, signals.Logs},
		}
		for _, item := range items {
			w, ok := want[item.name]
			Expect(ok).To(BeTrue(), item.name)
			Expect(item.enabled).To(BeFalse(), item.name)
			Expect(item.matchers).To(ConsistOf(`cluster="prod-eu-1"`, `role="`+w.role+`"`))
			Expect(item.contents).To(ContainSubstring(`url = "` + w.url + `"`))
			Expect(item.contents).NotTo(ContainSubstring(".example:"))
			Expect(findSeedPipelineItem(item.name).contents).To(Equal(item.contents))
			// The rewrite keeps them what #378 proves: one signal, proven, so a
			// metrics/logs collector is served them once they are enabled.
			got, err := signals.Derive(item.contents, reg)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Proven()).To(BeTrue(), item.name)
			Expect(got.Combined.Equal(signals.NewSet(w.sig))).To(BeTrue(), "%s: %s", item.name, got.Combined)
		}
	})
})

var _ = Describe("seedImportAllowlist", Label("integration"), func() {
	It("adds the module repository to platform-org's allowlist once", func() {
		ctx := context.Background()
		st, err := store.New(ctx, &config.DatabaseConfig{URL: devUsersPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		org, err := upsertSeedOrg(ctx, st, seedOrgPlatformID, "platform-org", "Platform Engineering", seedPlatformAdminGroupID, seedPlatformViewerGroupID, "platform-org")
		Expect(err).NotTo(HaveOccurred())

		Expect(seedImportAllowlist(ctx, st, org.ID)).To(Succeed())
		Expect(seedImportAllowlist(ctx, st, org.ID)).To(Succeed())

		rows, err := st.Queries.ListOrgImportRepositories(ctx, org.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Repository).To(Equal(seedModuleRepoURL))
		Expect(rows[0].CredentialID.Valid).To(BeFalse())
		org2, err := st.Queries.GetOrgByID(ctx, org.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(org2.StrictTenants).To(BeFalse(), "the seed must leave strict mode off")
	})
})
