package gitsync

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"shepherd/internal/store"
	"shepherd/internal/testutil"
)

func TestGitsync(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Gitsync Suite")
}

// giteaImage matches internal/gitrepo's suite (docs/git-provider-design.md
// §4): reconciler tests exercise a real git server, not a hand-written REST
// mock.
const giteaImage = "gitea/gitea:1-rootless"

const (
	giteaAdminUser = "shepherd-admin"
	giteaAdminPass = "Sh3pherd-Admin-Pass-1"
	giteaAdminMail = "admin@shepherd.test"
)

// giteaBaseURL is the host-reachable base URL of the shared Gitea
// container, set once in BeforeSuite.
var giteaBaseURL string

var giteaContainer testcontainers.Container

// giteaSkipReason is non-empty when Docker (or the Gitea container) is not
// available; specs check it and Skip themselves so `go test
// ./internal/gitsync/` still passes without Docker.
var giteaSkipReason string

// reconcilerSharedPG is a Postgres container shared across every spec in
// this package.
var reconcilerSharedPG *testutil.SharedPostgres

// Ginkgo allows only one BeforeSuite (Synchronized or not) per suite, so
// the Postgres and Gitea containers used across this package's specs are
// both started here.
var _ = SynchronizedBeforeSuite(func() []byte {
	ctx := context.Background()

	var err error
	reconcilerSharedPG, err = testutil.StartSharedPostgres(ctx)
	Expect(err).NotTo(HaveOccurred())
	Expect(store.MigrateUp(ctx, reconcilerSharedPG.RootURL)).To(Succeed())

	startGitea(ctx)

	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if reconcilerSharedPG != nil {
		Expect(reconcilerSharedPG.Terminate(context.Background())).To(Succeed())
	}
	if giteaContainer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		Expect(giteaContainer.Terminate(ctx)).To(Succeed())
	}
})

func startGitea(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        giteaImage,
		ExposedPorts: []string{"3000/tcp"},
		Env: map[string]string{
			"INSTALL_LOCK": "true",
			"HTTP_PORT":    "3000",
			"DISABLE_SSH":  "true",
			// Anonymous read isn't needed by these specs; every repo link
			// authenticates with a PAT.
			"GITEA__service__REQUIRE_SIGNIN_VIEW": "false",
		},
		WaitingFor: wait.ForHTTP("/api/healthz").
			WithPort("3000/tcp").
			WithStartupTimeout(2 * time.Minute).
			WithPollInterval(1 * time.Second),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		giteaSkipReason = fmt.Sprintf("gitsync: could not start gitea container (Docker unavailable?): %v", err)
		return
	}
	giteaContainer = ctr

	host, err := ctr.Host(ctx)
	Expect(err).NotTo(HaveOccurred())
	port, err := ctr.MappedPort(ctx, "3000/tcp")
	Expect(err).NotTo(HaveOccurred())
	giteaBaseURL = fmt.Sprintf("http://%s:%s", host, port.Port())

	createGiteaAdmin(ctx, ctr)
}

// createGiteaAdmin runs `gitea admin user create` inside the running
// container via the wrapper at /usr/local/bin/gitea, which injects the
// image's -c /etc/gitea/app.ini for us.
func createGiteaAdmin(ctx context.Context, ctr testcontainers.Container) {
	GinkgoHelper()

	exitCode, reader, err := ctr.Exec(ctx, []string{
		"/usr/local/bin/gitea", "admin", "user", "create",
		"--username", giteaAdminUser,
		"--password", giteaAdminPass,
		"--email", giteaAdminMail,
		"--admin",
		"--must-change-password=false",
	})
	Expect(err).NotTo(HaveOccurred())

	var out strings.Builder
	if reader != nil {
		_, _ = io.Copy(&out, reader) //nolint:errcheck // best-effort diagnostic capture below
	}
	Expect(exitCode).To(Equal(0), "gitea admin user create failed: "+out.String())
}

// skipWithoutGitea skips the calling spec when the shared Gitea container
// failed to start (typically: no Docker).
func skipWithoutGitea() {
	GinkgoHelper()
	if giteaSkipReason != "" {
		Skip(giteaSkipReason)
	}
}
