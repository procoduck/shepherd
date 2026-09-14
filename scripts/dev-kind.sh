#!/usr/bin/env bash
# scripts/dev-kind.sh — the Kubernetes flavour of `make dev`.
#
# Brings up a single-node kind cluster named shepherd-dev running the real
# Helm chart (CloudNativePG, Gateway API + NGINX Gateway Fabric, Calico),
# the existing dev seed, Gitea, the navikt mock OIDC provider, and the three
# dev/*.alloy agents — everything reachable on host port 80 under
# *.localtest.me. See docs/kind-test-environment-plan.md §11 for the design
# and docs/plans/2026-09-14-kind-dev-stack.md for how this was built.
#
# Every kubectl/helm call in this file goes through the kc()/hm() wrappers
# below so the target context is always explicit — several dev/e2e kube
# contexts can coexist on one laptop, and a bare `kubectl apply` here would
# silently hit whatever context happens to be current.
#
# Usage: scripts/dev-kind.sh up|reload|seed|status|down
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

CLUSTER="shepherd-dev"
CTX="kind-shepherd-dev"
NAMESPACE="shepherd-dev"
RELEASE="shepherd"

# shellcheck source=/dev/null
source "${REPO_ROOT}/deploy/versions.env"

# Fail loudly rather than let an empty pin surface as a confusing kind/helm
# error three steps later. KIND_NODE_IMAGE/CALICO_VERSION/NGF_CHART_VERSION
# land in deploy/versions.env in the pins slice (K2) of the kind dev stack
# plan; CNPG_CHART_VERSION/GATEWAY_API_VERSION/GATEWAY_API_CHANNEL/ALLOY_IMAGE
# already live there for the e2e suite and the app images.
for pin in KIND_NODE_IMAGE CALICO_VERSION NGF_CHART_VERSION CNPG_CHART_VERSION \
	GATEWAY_API_VERSION GATEWAY_API_CHANNEL ALLOY_IMAGE; do
	if [ -z "${!pin:-}" ]; then
		echo "dev-kind.sh: ${pin} missing from deploy/versions.env — merge the pins slice" >&2
		exit 1
	fi
done

# kc/hm: the ONLY places kubectl/helm are invoked. Every other function below
# calls these, never kubectl/helm directly (a repocheck spec enforces it).
kc() { kubectl --context "$CTX" "$@"; }
hm() { helm --kube-context "$CTX" "$@"; }

# build_id is the Helm podAnnotations.dev-build value: unique per `up`/
# `reload` so Helm always rolls a new pod template even when the image tag
# never changes (pullPolicy=Never, tag=local — see the C3 constraint in the
# plan). A bare `rollout restart` would race the migrate hook instead.
build_id() {
	printf '%s-%s' "$(git -C "$REPO_ROOT" rev-parse --short HEAD)" "$(date +%s)"
}

usage() {
	echo "usage: $0 up|reload|seed|status|down" >&2
}

# ensure_cluster creates the kind cluster from dev/kind/cluster.yaml unless
# one named $CLUSTER already exists, so `up` is safe to re-run.
ensure_cluster() {
	if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
		echo "dev-kind.sh: cluster ${CLUSTER} already exists, reusing it"
		return
	fi
	kind create cluster --name "$CLUSTER" --image "$KIND_NODE_IMAGE" \
		--config "${REPO_ROOT}/dev/kind/cluster.yaml"
}

# install_calico applies the pinned Calico manifest. Always re-applied
# (idempotent apply) even on an existing cluster, mirroring
# e2e/k8s/main_test.go installCNI.
install_calico() {
	kc apply -f "https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml"
}

# wait_cluster_ready blocks until the CNI is actually serving and CoreDNS can
# answer — see e2e/k8s/main_test.go waitForNodesReady/waitForClusterDNS for
# why both waits exist (nodes go Ready before CoreDNS schedules).
wait_cluster_ready() {
	kc wait --for=condition=Ready nodes --all --timeout=5m
	kc -n kube-system wait --for=condition=Available deploy/coredns --timeout=5m
}

# load_images pushes the locally built images into every kind node so the
# chart's pullPolicy=Never/tag=local pair (dev/kind/values.yaml) can start
# pods without touching a registry.
load_images() {
	kind load docker-image shepherd:local shepherd-simulator:local --name "$CLUSTER"
}

install_gateway_api_crds() {
	kc apply -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/${GATEWAY_API_CHANNEL}-install.yaml"
}

# install_ngf installs NGINX Gateway Fabric with its data-plane Service
# pinned to NodePort 30080 on listener port 80 (dev/kind/cluster.yaml maps
# that node port to host port 80).
install_ngf() {
	hm upgrade --install ngf oci://ghcr.io/nginx/charts/nginx-gateway-fabric \
		--version "$NGF_CHART_VERSION" -n nginx-gateway-system --create-namespace \
		--set nginx.service.type=NodePort \
		--set 'nginx.service.nodePorts[0].port=30080' \
		--set 'nginx.service.nodePorts[0].listenerPort=80' \
		--wait --timeout 4m
}

install_cnpg() {
	hm upgrade --install cnpg oci://ghcr.io/cloudnative-pg/charts/cloudnative-pg \
		--version "$CNPG_CHART_VERSION" -n cnpg-system --create-namespace \
		--wait --timeout 5m
}

ensure_namespace() {
	kc create namespace "$NAMESPACE" --dry-run=client -o yaml | kc apply -f -
}

# find_ngf_service returns the single Service NGF provisioned for the `dev`
# Gateway, found by the label NGF stamps on it. The Service can briefly lag
# Gateway condition=Programmed=True — e2e/k8s/route_conformance_test.go's
# waitProvisionedServiceName documents and retries the same race — so this
# polls for up to ~2m rather than sampling once, and only fails loudly
# (rather than silently picking [0]) once that deadline passes with the
# count still not exactly one.
find_ngf_service() {
	local svc count deadline
	deadline=$((SECONDS + 120))
	while true; do
		svc=$(kc -n "$NAMESPACE" get svc -l gateway.networking.k8s.io/gateway-name=dev \
			-o jsonpath='{.items[*].metadata.name}')
		count=$(wc -w <<<"$svc" | tr -d '[:space:]')
		if [ "$count" == "1" ]; then
			echo "$svc"
			return 0
		fi
		if [ "$SECONDS" -ge "$deadline" ]; then
			echo "dev-kind.sh: expected exactly one Service labelled" \
				"gateway.networking.k8s.io/gateway-name=dev in ${NAMESPACE}, found ${count} (${svc:-none})" >&2
			exit 1
		fi
		sleep 2
	done
}

# verify_nodeport is the "verify at the consumed layer" check for D3: NGF's
# chart values only ASK for NodePort 30080 on port 80, this confirms the
# provisioned Service actually got it, failing loudly with the observed value
# rather than leaving a silently-broken host port mapping.
verify_nodeport() {
	local svc="$1" nodeport
	nodeport=$(kc -n "$NAMESPACE" get svc "$svc" -o jsonpath='{.spec.ports[?(@.port==80)].nodePort}')
	if [ "$nodeport" != "30080" ]; then
		echo "dev-kind.sh: data-plane Service ${svc} port 80 has nodePort '${nodeport}', expected 30080" \
			"— check the nginx.service.nodePorts values passed to install_ngf" >&2
		exit 1
	fi
}

# apply_coredns_rewrite inserts one rewrite line into CoreDNS's `.:53 {`
# block, routing the whole *.localtest.me zone to the NGF data-plane
# Service, then restarts CoreDNS so it picks up the change. Idempotent: a
# second `up` sees the line already present and leaves the ConfigMap alone.
apply_coredns_rewrite() {
	local svc="$1" target corefile rewrite_line new_corefile
	target="${svc}.${NAMESPACE}.svc.cluster.local"
	# Single-quoted so bash leaves the backslash escapes alone; passed to
	# awk via ENVIRON (below), never -v, because awk -v processes escape
	# sequences in its value and would silently turn `\.` into `.`,
	# widening the anchored §1 regex into an unintended any-char match.
	rewrite_line='    rewrite name regex (.*)\.localtest\.me '"${target}"' answer auto'

	corefile=$(kc -n kube-system get cm coredns -o jsonpath='{.data.Corefile}')
	# Match the rewrite rule itself, not the dotted "localtest.me" text —
	# that text also appears (unescaped) in the target hostname, so a
	# looser guard would either miss the escaped rule or false-match on
	# the hostname and skip inserting it in the first place.
	if grep -q 'rewrite name regex .*localtest' <<<"$corefile"; then
		echo "dev-kind.sh: CoreDNS rewrite already present, leaving it"
		return
	fi

	new_corefile=$(REWRITE_LINE="$rewrite_line" awk '{print} /^\.:53 \{/{print ENVIRON["REWRITE_LINE"]}' <<<"$corefile")
	kc -n kube-system create configmap coredns --from-literal="Corefile=${new_corefile}" \
		--dry-run=client -o yaml | kc apply -f -
	kc -n kube-system rollout restart deploy/coredns
	kc -n kube-system rollout status deploy/coredns --timeout=2m
}

# apply_secret_and_configmaps creates the shared Secret from the existing
# dev/shepherd.dev.env (plus the OIDC client secret it deliberately omits —
# see dev/shepherd.dev.env's own OIDC comment) and one ConfigMap per
# dev/*.alloy file. Nothing under dev/ is copied or edited (plan C2).
#
# kubectl rejects --from-env-file combined with --from-literal/--from-file
# in one `create secret` call ("from-env-file cannot be combined with
# from-file or from-literal"), so the client secret is appended to the
# env-file's own lines via process substitution instead of a second flag —
# one env source, not two.
apply_secret_and_configmaps() {
	kc create secret generic shepherd-dev-env \
		--from-env-file=<(cat "${REPO_ROOT}/dev/shepherd.dev.env"; echo "SHEPHERD_OIDC_CLIENT_SECRET=dev-oidc-client-secret") \
		-n "$NAMESPACE" --dry-run=client -o yaml | kc apply -f -

	local name
	for name in alloy-metrics alloy-logs alloy-staging; do
		kc create configmap "$name" \
			--from-file=config.alloy="${REPO_ROOT}/dev/${name}.alloy" \
			-n "$NAMESPACE" --dry-run=client -o yaml | kc apply -f -
	done
}

# apply_workload_manifests applies the manifests the other kind-dev-stack
# slices own (gateway/gitea/oidc/routes/alloy) — this script only references
# them by the names fixed in the plan's contract, it does not create them.
# alloy.yaml carries the literal placeholder __ALLOY_IMAGE__ so the pin is
# never restated outside deploy/versions.env/Renovate.
apply_workload_manifests() {
	kc -n "$NAMESPACE" apply -f "${REPO_ROOT}/dev/kind/gateway.yaml"
	kc -n "$NAMESPACE" wait --for=condition=Programmed gateway/dev --timeout=2m

	local svc
	svc="$(find_ngf_service)"
	verify_nodeport "$svc"
	apply_coredns_rewrite "$svc"

	apply_secret_and_configmaps

	kc -n "$NAMESPACE" apply \
		-f "${REPO_ROOT}/dev/kind/gitea.yaml" \
		-f "${REPO_ROOT}/dev/kind/oidc.yaml" \
		-f "${REPO_ROOT}/dev/kind/routes.yaml"
	sed "s|__ALLOY_IMAGE__|${ALLOY_IMAGE}|g" "${REPO_ROOT}/dev/kind/alloy.yaml" \
		| kc -n "$NAMESPACE" apply -f -

	kc -n "$NAMESPACE" wait --for=condition=Available deploy/oidc deploy/gitea --timeout=3m
}

# bootstrap_gitea_admin creates the Gitea admin user the seed's gitops push
# authenticates as, tolerating "already exists" the same way compose's
# gitea-init service does — see dev/docker-compose.dev.yaml.
bootstrap_gitea_admin() {
	local out
	if out=$(kc -n "$NAMESPACE" exec deploy/gitea -- gitea admin user create \
		--username shepherd-admin --password 'Sh3pherd-Admin-Pass-1' \
		--email admin@shepherd.test --admin --must-change-password=false 2>&1); then
		echo "$out"
	elif grep -qi 'already exists' <<<"$out"; then
		echo "dev-kind.sh: gitea admin user already exists"
	else
		echo "$out" >&2
		echo "dev-kind.sh: gitea admin bootstrap failed" >&2
		exit 1
	fi
}

# install_shepherd runs the real chart with the dev/kind values, stamping a
# fresh dev-build annotation so Helm always rolls the pods after the migrate
# hook (plan C3) — a bare `rollout restart` would skip migrations.
install_shepherd() {
	hm upgrade --install "$RELEASE" "${REPO_ROOT}/deploy/helm/shepherd" \
		-n "$NAMESPACE" -f "${REPO_ROOT}/dev/kind/values.yaml" \
		--set-string "podAnnotations.dev-build=$(build_id)" \
		--wait --timeout 10m
}

print_banner() {
	cat <<BANNER

shepherd dev stack is up:
  Shepherd   http://shepherd.localtest.me        (admin / admin)
  Mock OIDC  http://oidc.localtest.me/default     ("Mock SSO" on the login page)
  Gitea      http://gitea.localtest.me            (shepherd-admin / Sh3pherd-Admin-Pass-1)

Re-run '$0 status' any time; '$0 down' deletes the ${CLUSTER} cluster and all its data.
BANNER
}

cmd_up() {
	ensure_cluster
	install_calico
	wait_cluster_ready
	load_images
	install_gateway_api_crds
	install_ngf
	install_cnpg
	ensure_namespace
	apply_workload_manifests
	bootstrap_gitea_admin
	install_shepherd
	cmd_seed
	print_banner
}

cmd_reload() {
	load_images
	install_shepherd
	kc -n "$NAMESPACE" rollout status deploy/shepherd
}

cmd_seed() {
	kc -n "$NAMESPACE" exec deploy/shepherd -- /usr/local/bin/shepherd dev seed
}

cmd_status() {
	kc -n "$NAMESPACE" get pods,svc,pvc,gateway,httproute
	hm -n "$NAMESPACE" status "$RELEASE"
	kc -n kube-system get cm coredns -o jsonpath='{.data.Corefile}' | grep -i localtest \
		|| echo "dev-kind.sh: no CoreDNS rewrite found — run '$0 up'"
	curl -sf http://shepherd.localtest.me/healthz >/dev/null && echo "shepherd: ok" || echo "shepherd: unreachable"
	curl -sf http://oidc.localtest.me/default/.well-known/openid-configuration >/dev/null && echo "oidc: ok" || echo "oidc: unreachable"
	curl -sf http://gitea.localtest.me/api/healthz >/dev/null && echo "gitea: ok" || echo "gitea: unreachable"
}

cmd_down() {
	kind delete cluster --name "$CLUSTER"
}

main() {
	case "${1:-}" in
	up) cmd_up ;;
	reload) cmd_reload ;;
	seed) cmd_seed ;;
	status) cmd_status ;;
	down) cmd_down ;;
	*)
		usage
		exit 1
		;;
	esac
}

main "$@"
