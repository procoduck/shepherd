package wizardtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"shepherd/internal/version"
)

// loadReadyTimeout bounds how long one golden may take to go from `docker
// run` to Alloy's /-/ready answering 200. A config Alloy refuses exits within
// a second or two of starting; this only has to cover a cold container start.
const loadReadyTimeout = 90 * time.Second

// AssertGoldensLoadInRealAlloy starts the pinned grafana/alloy image with
// `alloy run` on every committed golden in testdataDir and requires each one
// to finish its initial load — /-/ready answering 200 — rather than exit.
//
// This is the layer AssertGoldensAgainstRealAlloy cannot reach. `alloy
// validate` decodes a config against the component argument types, but it
// never builds the components, so any rule a component enforces in its
// constructor is invisible to it. stage.logfmt is the case that shipped:
// `stage.logfmt {}` decodes cleanly (every attribute is optional in the
// type), passes `alloy validate`, and is then refused by a running Alloy
// with "logfmt mapping or regex is required" — every collector serving that
// pipeline broke while Stage 2 and the goldens were green. `alloy run`
// builds every component, so this catches that whole class for every
// wizard.
//
// Loading is not collecting, so a golden that reaches ready is then kept
// running for a few seconds and must run cleanly too: no runtime error of
// the kinds in runtimeErrors in its log, and every component healthy in
// Alloy's components API (assertRuntimeHealthy). A full URL in a scrape
// target's __address__ and a glob in loki.source.file's __path__ both
// loaded, reported ready and left the collector APPLIED while collecting
// nothing; only the log said so.
//
// Some components reach outside the process while being built, and a test
// container has none of those surroundings. remote.kubernetes.secret
// fetches its Secret in its constructor and fails the load when it cannot;
// discovery.kubernetes and loki.source.kubernetes need in-cluster
// configuration to construct a client at all. So the container gets a
// fake in-cluster environment: a service-account token and CA, and
// KUBERNETES_SERVICE_HOST pointing at a small TLS server in this process
// that answers every Secret GET with each key the goldens reference. Every
// other request it answers 404, which the discovery components only log at
// run time — a failure there is environmental, never a load refusal.
//
// Like AssertGoldensAgainstRealAlloy this calls t.Fatal, never t.Skip, when
// docker is unavailable: a guard that silently passes zero checks is the
// failure class these helpers exist to close. It always runs the pinned
// image (a host `alloy` binary cannot be given the in-cluster files).
func AssertGoldensLoadInRealAlloy(t *testing.T, testdataDir string) {
	t.Helper()
	AssertGoldensLoadInRealAlloyWith(t, testdataDir, LoadFixtures{})
}

// LoadFixtures is what a collector's surroundings hold for goldens whose
// components parse a value while being built: a database exporter refuses an
// empty or malformed DSN at load time, so a placeholder would fail the load
// for a reason that only exists in the test.
type LoadFixtures struct {
	// Env gives each sys.env(...) variable a golden reads a representative
	// value, set in the container. On a real collector the operator sets
	// them. Every variable a golden reads must be here — a missing one fails
	// the test rather than quietly loading an empty value.
	Env map[string]string
	// SecretData overrides what the fake API serves for one Secret, keyed
	// "namespace/name" and then by data key; keys not overridden still get
	// the default placeholder. Every entry must name a Secret some golden
	// reads through remote.kubernetes.secret, and a key that golden reads
	// from it, so a fixture cannot silently stop proving anything.
	SecretData map[string]map[string]string
}

// AssertGoldensLoadInRealAlloyWith is AssertGoldensLoadInRealAlloy with the
// collector-side values in fx.
func AssertGoldensLoadInRealAlloyWith(t *testing.T, testdataDir string, fx LoadFixtures) {
	t.Helper()
	checkRuntimeErrorsPin(t)
	env := fx.Env
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("no docker to run the pinned Alloy image — this guard must not silently pass")
	}
	image, ok := ensurePinnedImage(docker)
	if !ok {
		t.Fatalf("could not find or pull %s — this guard must not silently pass", pinnedImage())
	}

	goldens, err := filepath.Glob(filepath.Join(testdataDir, "*.golden.alloy"))
	if err != nil {
		t.Fatalf("glob goldens: %v", err)
	}
	if len(goldens) == 0 {
		t.Fatalf("no goldens found in %s — the guard would pass vacuously", testdataDir)
	}
	contents := make(map[string]string, len(goldens))
	for _, g := range goldens {
		b, readErr := os.ReadFile(g) //nolint:gosec // g comes from filepath.Glob over a caller-fixed testdataDir, not external input
		if readErr != nil {
			t.Fatalf("read %s: %v", g, readErr)
		}
		contents[g] = string(b)
		for _, m := range sysEnvRef.FindAllStringSubmatch(string(b), -1) {
			if _, ok := env[m[1]]; !ok {
				t.Fatalf("%s reads sys.env(%q) but the load test has no value for it — "+
					"pass a representative one in LoadFixtures.Env", g, m[1])
			}
		}
	}
	envArgs := make([]string, 0, 2*len(env))
	for k, v := range env {
		envArgs = append(envArgs, "-e", k+"="+v)
	}

	checkSecretFixtures(t, contents, fx.SecretData)
	api := startFakeKubeAPI(t, secretKeysReferenced(contents), fx.SecretData)
	sa := writeServiceAccount(t, api.caPEM)

	for _, g := range goldens {
		content := contents[g]
		t.Run(filepath.Base(g), func(t *testing.T) {
			t.Parallel()
			if err := loadInAlloy(t, docker, image, api.port, sa, envArgs, content); err != nil {
				t.Errorf("%s does not load in a running alloy %s: %v\n--- config ---\n%s",
					g, version.AlloySchemaVersion, err, content)
			}
		})
	}
}

// loadInAlloy runs one config in a throwaway container and waits for the
// initial load to finish. A nil return means Alloy built every component
// and reported ready; an error carries the container's log.
func loadInAlloy(t *testing.T, docker, image string, apiPort int, saDir string, envArgs []string, content string) error {
	t.Helper()
	cfgDir := worldReadableTempDir(t)
	cfgPath := filepath.Join(cfgDir, "config.alloy")
	//nolint:gosec // G306: the container's alloy user must read it; a test-only fixture, no secret.
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), loadReadyTimeout+30*time.Second)
	defer cancel()

	args := []string{
		"run", "-d",
		"--add-host=host.docker.internal:host-gateway",
		"-e", "KUBERNETES_SERVICE_HOST=host.docker.internal",
		"-e", "KUBERNETES_SERVICE_PORT=" + strconv.Itoa(apiPort),
		"-v", cfgDir + ":/etc/shepherd-wizard-test:ro",
		"-v", saDir + ":/var/run/secrets/kubernetes.io/serviceaccount:ro",
		"-p", "127.0.0.1::12345",
	}
	args = append(args, envArgs...)
	args = append(args,
		image,
		"run",
		"--server.http.listen-addr=0.0.0.0:12345",
		"--storage.path=/tmp/alloy-data",
		"--stability.level=experimental",
		"/etc/shepherd-wizard-test/config.alloy",
	)
	//nolint:gosec // docker comes from exec.LookPath, image is the pinned constant, the rest are test temp paths and caller-fixed env.
	out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run: %w: %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	// Cleanup and diagnostics get their own deadline: they must still run
	// after the load wait's context has expired.
	defer func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rmCancel()
		//nolint:gosec // id is the container id docker itself just printed.
		_ = exec.CommandContext(rmCtx, docker, "rm", "-f", id).Run() //nolint:errcheck // best-effort cleanup
	}()

	logs := func() string {
		logCtx, logCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer logCancel()
		//nolint:gosec // id is the container id docker itself just printed.
		l, _ := exec.CommandContext(logCtx, docker, "logs", id).CombinedOutput() //nolint:errcheck // diagnostics only
		return string(l)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(loadReadyTimeout)
	var addr string
	for time.Now().Before(deadline) {
		//nolint:gosec // id is the container id docker itself just printed.
		state, inspectErr := exec.CommandContext(ctx, docker, "inspect", "-f", "{{.State.Running}} {{.State.ExitCode}}", id).Output()
		if inspectErr == nil && strings.HasPrefix(string(state), "false") {
			return fmt.Errorf("alloy exited during the initial load (running/exit: %s)\n--- alloy log ---\n%s",
				strings.TrimSpace(string(state)), logs())
		}
		if addr == "" {
			//nolint:gosec // id is the container id docker itself just printed.
			if p, portErr := exec.CommandContext(ctx, docker, "port", id, "12345/tcp").Output(); portErr == nil {
				addr = strings.TrimSpace(strings.SplitN(string(p), "\n", 2)[0])
			}
		}
		if addr != "" {
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/-/ready", http.NoBody)
			if reqErr != nil {
				return fmt.Errorf("build ready request: %w", reqErr)
			}
			if resp, getErr := client.Do(req); getErr == nil {
				_ = resp.Body.Close() //nolint:errcheck // body unused
				if resp.StatusCode == http.StatusOK {
					return assertRuntimeHealthy(ctx, client, addr, logs)
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("alloy did not report ready within %s\n--- alloy log ---\n%s", loadReadyTimeout, logs())
}

// runtimeSettleWindow is how long a golden keeps running after /-/ready
// before its log and component health are read. Loading is not collecting:
// prometheus.scrape builds its targets on the scrape manager's first reload
// tick (every 5s), so a refused target only shows up a few seconds after
// the initial load finished. The window covers two ticks.
const runtimeSettleWindow = 12 * time.Second

// runtimeErrorsCapturedOn is the Alloy schema version runtimeErrors' strings
// were taken from. Bumping the Alloy pin (version.AlloySchemaVersion) fails
// every load test until this is bumped with it — after re-running the broken
// configs those strings come from (see runtimeErrors) against the new image
// and confirming each message still appears word for word.
const runtimeErrorsCapturedOn = "alloy-v1.20.1"

// checkRuntimeErrorsPin fails t when the Alloy pin has moved since
// runtimeErrors was last confirmed.
func checkRuntimeErrorsPin(t *testing.T) {
	t.Helper()
	if version.AlloySchemaVersion != runtimeErrorsCapturedOn {
		t.Fatalf("the Alloy pin is %s but wizardtest.runtimeErrors was captured on %s: re-verify those "+
			"log strings against the new image (run a full URL as a scrape __address__ and a glob as a "+
			"loki.source.file __path__, and check each message still appears verbatim), update them if "+
			"Alloy reworded any, then set runtimeErrorsCapturedOn to %s — otherwise the runtime health "+
			"check can pass silently", version.AlloySchemaVersion, runtimeErrorsCapturedOn, version.AlloySchemaVersion)
	}
}

// runtimeErrors are log messages a running Alloy emits for a pipeline that
// loaded — every component built, /-/ready answering 200, the collector
// reporting APPLIED — but collects nothing. Component health stays
// "healthy" through every one of them (Alloy only logs them), so
// AssertGoldensLoadInRealAlloy's ready check and the components API are
// both blind to this class; only the log says so. Both shipped from the
// App Observability wizard (2026-10-08 walkthrough):
//
//   - a full URL in a scrape target's __address__: the scrape manager
//     refuses the target ("Creating target failed … is not a valid
//     hostname") and the job scrapes nothing;
//   - a glob in loki.source.file's __path__: the source stats the literal
//     pattern and gives up ("failed to create source, skipping … stat
//     /var/log/app/*.log: no such file or directory"). Globs belong in
//     local.file_match.
//
// These are literal Alloy log strings, captured from grafana/alloy v1.20.1
// (runtimeErrorsCapturedOn). A newer Alloy that rewords one would turn this
// check into a silent pass, so AssertGoldensLoadInRealAlloyWithEnv refuses
// to run against any other pin until someone re-confirms them.
var runtimeErrors = []string{
	"Creating target failed",
	"is not a valid hostname",
	"failed to create source",
}

// componentHealth is the part of one /api/v0/web/components entry this
// check reads.
type componentHealth struct {
	LocalID string `json:"localID"`
	Health  struct {
		State   string `json:"state"`
		Message string `json:"message"`
	} `json:"health"`
}

// assertRuntimeHealthy lets a golden that has finished its initial load run
// for runtimeSettleWindow, then requires its log to carry none of
// runtimeErrors and every component the components API lists to report
// "healthy". A nil return means the pipeline is, as far as a container with
// no real sources can tell, collecting: its targets were accepted and its
// sources created.
func assertRuntimeHealthy(ctx context.Context, client *http.Client, addr string, logs func() string) error {
	select {
	case <-time.After(runtimeSettleWindow):
	case <-ctx.Done():
		return fmt.Errorf("waiting for the runtime settle window: %w", ctx.Err())
	}
	log := logs()
	var problems []string
	for _, line := range strings.Split(log, "\n") {
		for _, pattern := range runtimeErrors {
			if strings.Contains(line, pattern) {
				problems = append(problems, "runtime error in the log: "+line)
				break
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/v0/web/components", http.NoBody)
	if err != nil {
		return fmt.Errorf("build components request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("read component health: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // read-only body
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("read component health: HTTP %d", resp.StatusCode)
	}
	var components []componentHealth
	if err := json.NewDecoder(resp.Body).Decode(&components); err != nil {
		return fmt.Errorf("decode component health: %w", err)
	}
	if len(components) == 0 {
		return errors.New("the components API listed no components — the health check would pass vacuously")
	}
	for _, c := range components {
		if c.Health.State != "healthy" {
			problems = append(problems, fmt.Sprintf("component %s is %q: %s", c.LocalID, c.Health.State, c.Health.Message))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("alloy loaded the config but it does not run cleanly:\n  %s\n--- alloy log ---\n%s",
			strings.Join(problems, "\n  "), log)
	}
	return nil
}

// secretDataRef matches `<component>.data["key"]`, the only way a golden
// reads a value out of a remote.kubernetes.secret export.
var secretDataRef = regexp.MustCompile(`\.data\["([^"]+)"\]`)

// sysEnvRef matches a golden's read of the collector environment.
var sysEnvRef = regexp.MustCompile(`sys\.env\("([^"]+)"\)`)

// secretKeysReferenced lists every Secret data key any golden reads, so the
// fake API's Secrets carry each one: a key the Secret lacks evaluates to null
// and convert.nonsensitive(null) would fail the load for a reason that only
// exists in this test.
func secretKeysReferenced(contents map[string]string) []string {
	seen := map[string]bool{}
	for _, c := range contents {
		for _, m := range secretDataRef.FindAllStringSubmatch(c, -1) {
			seen[m[1]] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// secretBlockRef matches a remote.kubernetes.secret block as wizards render
// it, capturing its label, namespace and name.
var secretBlockRef = regexp.MustCompile(
	`remote\.kubernetes\.secret "([^"]+)" \{\s*namespace\s*=\s*"([^"]+)"\s*name\s*=\s*"([^"]+)"`)

// checkSecretFixtures fails the test when a SecretData entry names a Secret
// no golden reads, or a key no golden reads from that Secret.
func checkSecretFixtures(t *testing.T, contents map[string]string, fixtures map[string]map[string]string) {
	t.Helper()
	read := map[string]map[string]bool{} // "namespace/name" -> keys read
	for _, c := range contents {
		for _, b := range secretBlockRef.FindAllStringSubmatch(c, -1) {
			ref := b[2] + "/" + b[3]
			if read[ref] == nil {
				read[ref] = map[string]bool{}
			}
			keyRef := regexp.MustCompile(`remote\.kubernetes\.secret\.` + regexp.QuoteMeta(b[1]) + `\.data\["([^"]+)"\]`)
			for _, m := range keyRef.FindAllStringSubmatch(c, -1) {
				read[ref][m[1]] = true
			}
		}
	}
	for ref, keys := range fixtures {
		if read[ref] == nil {
			t.Fatalf("LoadFixtures.SecretData names Secret %s, which no golden reads", ref)
		}
		for k := range keys {
			if !read[ref][k] {
				t.Fatalf("LoadFixtures.SecretData sets key %q of Secret %s, which no golden reads from it", k, ref)
			}
		}
	}
}

type fakeKubeAPI struct {
	port  int
	caPEM []byte
}

var secretPath = regexp.MustCompile(`^/api/v1/namespaces/([^/]+)/secrets/([^/]+)$`)

// startFakeKubeAPI serves the one Kubernetes API call a component makes
// while being built (a Secret GET) over TLS, with a self-signed certificate
// valid for the name the container reaches this process by. It listens on
// every interface because on Linux host.docker.internal resolves to the
// docker bridge address, not loopback.
func startFakeKubeAPI(t *testing.T, keys []string, overrides map[string]map[string]string) fakeKubeAPI {
	t.Helper()
	certPEM, keyPEM := selfSignedCert(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load fake kube API certificate: %v", err)
	}

	data := map[string]string{}
	for _, k := range keys {
		v := "placeholder-" + k
		if strings.Contains(k, "url") {
			v = "https://auth.example.com/oauth2/token"
		}
		data[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if m := secretPath.FindStringSubmatch(r.URL.Path); m != nil && r.Method == http.MethodGet {
			served := make(map[string]string, len(data))
			for k, v := range data {
				served[k] = v
			}
			for k, v := range overrides[m[1]+"/"+m[2]] {
				served[k] = base64.StdEncoding.EncodeToString([]byte(v))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test fake; a write error surfaces as the client's failure
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata":   map[string]any{"namespace": m[1], "name": m[2]},
				"type":       "Opaque",
				"data":       served,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test fake
			"apiVersion": "v1", "kind": "Status", "status": "Failure",
			"reason": "NotFound", "code": http.StatusNotFound,
			"message": "the wizard load test's fake API serves Secrets only",
		})
	})

	// All interfaces: the container reaches this test-only fake through the
	// docker bridge, not loopback.
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen for fake kube API: %v", err)
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	go func() {
		if serveErr := srv.ServeTLS(ln, "", ""); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			t.Logf("fake kube API stopped: %v", serveErr)
		}
	}()
	t.Cleanup(func() { _ = srv.Close() }) //nolint:errcheck // test teardown

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("fake kube API listener is not TCP: %T", ln.Addr())
	}
	return fakeKubeAPI{port: tcpAddr.Port, caPEM: certPEM}
}

// selfSignedCert returns a self-signed certificate (doubling as its own CA)
// for host.docker.internal, the name the container dials.
func selfSignedCert(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "shepherd-wizard-test-kube-api"},
		DNSNames:              []string{"host.docker.internal", "localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// writeServiceAccount lays out the files rest.InClusterConfig reads.
func writeServiceAccount(t *testing.T, caPEM []byte) string {
	t.Helper()
	dir := worldReadableTempDir(t)
	files := map[string][]byte{
		"token":     []byte("shepherd-wizard-test-token"),
		"ca.crt":    caPEM,
		"namespace": []byte("default"),
	}
	for name, b := range files {
		//nolint:gosec // G306: the container's alloy user must read it; a fake token for a fake API.
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// worldReadableTempDir is t.TempDir opened up so a container running as a
// different uid can read what is mounted from it. On macOS the path is
// resolved through /private so Docker Desktop's file sharing recognises it.
func worldReadableTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	//nolint:gosec // G302: test fixture directory, must be traversable by the container's uid.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	return dir
}
