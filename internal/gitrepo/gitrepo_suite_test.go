package gitrepo_test

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
)

func TestGitrepo(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "gitrepo Suite")
}

// giteaImage is pinned per docs/git-provider-design.md §4: a real git
// server, not a hand-written REST mock.
const giteaImage = "gitea/gitea:1-rootless"

const (
	giteaAdminUser = "shepherd-admin"
	giteaAdminPass = "Sh3pherd-Admin-Pass-1"
	giteaAdminMail = "admin@shepherd.test"
)

// giteaBaseURL is the host-reachable base URL of the shared Gitea
// container, e.g. http://127.0.0.1:54321. Set once in BeforeSuite.
var giteaBaseURL string

// giteaSSHAddr is the host-reachable "host:port" of the shared Gitea
// container's SSH listener, set once in BeforeSuite alongside
// giteaBaseURL. Used to build ssh:// clone URLs and to fetch the server's
// real host key for the `ssh` auth kind tests.
var giteaSSHAddr string

var giteaContainer testcontainers.Container

// giteaSkipReason is non-empty when Docker (or the Gitea container) is not
// available; specs check it and Skip themselves so `go test
// ./internal/gitrepo/` still passes in an environment without Docker, per
// the task's "skip-able without Docker" requirement.
var giteaSkipReason string

var _ = BeforeSuite(func() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        giteaImage,
		ExposedPorts: []string{"3000/tcp", "2222/tcp"},
		Env: map[string]string{
			// Non-interactive install, no web setup wizard.
			"INSTALL_LOCK": "true",
			"HTTP_PORT":    "3000",
			// The rootless image can't bind the privileged port 22, so
			// its built-in SSH server listens on 2222 instead — set
			// explicitly rather than relying on the image's default so a
			// change there doesn't silently break the `ssh` auth kind
			// tests (step 2).
			"DISABLE_SSH":     "false",
			"SSH_LISTEN_PORT": "2222",
			"SSH_PORT":        "2222",
			// Anonymous read of public repos must work for the `none`
			// auth kind test; this is Gitea's default, set explicitly so
			// a change to that default doesn't silently break the suite.
			"GITEA__service__REQUIRE_SIGNIN_VIEW": "false",
		},
		WaitingFor: wait.ForAll(
			wait.ForHTTP("/api/healthz").
				WithPort("3000/tcp").
				WithStartupTimeout(2*time.Minute).
				WithPollInterval(1*time.Second),
			wait.ForListeningPort("2222/tcp").
				WithStartupTimeout(2*time.Minute).
				WithPollInterval(1*time.Second),
		),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		giteaSkipReason = fmt.Sprintf("gitrepo: could not start gitea container (Docker unavailable?): %v", err)
		return
	}
	giteaContainer = ctr

	host, err := ctr.Host(ctx)
	Expect(err).NotTo(HaveOccurred())
	port, err := ctr.MappedPort(ctx, "3000/tcp")
	Expect(err).NotTo(HaveOccurred())
	giteaBaseURL = fmt.Sprintf("http://%s:%s", host, port.Port())

	sshPort, err := ctr.MappedPort(ctx, "2222/tcp")
	Expect(err).NotTo(HaveOccurred())
	giteaSSHAddr = fmt.Sprintf("%s:%s", host, sshPort.Port())

	createGiteaAdmin(ctx, ctr)
})

var _ = AfterSuite(func() {
	if giteaContainer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	Expect(giteaContainer.Terminate(ctx)).To(Succeed())
})

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
