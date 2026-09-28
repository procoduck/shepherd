package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"shepherd/internal/receiver"
)

// The receiver tier (docs/plans/2026-09-28-receiver-tier.md) renders its
// Alloy config at pod start: an init container runs `shepherd receiver
// render` over a file the chart builds from its values, and the Alloy
// container runs the result. Rendering here, rather than templating Alloy in
// Helm, keeps every receiver config behind internal/receiver's Validate —
// the checks that refuse client-asserted tenancy and unbounded listeners.

var receiverCmd = &cobra.Command{
	Use:   "receiver",
	Short: "Receiver-tier tooling",
}

var (
	receiverRenderConfig string
	receiverRenderOut    string
)

var receiverRenderCmd = &cobra.Command{
	Use:   "render",
	Short: "Validate a receiver config file and render it to Alloy config",
	Long: "Reads a receiver config file (YAML), validates it with the same checks the receiver " +
		"package enforces, and writes the rendered Alloy config to --out. Nothing is written when " +
		"validation fails, so a bad config fails the pod at init instead of starting an Alloy " +
		"that forwards untagged data.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		content, err := renderReceiverFile(receiverRenderConfig)
		if err != nil {
			return err
		}
		if err := os.WriteFile(receiverRenderOut, []byte(content), 0o644); err != nil { //nolint:gosec // G306: the rendered config holds no secrets (they are sys.env references) and Alloy runs as another user
			return fmt.Errorf("writing %s: %w", receiverRenderOut, err)
		}
		cmd.PrintErrf("receiver: rendered %s to %s\n", receiverRenderConfig, receiverRenderOut)
		return nil
	},
}

func init() {
	receiverRenderCmd.Flags().StringVar(&receiverRenderConfig, "config", "", "receiver config file (YAML)")
	receiverRenderCmd.Flags().StringVar(&receiverRenderOut, "out", "", "where to write the rendered Alloy config")
	_ = receiverRenderCmd.MarkFlagRequired("config") //nolint:errcheck // only fails for an unknown flag name
	_ = receiverRenderCmd.MarkFlagRequired("out")    //nolint:errcheck // only fails for an unknown flag name
	receiverCmd.AddCommand(receiverRenderCmd)
	rootCmd.AddCommand(receiverCmd)
}

// receiverFile is the on-disk format: snake_case keys the chart writes from
// its values, deliberately separate from receiver.Config's Go field names so
// a rename there cannot silently change what the chart has to produce.
// OTLP/HTTP only: pass-through tenancy refuses gRPC (an HTTPRoute cannot
// front it), and Faro is demand-driven (gateway plan D10).
type receiverFile struct {
	OTLP []receiverFileOTLP `yaml:"otlp"`
}

type receiverFileOTLP struct {
	Label   string                `yaml:"label"`
	Mode    string                `yaml:"mode"`
	HTTP    *receiverFileHTTP     `yaml:"http"`
	Batch   receiverFileBatch     `yaml:"batch"`
	Metrics *receiverFileExporter `yaml:"metrics"`
	Logs    *receiverFileExporter `yaml:"logs"`
	Traces  *receiverFileExporter `yaml:"traces"`
}

type receiverFileHTTP struct {
	ListenAddr         string `yaml:"listen_addr"`
	MaxRequestBodySize string `yaml:"max_request_body_size"`
}

type receiverFileBatch struct {
	Timeout          string `yaml:"timeout"`
	SendBatchSize    int    `yaml:"send_batch_size"`
	SendBatchMaxSize int    `yaml:"send_batch_max_size"`
}

type receiverFileExporter struct {
	Name     string `yaml:"name"`
	Protocol string `yaml:"protocol"`
	// Exactly one of Endpoint (a literal http(s) URL) and EndpointEnv (the
	// name of an environment variable holding it). Never a raw Alloy
	// expression: this file comes from chart values, and an expression
	// there would be config injection.
	Endpoint        string            `yaml:"endpoint"`
	EndpointEnv     string            `yaml:"endpoint_env"`
	Headers         map[string]string `yaml:"headers"`
	SecretHeaderEnv map[string]string `yaml:"secret_header_env"`
}

var envNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// renderReceiverFile reads, strictly decodes, validates and renders a
// receiver config file. It is the whole of `receiver render` except the
// write, so tests can drive it without touching the filesystem output.
func renderReceiverFile(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's --config flag
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var f receiverFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a misspelled or unsupported key (grpc, faro, ...) is an error, not ignored
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s: empty receiver config", path)
		}
		return "", fmt.Errorf("%s: %w", path, err)
	}
	cfg, err := f.toConfig()
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if err := receiver.Validate(cfg); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	content, err := receiver.Render(cfg)
	if err != nil {
		return "", fmt.Errorf("%s: rendering: %w", path, err)
	}
	return content, nil
}

func (f receiverFile) toConfig() (receiver.Config, error) {
	var cfg receiver.Config
	for i, p := range f.OTLP {
		mode, err := tenancyMode(p.Mode)
		if err != nil {
			return receiver.Config{}, fmt.Errorf("otlp[%d]: %w", i, err)
		}
		out := receiver.OTLPPipeline{
			Label: p.Label,
			Mode:  mode,
			Batch: receiver.BatchConfig(p.Batch),
		}
		if p.HTTP != nil {
			out.HTTP = &receiver.OTLPHTTPListener{
				ListenAddr:         p.HTTP.ListenAddr,
				MaxRequestBodySize: p.HTTP.MaxRequestBodySize,
			}
		}
		for _, src := range []struct {
			signal string
			in     *receiverFileExporter
			dst    **receiver.OTLPExporter
		}{
			{"metrics", p.Metrics, &out.Metrics},
			{"logs", p.Logs, &out.Logs},
			{"traces", p.Traces, &out.Traces},
		} {
			if src.in == nil {
				continue
			}
			exp, err := src.in.toExporter()
			if err != nil {
				return receiver.Config{}, fmt.Errorf("otlp[%d].%s: %w", i, src.signal, err)
			}
			*src.dst = exp
		}
		cfg.OTLP = append(cfg.OTLP, out)
	}
	return cfg, nil
}

func tenancyMode(s string) (receiver.OTLPTenancyMode, error) {
	switch s {
	case "pass_through":
		return receiver.TenancyPassThrough, nil
	case "static":
		return receiver.TenancyStatic, nil
	default:
		return "", fmt.Errorf("mode must be %q or %q, got %q", "pass_through", "static", s)
	}
}

func (e receiverFileExporter) toExporter() (*receiver.OTLPExporter, error) {
	var expr string
	switch {
	case e.Endpoint != "" && e.EndpointEnv != "":
		return nil, errors.New("set endpoint or endpoint_env, not both")
	case e.Endpoint != "":
		u, err := url.Parse(e.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("endpoint %q must be an absolute http(s) URL", e.Endpoint)
		}
		// strconv.Quote's escapes (\" \\ and \x.. for control bytes) are all
		// valid in an Alloy string literal; a control byte cannot survive
		// url.Parse above anyway.
		expr = strconv.Quote(e.Endpoint)
	case e.EndpointEnv != "":
		if !envNameRE.MatchString(e.EndpointEnv) {
			return nil, fmt.Errorf("endpoint_env %q must be an environment variable name (%s)", e.EndpointEnv, envNameRE)
		}
		expr = fmt.Sprintf("sys.env(%q)", e.EndpointEnv)
	default:
		return nil, errors.New("one of endpoint or endpoint_env is required")
	}
	for header, env := range e.SecretHeaderEnv {
		if !envNameRE.MatchString(env) {
			return nil, fmt.Errorf("secret_header_env[%q] = %q must be an environment variable name (%s)", header, env, envNameRE)
		}
	}
	return &receiver.OTLPExporter{
		Name:            e.Name,
		Protocol:        receiver.ExporterProtocol(e.Protocol),
		EndpointExpr:    expr,
		Headers:         e.Headers,
		SecretHeaderEnv: e.SecretHeaderEnv,
	}, nil
}
