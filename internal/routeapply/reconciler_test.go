package routeapply_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"shepherd/internal/config"
	"shepherd/internal/gateway"
	"shepherd/internal/routeapply"
	"shepherd/internal/store/sqlc"
)

func TestRouteApply(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RouteApply Suite")
}

// fakeStore is tenant_routes in memory, with the same revoke rule as the SQL.
type fakeStore struct {
	rows    []sqlc.TenantRoute
	writes  int
	listErr error
}

func (f *fakeStore) RevokeExpiredTenantRoutes(context.Context) ([]sqlc.TenantRoute, error) {
	var out []sqlc.TenantRoute
	for i := range f.rows {
		r := &f.rows[i]
		if r.Status == "deprecated" && r.ValidUntil.Valid && r.ValidUntil.Time.Before(time.Now()) {
			r.Status = "revoked"
			r.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeStore) ListTenantRoutesForReconcile(context.Context) ([]sqlc.TenantRoute, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]sqlc.TenantRoute(nil), f.rows...), nil
}

func (f *fakeStore) SetTenantRouteApplyStatus(_ context.Context, arg sqlc.SetTenantRouteApplyStatusParams) (sqlc.TenantRoute, error) {
	for i := range f.rows {
		if f.rows[i].ID == arg.ID {
			f.writes++
			f.rows[i].ApplyStatus = arg.ApplyStatus
			f.rows[i].ApplyMessage = arg.ApplyMessage
			if arg.ApplyStatus == routeapply.StatusApplied {
				f.rows[i].AppliedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			}
			return f.rows[i], nil
		}
	}
	return sqlc.TenantRoute{}, errors.New("no such route")
}

func (f *fakeStore) row(id pgtype.UUID) sqlc.TenantRoute {
	for i := range f.rows {
		if f.rows[i].ID == id {
			return f.rows[i]
		}
	}
	Fail("no row " + id.String())
	return sqlc.TenantRoute{}
}

// fakeCluster holds HTTPRoutes by name and plays the gateway controller:
// a route whose parent is the Gateway "closed" is refused, any other attaches.
type fakeCluster struct {
	routes   map[string]*gatewayv1.HTTPRoute
	applies  int
	crdErr   error
	listErr  error
	deletes  []string
	deleteOK bool
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{routes: map[string]*gatewayv1.HTTPRoute{}, deleteOK: true}
}

func (c *fakeCluster) Apply(_ context.Context, route *gatewayv1.HTTPRoute) error {
	c.applies++
	cp := route.DeepCopy()
	parent := route.Spec.ParentRefs[0]
	accepted := metav1.ConditionTrue
	reason := "Accepted"
	if parent.Name == "closed" {
		accepted, reason = metav1.ConditionFalse, "NotAllowedByListeners"
	}
	cp.Status.Parents = []gatewayv1.RouteParentStatus{{
		ParentRef:  parent,
		Conditions: []metav1.Condition{{Type: "Accepted", Status: accepted, Reason: reason}},
	}}
	c.routes[route.Name] = cp
	return nil
}

func (c *fakeCluster) Get(_ context.Context, ns, name string) (*gatewayv1.HTTPRoute, error) {
	r, ok := c.routes[name]
	if !ok {
		return nil, fmt.Errorf("%s/%s: %w", ns, name, gateway.ErrNotFound)
	}
	return r.DeepCopy(), nil
}

func (c *fakeCluster) HTTPRouteCRDAnnotations(context.Context) (map[string]string, error) {
	if c.crdErr != nil {
		return nil, c.crdErr
	}
	return map[string]string{
		gateway.BundleVersionAnnotation: "v1.4.1",
		gateway.ChannelAnnotation:       gateway.RequiredChannel,
	}, nil
}

func (c *fakeCluster) Delete(_ context.Context, _, name string) error {
	if !c.deleteOK {
		return errors.New("forbidden")
	}
	c.deletes = append(c.deletes, name)
	delete(c.routes, name)
	return nil
}

func (c *fakeCluster) ListManaged(context.Context, string) ([]string, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	var names []string
	for name, r := range c.routes {
		if _, ok := r.Labels[routeapply.RouteIDLabel]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

var _ = Describe("Reconciler", func() {
	var (
		ctx     context.Context
		st      *fakeStore
		cl      *fakeCluster
		rec     *routeapply.Reconciler
		seq     byte
		cfg     config.RouteApplyConfig
		clock   time.Time
		newUUID func() pgtype.UUID
	)

	BeforeEach(func() {
		ctx = context.Background()
		st = &fakeStore{}
		cl = newFakeCluster()
		cfg = config.RouteApplyConfig{
			Enabled: true, Namespace: "shepherd", BackendService: "shepherd-receiver", BackendPort: 4318,
			Interval: time.Minute, AttachTimeout: time.Second,
		}
		seq = 0
		newUUID = func() pgtype.UUID {
			seq++
			return pgtype.UUID{Bytes: [16]byte{15: seq}, Valid: true}
		}
		clock = time.Now()
		rec = routeapply.New(st, cl, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		routeapply.SetClock(rec, func() time.Time { return clock })
	})

	addRow := func(kind, status, gatewayName string, validUntil time.Time) pgtype.UUID {
		id := newUUID()
		row := sqlc.TenantRoute{
			ID: id, TenantID: "acme", Kind: kind, Segment: fmt.Sprintf("acme-%d", seq), Status: status,
			GatewayMode: "operator", GatewayName: gatewayName, GatewayNamespace: "gateways",
			ApplyStatus: routeapply.StatusPending,
		}
		if !validUntil.IsZero() {
			row.ValidUntil = pgtype.Timestamptz{Time: validUntil, Valid: true}
		}
		st.rows = append(st.rows, row)
		return id
	}

	It("applies an active route, verified attached, pointing at the receiver", func() {
		id := addRow("otlp", "active", "edge", time.Time{})
		rec.Reconcile(ctx)

		name := routeapply.ObjectName(id.String())
		route, ok := cl.routes[name]
		Expect(ok).To(BeTrue(), "the HTTPRoute is named from the route id")
		Expect(route.Namespace).To(Equal("shepherd"))
		Expect(route.Labels).To(HaveKeyWithValue(routeapply.RouteIDLabel, id.String()))
		Expect(route.Labels).To(HaveKeyWithValue(gateway.ManagedByLabel, "shepherd"))
		Expect(string(route.Spec.ParentRefs[0].Name)).To(Equal("edge"))
		backend := route.Spec.Rules[0].BackendRefs[0]
		Expect(string(backend.Name)).To(Equal("shepherd-receiver"))
		Expect(*backend.Port).To(BeEquivalentTo(4318))

		row := st.row(id)
		Expect(row.ApplyStatus).To(Equal(routeapply.StatusApplied))
		Expect(row.ApplyMessage).To(BeEmpty())
		Expect(row.AppliedAt.Valid).To(BeTrue())
	})

	It("keeps a deprecated route inside its rotation overlap", func() {
		id := addRow("otlp", "deprecated", "edge", time.Now().Add(time.Hour))
		rec.Reconcile(ctx)
		Expect(cl.routes).To(HaveKey(routeapply.ObjectName(id.String())))
		Expect(st.row(id).ApplyStatus).To(Equal(routeapply.StatusApplied))
	})

	It("revokes a deprecated route whose overlap has ended and removes its HTTPRoute", func() {
		id := addRow("otlp", "deprecated", "edge", time.Now().Add(time.Hour))
		rec.Reconcile(ctx)
		Expect(cl.routes).To(HaveKey(routeapply.ObjectName(id.String())))

		st.rows[0].ValidUntil.Time = time.Now().Add(-time.Second)
		rec.Reconcile(ctx)
		Expect(cl.routes).NotTo(HaveKey(routeapply.ObjectName(id.String())))
		row := st.row(id)
		Expect(row.Status).To(Equal("revoked"))
		Expect(row.ApplyStatus).To(Equal(routeapply.StatusRemoved))
	})

	It("removes a revoked route's HTTPRoute, and does not rewrite the row once removed", func() {
		id := addRow("otlp", "active", "edge", time.Time{})
		rec.Reconcile(ctx)
		st.rows[0].Status = "revoked"

		rec.Reconcile(ctx)
		Expect(cl.deletes).To(ConsistOf(routeapply.ObjectName(id.String())))
		Expect(st.row(id).ApplyStatus).To(Equal(routeapply.StatusRemoved))

		writes := st.writes
		rec.Reconcile(ctx)
		Expect(st.writes).To(Equal(writes), "an unchanged non-applied outcome is not rewritten")
	})

	It("records a gateway refusal with its reason, and backs off before retrying", func() {
		id := addRow("otlp", "active", "closed", time.Time{})
		rec.Reconcile(ctx)
		row := st.row(id)
		Expect(row.ApplyStatus).To(Equal(routeapply.StatusRefused))
		Expect(row.ApplyMessage).To(ContainSubstring("NotAllowedByListeners"))
		Expect(row.AppliedAt.Valid).To(BeFalse())

		applies := cl.applies
		rec.Reconcile(ctx)
		Expect(cl.applies).To(Equal(applies), "a refused route waits out its backoff")

		clock = clock.Add(2 * time.Minute)
		rec.Reconcile(ctx)
		Expect(cl.applies).To(Equal(applies+1), "and is retried after it")
	})

	It("records anything else as an error, then applies once the cause is gone", func() {
		id := addRow("otlp", "active", "edge", time.Time{})
		cl.crdErr = errors.New("customresourcedefinitions is forbidden")
		rec.Reconcile(ctx)
		row := st.row(id)
		Expect(row.ApplyStatus).To(Equal(routeapply.StatusError))
		Expect(row.ApplyMessage).To(ContainSubstring("forbidden"))

		cl.crdErr = nil
		clock = clock.Add(2 * time.Minute)
		rec.Reconcile(ctx)
		Expect(st.row(id).ApplyStatus).To(Equal(routeapply.StatusApplied))
		Expect(st.row(id).ApplyMessage).To(BeEmpty())
	})

	It("marks Faro routes not applicable and never applies them", func() {
		id := addRow("faro", "active", "edge", time.Time{})
		rec.Reconcile(ctx)
		Expect(cl.applies).To(BeZero())
		Expect(st.row(id).ApplyStatus).To(Equal(routeapply.StatusNotApplicable))
	})

	It("deletes managed HTTPRoutes no row wants, and leaves unlabelled ones alone", func() {
		orphan := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{
			Name: "shepherd-tenant-route-gone", Namespace: "shepherd",
			Labels: map[string]string{routeapply.RouteIDLabel: "gone", gateway.ManagedByLabel: "shepherd"},
		}}
		handMade := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "acme-otlp", Namespace: "shepherd"}}
		cl.routes[orphan.Name] = orphan
		cl.routes[handMade.Name] = handMade

		rec.Reconcile(ctx)
		Expect(cl.routes).NotTo(HaveKey(orphan.Name))
		Expect(cl.routes).To(HaveKey(handMade.Name))
	})

	It("does not mark a revoked route removed when its HTTPRoute could not be deleted", func() {
		id := addRow("otlp", "active", "edge", time.Time{})
		rec.Reconcile(ctx)
		st.rows[0].Status = "revoked"
		cl.deleteOK = false

		rec.Reconcile(ctx)
		Expect(st.row(id).ApplyStatus).To(Equal(routeapply.StatusApplied), "still in the cluster, still routing")

		cl.deleteOK = true
		cl.listErr = errors.New("httproutes is forbidden")
		rec.Reconcile(ctx)
		Expect(st.row(id).ApplyStatus).To(Equal(routeapply.StatusApplied), "unknown is not removed")
	})

	It("touches nothing in the cluster when the routes cannot be read", func() {
		addRow("otlp", "active", "edge", time.Time{})
		st.listErr = errors.New("db down")
		rec.Reconcile(ctx)
		Expect(cl.applies).To(BeZero())
		Expect(cl.deletes).To(BeEmpty())
	})
})
