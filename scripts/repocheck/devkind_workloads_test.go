package repocheck_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// Red run, 2026-09-14 (kind dev stack, slice K4 "workloads"): dev/kind/alloy.yaml and
// dev/kind/gitea.yaml did not exist yet. The very first assertion below —
// loadYAMLDocs("dev/kind/alloy.yaml") — failed with:
//
//	open dev/kind/alloy.yaml: no such file or directory
//
// (same shape for gitea.yaml). This file parses both manifests with yaml.v3 (multi-doc)
// plus dev/docker-compose.dev.yaml, and checks the shapes the kind dev-stack plan's §K4
// pins down: three Alloy Deployments with the placeholder image and matching ConfigMap
// mounts, and a Gitea PVC/Deployment/Service whose env and image equal compose's.

// kindDoc is the subset of any Kubernetes manifest document these specs need to route on.
type kindDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

// loadYAMLDocs decodes a repo-relative, possibly multi-document YAML file into raw nodes,
// one per `---`-separated document, so callers can route each on its `kind` before
// re-decoding into a specific typed shape.
func loadYAMLDocs(rel string) []*yaml.Node {
	GinkgoHelper()
	content := readRepoFile(rel)
	dec := yaml.NewDecoder(strings.NewReader(content))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		Expect(err).NotTo(HaveOccurred(), rel)
		docs = append(docs, &n)
	}
	return docs
}

type k8sDeployment struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Strategy struct {
			Type string `yaml:"type"`
		} `yaml:"strategy"`
		Template struct {
			Spec struct {
				Containers []struct {
					Image string   `yaml:"image"`
					Args  []string `yaml:"args"`
					Env   []struct {
						Name  string `yaml:"name"`
						Value string `yaml:"value"`
					} `yaml:"env"`
					VolumeMounts []struct {
						Name      string `yaml:"name"`
						MountPath string `yaml:"mountPath"`
						ReadOnly  bool   `yaml:"readOnly"`
					} `yaml:"volumeMounts"`
					ReadinessProbe struct {
						HTTPGet struct {
							Path string `yaml:"path"`
							Port int    `yaml:"port"`
						} `yaml:"httpGet"`
					} `yaml:"readinessProbe"`
				} `yaml:"containers"`
				Volumes []struct {
					Name      string `yaml:"name"`
					ConfigMap *struct {
						Name string `yaml:"name"`
					} `yaml:"configMap"`
					PersistentVolumeClaim *struct {
						ClaimName string `yaml:"claimName"`
					} `yaml:"persistentVolumeClaim"`
				} `yaml:"volumes"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func (d k8sDeployment) envMap() map[string]string {
	m := map[string]string{}
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			m[e.Name] = e.Value
		}
	}
	return m
}

// mountPathVolumeName returns the volume name mounted at a given path, and whether one was
// found (across every container).
func (d k8sDeployment) mountPathVolumeName(path string) (name string, readOnly, found bool) {
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, vm := range c.VolumeMounts {
			if vm.MountPath == path {
				return vm.Name, vm.ReadOnly, true
			}
		}
	}
	return "", false, false
}

type k8sPVC struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		AccessModes []string `yaml:"accessModes"`
		Resources   struct {
			Requests struct {
				Storage string `yaml:"storage"`
			} `yaml:"requests"`
		} `yaml:"resources"`
	} `yaml:"spec"`
}

type k8sService struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Ports []struct {
			Name string `yaml:"name"`
			Port int    `yaml:"port"`
		} `yaml:"ports"`
	} `yaml:"spec"`
}

// composeFile is the subset of dev/docker-compose.dev.yaml these specs compare against.
type composeFile struct {
	Services map[string]struct {
		Image       string            `yaml:"image"`
		Environment map[string]string `yaml:"environment"`
	} `yaml:"services"`
}

var _ = Describe("dev/kind/alloy.yaml", func() {
	var (
		raw         string
		deployments []k8sDeployment
	)

	BeforeEach(func() {
		raw = readRepoFile("dev/kind/alloy.yaml")
		docs := loadYAMLDocs("dev/kind/alloy.yaml")
		deployments = nil
		for _, doc := range docs {
			var probe kindDoc
			Expect(doc.Decode(&probe)).To(Succeed())
			if probe.Kind != "Deployment" {
				continue
			}
			var dep k8sDeployment
			Expect(doc.Decode(&dep)).To(Succeed())
			deployments = append(deployments, dep)
		}
	})

	It("defines exactly three Deployments", func() {
		Expect(deployments).To(HaveLen(3))
	})

	It("names the three Deployments after dev/alloy-*.alloy's basenames", func() {
		root := repoRoot()
		entries, err := os.ReadDir(filepath.Join(root, "dev"))
		Expect(err).NotTo(HaveOccurred())

		var wantNames []string
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "alloy-") || filepath.Ext(e.Name()) != ".alloy" {
				continue
			}
			wantNames = append(wantNames, strings.TrimSuffix(e.Name(), ".alloy"))
		}
		Expect(wantNames).NotTo(BeEmpty(), "no dev/alloy-*.alloy fixtures found")

		var gotNames []string
		for _, d := range deployments {
			gotNames = append(gotNames, d.Metadata.Name)
		}
		sort.Strings(wantNames)
		sort.Strings(gotNames)
		Expect(gotNames).To(Equal(wantNames))
	})

	It("uses the __ALLOY_IMAGE__ placeholder for every container, never a real image", func() {
		for _, d := range deployments {
			for _, c := range d.Spec.Template.Spec.Containers {
				Expect(c.Image).To(Equal("__ALLOY_IMAGE__"), d.Metadata.Name)
			}
		}
		Expect(raw).NotTo(ContainSubstring("grafana/alloy"))
	})

	It("runs compose's exact args", func() {
		for _, d := range deployments {
			var args []string
			for _, c := range d.Spec.Template.Spec.Containers {
				args = append(args, c.Args...)
			}
			Expect(args).To(ContainElement("/etc/alloy/config.alloy"), d.Metadata.Name)
			Expect(args).To(ContainElement("--disable-reporting"), d.Metadata.Name)
		}
	})

	It("mounts a ConfigMap named after itself, read-only, at /etc/alloy", func() {
		for _, d := range deployments {
			volName, readOnly, found := d.mountPathVolumeName("/etc/alloy")
			Expect(found).To(BeTrue(), "%s: no volumeMount at /etc/alloy", d.Metadata.Name)
			Expect(readOnly).To(BeTrue(), "%s: /etc/alloy must be read-only", d.Metadata.Name)

			var cmName string
			for _, v := range d.Spec.Template.Spec.Volumes {
				if v.Name == volName {
					Expect(v.ConfigMap).NotTo(BeNil(), "%s: volume %q is not a configMap", d.Metadata.Name, volName)
					cmName = v.ConfigMap.Name
				}
			}
			Expect(cmName).To(Equal(d.Metadata.Name))
		}
	})

	It("gives each pod a writable /tmp via emptyDir", func() {
		for _, d := range deployments {
			_, _, found := d.mountPathVolumeName("/tmp")
			Expect(found).To(BeTrue(), d.Metadata.Name)
		}
	})
})

var _ = Describe("dev/kind/gitea.yaml", func() {
	var (
		pvc        k8sPVC
		deployment k8sDeployment
		service    k8sService
		compose    composeFile
	)

	BeforeEach(func() {
		docs := loadYAMLDocs("dev/kind/gitea.yaml")
		pvc = k8sPVC{}
		deployment = k8sDeployment{}
		service = k8sService{}
		for _, doc := range docs {
			var probe kindDoc
			Expect(doc.Decode(&probe)).To(Succeed())
			switch probe.Kind {
			case "PersistentVolumeClaim":
				Expect(doc.Decode(&pvc)).To(Succeed())
			case "Deployment":
				Expect(doc.Decode(&deployment)).To(Succeed())
			case "Service":
				Expect(doc.Decode(&service)).To(Succeed())
			}
		}
		loadYAML("dev/docker-compose.dev.yaml", &compose)
	})

	It("declares a 1Gi RWO PVC named gitea-data", func() {
		Expect(pvc.Kind).To(Equal("PersistentVolumeClaim"))
		Expect(pvc.Metadata.Name).To(Equal("gitea-data"))
		Expect(pvc.Spec.AccessModes).To(ContainElement("ReadWriteOnce"))
		Expect(pvc.Spec.Resources.Requests.Storage).To(Equal("1Gi"))
	})

	It("mounts the PVC at /var/lib/gitea and uses the Recreate strategy", func() {
		Expect(deployment.Kind).To(Equal("Deployment"))
		Expect(deployment.Spec.Strategy.Type).To(Equal("Recreate"))

		volName, _, found := deployment.mountPathVolumeName("/var/lib/gitea")
		Expect(found).To(BeTrue())

		var claim string
		for _, v := range deployment.Spec.Template.Spec.Volumes {
			if v.Name == volName {
				Expect(v.PersistentVolumeClaim).NotTo(BeNil(), "volume %q is not a PVC", volName)
				claim = v.PersistentVolumeClaim.ClaimName
			}
		}
		Expect(claim).To(Equal(pvc.Metadata.Name))
	})

	It("gives the pod a writable /tmp via emptyDir", func() {
		_, _, found := deployment.mountPathVolumeName("/tmp")
		Expect(found).To(BeTrue())
	})

	It("probes readiness against /api/healthz on 3000", func() {
		var probe struct {
			Path string
			Port int
		}
		for _, c := range deployment.Spec.Template.Spec.Containers {
			if c.ReadinessProbe.HTTPGet.Path != "" {
				probe.Path = c.ReadinessProbe.HTTPGet.Path
				probe.Port = c.ReadinessProbe.HTTPGet.Port
			}
		}
		Expect(probe.Path).To(Equal("/api/healthz"))
		Expect(probe.Port).To(Equal(3000))
	})

	It("exposes a Service named gitea on ports 3000 and 2222", func() {
		Expect(service.Kind).To(Equal("Service"))
		Expect(service.Metadata.Name).To(Equal("gitea"))
		var ports []int
		for _, p := range service.Spec.Ports {
			ports = append(ports, p.Port)
		}
		Expect(ports).To(ContainElements(3000, 2222))
	})

	It("uses compose's gitea image, verbatim", func() {
		want := compose.Services["gitea"].Image
		Expect(want).NotTo(BeEmpty(), "dev/docker-compose.dev.yaml services.gitea.image")
		var got string
		for _, c := range deployment.Spec.Template.Spec.Containers {
			got = c.Image
		}
		Expect(got).To(Equal(want))
	})

	It("sets compose's gitea env, key-for-key", func() {
		want := compose.Services["gitea"].Environment
		Expect(want).NotTo(BeEmpty(), "dev/docker-compose.dev.yaml services.gitea.environment")
		Expect(deployment.envMap()).To(Equal(want))
	})
})
