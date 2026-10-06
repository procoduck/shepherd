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
	AssertGoldensLoadInRealAlloyWithEnv(t, testdataDir, nil)
}

// AssertGoldensLoadInRealAlloyWithEnv is AssertGoldensLoadInRealAlloy for
// goldens that read the collector's environment with sys.env(...): env gives
// each variable a representative value, set in the container. On a real
// collector the operator sets them; an exporter that parses its DSN while
// being built would otherwise fail on an empty string, a refusal that only
// exists in the test. Every variable a golden reads must be in env — a
// missing one fails the test rather than quietly loading an empty value.
func AssertGoldensLoadInRealAlloyWithEnv(t *testing.T, testdataDir string, env map[string]string) {
	t.Helper()
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
					"pass a representative one via AssertGoldensLoadInRealAlloyWithEnv", g, m[1])
			}
		}
	}
	envArgs := make([]string, 0, 2*len(env))
	for k, v := range env {
		envArgs = append(envArgs, "-e", k+"="+v)
	}

	api := startFakeKubeAPI(t, secretKeysReferenced(contents))
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
					return nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("alloy did not report ready within %s\n--- alloy log ---\n%s", loadReadyTimeout, logs())
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
func startFakeKubeAPI(t *testing.T, keys []string) fakeKubeAPI {
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
			_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test fake; a write error surfaces as the client's failure
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata":   map[string]any{"namespace": m[1], "name": m[2]},
				"type":       "Opaque",
				"data":       data,
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
