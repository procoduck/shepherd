package agentapi

import (
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	"shepherd/gen/collector/v1/collectorv1connect"
)

// MaxRequestBytes caps one decoded collector.v1 request message. The largest
// real request is a GetConfig carrying effective_config — Alloy echoes the
// config it loaded, i.e. what Shepherd served — plus local_attributes. Served
// configs run to tens or a few hundred KiB even for a full k8s-monitoring
// import, so 8 MiB leaves an order of magnitude of headroom while keeping a
// single authenticated caller from making the server buffer an unbounded
// message.
const MaxRequestBytes = 8 << 20

// MaxIdentifierBytes caps the instance id and the cluster attribute. Alloy's
// remotecfg id defaults to a UUID and is otherwise operator-set, usually a
// hostname (at most 253 bytes); cluster names are DNS-like too. Both become
// keys in the database and the refusal log, so they are bounded.
const MaxIdentifierBytes = 256

// NewHandler is the collector.v1 Connect handler with the request size limit
// applied; internal/server mounts exactly this. opts carry the auth gate and
// interceptors. sanitizeErrors runs innermost, after opts' interceptors, so
// they (telemetry) see the code the agent is answered with.
func NewHandler(svc *Service, opts ...connect.HandlerOption) (string, http.Handler) {
	all := append([]connect.HandlerOption{connect.WithReadMaxBytes(MaxRequestBytes)}, opts...)
	all = append(all, connect.WithInterceptors(sanitizeErrors(svc.logger)))
	return collectorv1connect.NewCollectorServiceHandler(svc, all...)
}

// requireInstanceID refuses an empty or over-long wire id before any DB work.
func requireInstanceID(id string) error {
	switch {
	case id == "":
		return connect.NewError(connect.CodeInvalidArgument, errors.New("instance id is required"))
	case len(id) > MaxIdentifierBytes:
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("instance id is %d bytes; at most %d allowed", len(id), MaxIdentifierBytes))
	}
	return nil
}

// truncateID bounds an id for use as a map key or log value.
func truncateID(id string) string {
	if len(id) > MaxIdentifierBytes {
		return id[:MaxIdentifierBytes]
	}
	return id
}
