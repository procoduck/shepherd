package mgmtapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/store"
)

// OrgsHandler handles org-scoped routes. Every method is a thin REST shim
// delegating to the corresponding shepherd.mgmt.v1 service in-process (see
// rpc_fleet.go, rpc_destination.go, rpc_me.go, and
// docs/archive/api-contract-design.md, "Server wiring"). No business logic lives
// here.
type OrgsHandler struct {
	store        *store.Store
	logger       *slog.Logger
	fleet        *FleetService
	destinations *DestinationService
	me           *MeService
}

// NewOrgsHandler creates an organization handler.
func NewOrgsHandler(st *store.Store, logger *slog.Logger) *OrgsHandler {
	return &OrgsHandler{
		store:        st,
		logger:       logger,
		fleet:        NewFleetService(st, logger),
		destinations: NewDestinationService(st, logger),
		me:           NewMeService(st, logger),
	}
}

// fleetListMarshalOpts renders Collector/CollectorInstance messages with
// zero-value scalar fields omitted (no EmitUnpopulated), matching the
// `omitempty` behavior of the hand-written collectorResponse /
// collectorInstanceResponse structs these shims replaced — unlike
// shim.go's MarshalOpts (used elsewhere in this package), which always
// emits every field. See collectors_metadata_test.go for the exact
// presence/absence assertions this preserves.
var fleetListMarshalOpts = protojson.MarshalOptions{UseProtoNames: true} //nolint:gochecknoglobals // shared, read-only marshal config

// writeFleetJSON renders a proto message as the shim's HTTP response body,
// using WriteConnectError's status-mapping conventions for the (practically
// unreachable — msg is always freshly built by the FleetService call above)
// marshal-failure path.
func writeFleetJSON(w http.ResponseWriter, status int, msg proto.Message, opts protojson.MarshalOptions) {
	body, err := opts.Marshal(msg)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to encode response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body) //nolint:errcheck // response headers have already been sent
}

// Me returns the current actor and organizations (thin REST shim over
// MeService.GetMe — see rpc_me.go for the business logic). Cache-Control is
// set here to match the legacy handler's header exactly; MeService.GetMe
// also sets it on the Connect response for direct Connect callers.
func (h *OrgsHandler) Me(w http.ResponseWriter, r *http.Request) {
	// Never cache /api/me — authentication state must always be fresh.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	resp, err := h.me.GetMe(r.Context(), connect.NewRequest(&mgmtv1.GetMeRequest{}))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusOK, resp.Msg)
}

// ListCollectors returns collectors for the organization (thin REST shim
// over FleetService.ListCollectors — see rpc_fleet.go for the business
// logic, including the app-admin cross-org listing behavior).
func (h *OrgsHandler) ListCollectors(w http.ResponseWriter, r *http.Request) {
	req := connect.NewRequest(&mgmtv1.ListCollectorsRequest{OrgId: chi.URLParam(r, "org")})
	resp, err := h.fleet.ListCollectors(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, resp.Msg, fleetListMarshalOpts)
}

// GetCollector returns a collector, including its live instances (thin REST
// shim over FleetService.getCollector — the same implementation
// GetCollector's Connect method uses, called directly here rather than
// through the Connect method so this handler also gets back the untouched
// local_attributes bytes it needs for byte-compatible rendering; see
// collectorLocalAttrsRaw and writeCollectorJSON).
func (h *OrgsHandler) GetCollector(w http.ResponseWriter, r *http.Request) {
	msg, raw, err := h.fleet.getCollector(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeCollectorJSON(w, http.StatusOK, msg, raw)
}

// writeCollectorJSON renders a Collector as legacy-compatible JSON
// (fleetListMarshalOpts — omitempty scalars, matching collectorResponse),
// then substitutes the local_attributes bytes protojson rendered (from the
// LocalAttributes structpb.Struct, which is neither key-ordered nor
// number-typed the way the stored JSON is) with the untouched bytes raw
// carries — the collector-level rollup and each instance's own value —
// reproducing the legacy handler's direct json.RawMessage passthrough
// byte-for-byte (verified: key order and number formatting, e.g. `1.5` vs
// `1.500000`, both survive that a Struct round-trip would not).
func writeCollectorJSON(w http.ResponseWriter, status int, msg *mgmtv1.Collector, raw collectorLocalAttrsRaw) {
	b, err := fleetListMarshalOpts.Marshal(msg)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to encode response")
		return
	}
	b, err = spliceLocalAttributes(b, raw)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to render response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b) //nolint:errcheck,gosec // response status/headers already committed; G705: b is application/json, not HTML, and the local_attributes bytes it embeds get the exact unescaped passthrough legacy's json.RawMessage field always gave them — not a new exposure
}

// spliceLocalAttributes replaces the "local_attributes" member of a
// protojson-marshaled Collector object — and of each element of its
// "instances" array — with the corresponding raw bytes in raw, when
// present. Members not named "local_attributes"/"instances" pass through
// untouched.
func spliceLocalAttributes(body []byte, raw collectorLocalAttrsRaw) ([]byte, error) {
	entries, err := decodeJSONObject(body)
	if err != nil {
		return nil, err
	}
	for i, e := range entries {
		switch e.key {
		case "local_attributes":
			if raw.collector != nil {
				entries[i].raw = raw.collector
			}
		case "instances":
			items, err := decodeJSONArray(e.raw)
			if err != nil {
				return nil, err
			}
			for j := range items {
				if j >= len(raw.instances) || raw.instances[j] == nil {
					continue
				}
				items[j], err = spliceObjectField(items[j], "local_attributes", raw.instances[j])
				if err != nil {
					return nil, err
				}
			}
			entries[i].raw = encodeJSONArray(items)
		}
	}
	return encodeJSONObject(entries), nil
}

// spliceObjectField replaces obj's key member with value, if present.
func spliceObjectField(obj []byte, key string, value json.RawMessage) ([]byte, error) {
	entries, err := decodeJSONObject(obj)
	if err != nil {
		return nil, err
	}
	for i, e := range entries {
		if e.key == key {
			entries[i].raw = value
		}
	}
	return encodeJSONObject(entries), nil
}

// ServedConfig returns the served configuration for a collector (thin REST
// shim over FleetService.GetServedConfig).
func (h *OrgsHandler) ServedConfig(w http.ResponseWriter, r *http.Request) {
	req := connect.NewRequest(&mgmtv1.GetServedConfigRequest{
		OrgId: chi.URLParam(r, "org"),
		Id:    chi.URLParam(r, "id"),
	})
	resp, err := h.fleet.GetServedConfig(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	// computed_at is unset on a cache miss (GetServedConfigResponse{}, all
	// zero) — legacy ServedConfig renders {"content":"","hash":""} in that
	// case, with no "computed_at" key at all; content/hash are always
	// emitted (never omitempty in the legacy shape) even when empty.
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, "computed_at")
}

// ListAssignments returns the group assignments granting access to a
// collector (thin REST shim over FleetService.ListAssignments).
func (h *OrgsHandler) ListAssignments(w http.ResponseWriter, r *http.Request) {
	req := connect.NewRequest(&mgmtv1.ListAssignmentsRequest{
		OrgId:       chi.URLParam(r, "org"),
		CollectorId: chi.URLParam(r, "id"),
	})
	resp, err := h.fleet.ListAssignments(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, resp.Msg, fleetListMarshalOpts)
}

// CreateAssignment creates a group assignment (thin REST shim over
// FleetService.CreateAssignment).
func (h *OrgsHandler) CreateAssignment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GroupID          string `json:"group_id"`
		GroupDisplayName string `json:"group_display_name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	req := connect.NewRequest(&mgmtv1.CreateAssignmentRequest{
		OrgId:            chi.URLParam(r, "org"),
		CollectorId:      chi.URLParam(r, "id"),
		GroupId:          body.GroupID,
		GroupDisplayName: body.GroupDisplayName,
	})
	resp, err := h.fleet.CreateAssignment(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeFleetJSON(w, http.StatusCreated, resp.Msg, MarshalOpts)
}

// DeleteAssignment deletes a group assignment (thin REST shim over
// FleetService.DeleteAssignment). The legacy route returns 204 No Content
// with no body on success — DeleteAssignmentResponse carries no fields, so
// there is nothing to render either way.
func (h *OrgsHandler) DeleteAssignment(w http.ResponseWriter, r *http.Request) {
	req := connect.NewRequest(&mgmtv1.DeleteAssignmentRequest{
		OrgId:       chi.URLParam(r, "org"),
		CollectorId: chi.URLParam(r, "id"),
		GroupId:     chi.URLParam(r, "group_id"),
	})
	if _, err := h.fleet.DeleteAssignment(r.Context(), req); err != nil {
		WriteConnectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListAttributes returns organization attributes (thin REST shim over
// FleetService.ListAttributes). The legacy route returns the attributes map
// as a bare JSON object rather than wrapped in the ListAttributesResponse
// envelope, so the shim marshals resp.Msg.Attributes directly — see the
// ListAttributesResponse doc comment in proto/shepherd/mgmt/v1/fleet.proto.
func (h *OrgsHandler) ListAttributes(w http.ResponseWriter, r *http.Request) {
	req := connect.NewRequest(&mgmtv1.ListAttributesRequest{OrgId: chi.URLParam(r, "org")})
	resp, err := h.fleet.ListAttributes(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, resp.Msg.GetAttributes(), fleetListMarshalOpts)
}

// Destination handlers (thin REST shims over DestinationService — see
// rpc_destination.go for the business logic).

// destinationRequest is the legacy wire shape for create/update destination
// bodies.
type destinationRequest struct {
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	URL             string          `json:"url"`
	TenantID        string          `json:"tenant_id"`
	SecretName      string          `json:"secret_name"`
	SecretNamespace string          `json:"secret_namespace"`
	AuthMode        string          `json:"auth_mode"`
	Extra           json.RawMessage `json:"extra"`
}

// destinationExtraStruct converts a raw JSON extra payload (if any) to a
// *structpb.Struct for the proto request. An absent/empty payload, or one
// that isn't a JSON object, yields a nil Struct — DestinationService's
// destinationExtraJSON then substitutes the "{}" default, matching the
// legacy handler's `if len(extra) == 0` behavior. Mirrors pipelines.go's
// wizardStateStruct.
func destinationExtraStruct(raw json.RawMessage) *structpb.Struct {
	if len(raw) == 0 {
		return nil
	}
	var s structpb.Struct
	if err := protojson.Unmarshal(raw, &s); err != nil {
		return nil
	}
	return &s
}

// ListDestinations returns destinations for the organization (thin REST
// shim over DestinationService.ListDestinations).
func (h *OrgsHandler) ListDestinations(w http.ResponseWriter, r *http.Request) {
	req := connect.NewRequest(&mgmtv1.ListDestinationsRequest{OrgId: chi.URLParam(r, "org")})
	resp, err := h.destinations.ListDestinations(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	// MarshalOpts (not fleetListMarshalOpts): legacy destinationResponse
	// always emits every field except extra (its one omitempty field, never
	// actually empty since the column defaults to "{}"). EmitUnpopulated
	// also keeps "items"/"total" present when the list is empty, matching
	// the legacy listResponse envelope exactly.
	writeFleetJSON(w, http.StatusOK, resp.Msg, MarshalOpts)
}

// CreateDestination creates a destination (thin REST shim over
// DestinationService.CreateDestination).
func (h *OrgsHandler) CreateDestination(w http.ResponseWriter, r *http.Request) {
	var body destinationRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := connect.NewRequest(&mgmtv1.CreateDestinationRequest{
		OrgId: chi.URLParam(r, "org"), Name: body.Name, Type: body.Type, Url: body.URL,
		TenantId: body.TenantID, SecretName: body.SecretName, SecretNamespace: body.SecretNamespace,
		AuthMode: body.AuthMode, Extra: destinationExtraStruct(body.Extra),
	})
	resp, err := h.destinations.CreateDestination(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeFleetJSON(w, http.StatusCreated, resp.Msg, MarshalOpts)
}

// GetDestination returns a destination (thin REST shim over
// DestinationService.GetDestination).
func (h *OrgsHandler) GetDestination(w http.ResponseWriter, r *http.Request) {
	req := connect.NewRequest(&mgmtv1.GetDestinationRequest{
		OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id"),
	})
	resp, err := h.destinations.GetDestination(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, resp.Msg, MarshalOpts)
}

// UpdateDestination updates a destination (thin REST shim over
// DestinationService.UpdateDestination).
func (h *OrgsHandler) UpdateDestination(w http.ResponseWriter, r *http.Request) {
	var body destinationRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := connect.NewRequest(&mgmtv1.UpdateDestinationRequest{
		OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id"),
		Name: body.Name, Type: body.Type, Url: body.URL,
		TenantId: body.TenantID, SecretName: body.SecretName, SecretNamespace: body.SecretNamespace,
		AuthMode: body.AuthMode, Extra: destinationExtraStruct(body.Extra),
	})
	resp, err := h.destinations.UpdateDestination(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, resp.Msg, MarshalOpts)
}

// DeleteDestination deletes a destination (thin REST shim over
// DestinationService.DeleteDestination). A destinationInUseError renders the
// legacy {"error":{"code":"in_use",...}} envelope exactly (see
// rpc_destination.go's destinationInUseError doc comment); every other
// error uses the generic connect error shim.
func (h *OrgsHandler) DeleteDestination(w http.ResponseWriter, r *http.Request) {
	req := connect.NewRequest(&mgmtv1.DeleteDestinationRequest{
		OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id"),
	})
	if _, err := h.destinations.DeleteDestination(r.Context(), req); err != nil {
		var inUse *destinationInUseError
		if errors.As(err, &inUse) {
			respondError(w, http.StatusConflict, "in_use", inUse.Error())
			return
		}
		WriteConnectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
