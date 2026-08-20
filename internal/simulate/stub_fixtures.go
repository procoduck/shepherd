package simulate

import "sort"

// The stub fixture library backing §6.4's "the fixture library from 6.3a, so
// relabel chains behave identically". The k8s entries are not copies of the S2
// fixtures — they return the very same functions S2's relabel and log traces
// use, so an S2 trace and an S3 sandbox run cannot disagree about what a
// discovered target or a log line looks like. A copy would rot; a shared call
// cannot.
//
// No entry carries a real address. __address__ is a placeholder the transform
// overwrites from HarnessEndpoints.TargetAddress at transform time, which is
// what keeps deployment-specific hosts out of committed data.
const targetAddressPlaceholder = "127.0.0.1:0"

func target(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	out["__address__"] = targetAddressPlaceholder
	for k, v := range labels {
		out[k] = v
	}
	return out
}

// stubTargetFixtures maps a fixture name to the meta-label sets the discovery
// mechanism it stands in for would have produced. The label names are the real
// ones Prometheus service discovery emits, because relabel rules downstream
// match on them and a stub with invented label names would make those rules
// behave differently in S3 than in production — the fidelity lie §6.5 refuses.
var stubTargetFixtures = map[string]func() []map[string]string{
	"k8s-pod-targets": BuiltinRelabelTargets,
	"aws-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_aws_region": "us-east-1", "__meta_aws_service": "ec2", "__meta_aws_instance_id": "i-0a1b2c3d"}),
			target(map[string]string{"__meta_aws_region": "us-east-1", "__meta_aws_service": "ecs", "__meta_aws_instance_id": "i-4e5f6a7b"}),
		}
	},
	"ec2-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_ec2_instance_id": "i-0a1b2c3d", "__meta_ec2_availability_zone": "us-east-1a", "__meta_ec2_instance_state": "running", "__meta_ec2_tag_Name": "api-server"}),
			target(map[string]string{"__meta_ec2_instance_id": "i-4e5f6a7b", "__meta_ec2_availability_zone": "us-east-1b", "__meta_ec2_instance_state": "running", "__meta_ec2_tag_Name": "worker"}),
		}
	},
	"lightsail-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_lightsail_instance_name": "api-server", "__meta_lightsail_availability_zone": "us-east-1a", "__meta_lightsail_instance_state": "running"}),
		}
	},
	"azure-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_azure_machine_name": "api-server", "__meta_azure_machine_location": "westeurope", "__meta_azure_machine_resource_group": "monitoring", "__meta_azure_machine_tag_app": "api-server"}),
			target(map[string]string{"__meta_azure_machine_name": "worker", "__meta_azure_machine_location": "westeurope", "__meta_azure_machine_resource_group": "monitoring", "__meta_azure_machine_tag_app": "worker"}),
		}
	},
	"gce-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_gce_instance_name": "api-server", "__meta_gce_zone": "projects/example/zones/us-central1-a", "__meta_gce_project": "example", "__meta_gce_label_app": "api-server"}),
		}
	},
	"digitalocean-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_digitalocean_droplet_id": "1001", "__meta_digitalocean_region": "ams3", "__meta_digitalocean_tags": ",monitoring,"}),
		}
	},
	"hetzner-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_hetzner_server_name": "api-server", "__meta_hetzner_datacenter": "fsn1-dc14", "__meta_hetzner_role": "hcloud"}),
		}
	},
	"linode-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_linode_instance_label": "api-server", "__meta_linode_region": "eu-central", "__meta_linode_status": "running"}),
		}
	},
	"scaleway-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_scaleway_instance_name": "api-server", "__meta_scaleway_instance_zone": "fr-par-1", "__meta_scaleway_instance_status": "running"}),
		}
	},
	"ionos-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_ionos_server_name": "api-server", "__meta_ionos_server_availability_zone": "AUTO", "__meta_ionos_server_state": "AVAILABLE"}),
		}
	},
	"ovhcloud-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_ovhcloud_dedicated_server_name": "api-server", "__meta_ovhcloud_dedicated_server_datacenter": "gra1", "__meta_ovhcloud_dedicated_server_state": "ok"}),
		}
	},
	"openstack-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_openstack_instance_name": "api-server", "__meta_openstack_instance_status": "ACTIVE", "__meta_openstack_project_id": "0123456789abcdef"}),
		}
	},
	"triton-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_triton_machine_alias": "api-server", "__meta_triton_machine_brand": "lx", "__meta_triton_server_id": "srv-1"}),
		}
	},
	"consul-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_consul_service": "api-server", "__meta_consul_dc": "dc1", "__meta_consul_node": "node-1", "__meta_consul_tags": ",metrics,"}),
			target(map[string]string{"__meta_consul_service": "worker", "__meta_consul_dc": "dc1", "__meta_consul_node": "node-2", "__meta_consul_tags": ",metrics,"}),
		}
	},
	"nomad-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_nomad_service": "api-server", "__meta_nomad_dc": "dc1", "__meta_nomad_namespace": "default", "__meta_nomad_node_id": "node-1"}),
		}
	},
	"kuma-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_kuma_mesh": "default", "__meta_kuma_service": "api-server", "__meta_kuma_dataplane": "api-server-1"}),
		}
	},
	"marathon-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_marathon_app": "/api-server", "__meta_marathon_task": "api-server.1", "__meta_marathon_app_label_metrics": "true"}),
		}
	},
	"eureka-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_eureka_app_name": "API-SERVER", "__meta_eureka_app_instance_status": "UP", "__meta_eureka_app_instance_hostname": "api-server.example.com"}),
		}
	},
	"serverset-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_serverset_path": "/services/api-server/member_0000000001", "__meta_serverset_status": "ALIVE", "__meta_serverset_shard": "0"}),
		}
	},
	"nerve-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_nerve_path": "/nerve/services/api-server", "__meta_nerve_endpoint_host": "api-server.example.com", "__meta_nerve_endpoint_name": "api-server"}),
		}
	},
	"uyuni-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_uyuni_minion_hostname": "api-server.example.com", "__meta_uyuni_exporter": "node", "__meta_uyuni_groups": "monitoring"}),
		}
	},
	"puppet-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_puppetdb_certname": "api-server.example.com", "__meta_puppetdb_environment": "production", "__meta_puppetdb_resource_title": "Node_exporter"}),
		}
	},
	"docker-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_docker_container_name": "/api-server", "__meta_docker_container_label_app": "api-server", "__meta_docker_network_name": "monitoring"}),
			target(map[string]string{"__meta_docker_container_name": "/worker", "__meta_docker_container_label_app": "worker", "__meta_docker_network_name": "monitoring"}),
		}
	},
	"dns-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_dns_name": "api-server.example.com", "__meta_dns_srv_record_target": "api-server.example.com.", "__meta_dns_srv_record_port": "8080"}),
		}
	},
	"file-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_filepath": "/etc/alloy/targets/api-server.yaml", "job": "api-server"}),
		}
	},
	"http-targets": func() []map[string]string {
		return []map[string]string{
			target(map[string]string{"__meta_url": "https://sd.example.com/targets", "job": "api-server"}),
		}
	},
}

// stubLogFixtures maps a log fixture name to the lines the harness's synthetic
// emitter writes for it. k8s-pod-logs reuses the S2 fixture set for the same
// reason the target fixtures do.
var stubLogFixtures = map[string]func() []string{
	"k8s-pod-logs": BuiltinLogLines,
	"docker-logs": func() []string {
		return []string{
			`{"level":"info","ts":"2026-08-18T10:02:00Z","msg":"container started","container":"api-server"}`,
			`level=warn ts=2026-08-18T10:02:05Z msg="restart backoff" container=worker attempts=3`,
		}
	},
	"file-logs": func() []string {
		return []string{
			`{"level":"error","ts":"2026-08-18T10:03:00Z","msg":"disk write failed","path":"/var/log/app.log"}`,
			`level=info ts=2026-08-18T10:03:01Z msg="log rotated" path=/var/log/app.log`,
		}
	},
}

// StubTargets returns the synthetic target set for a fixture name, and whether
// the name is known. Every entry's __address__ is the placeholder; the
// transform substitutes the harness address.
func StubTargets(fixture string) ([]map[string]string, bool) {
	build, ok := stubTargetFixtures[fixture]
	if !ok {
		return nil, false
	}
	return build(), true
}

// StubLogLines returns the synthetic log lines for a fixture name, and whether
// the name is known.
func StubLogLines(fixture string) ([]string, bool) {
	build, ok := stubLogFixtures[fixture]
	if !ok {
		return nil, false
	}
	return build(), true
}

// StubFixtureNames returns every fixture name the library can serve, sorted.
// The schema registry keeps its own copy of this list so it can guard the
// overlay without importing this package (which would cost internal/schema its
// leaf status); a test asserts the two agree exactly.
func StubFixtureNames() []string {
	names := make([]string, 0, len(stubTargetFixtures)+len(stubLogFixtures))
	for name := range stubTargetFixtures {
		names = append(names, name)
	}
	for name := range stubLogFixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
