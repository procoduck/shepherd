package repocheck_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// repoRoot walks up from the test's working directory to the module root.
func repoRoot() string {
	GinkgoHelper()
	dir, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	for range 10 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	Fail("could not find the module root from " + dir)
	return ""
}

// readRepoFile returns the contents of a file addressed relative to the root.
func readRepoFile(rel string) string {
	GinkgoHelper()
	b, err := os.ReadFile(filepath.Join(repoRoot(), rel))
	Expect(err).NotTo(HaveOccurred(), rel)
	return string(b)
}

// loadYAML unmarshals a repo-relative YAML file into out.
func loadYAML(rel string, out any) {
	GinkgoHelper()
	Expect(yaml.Unmarshal([]byte(readRepoFile(rel)), out)).To(Succeed(), rel)
}

// runMake runs `make` at the repo root and returns combined output and the
// exit error (nil on success). Callers pass -n for dry runs.
func runMake(args ...string) (string, error) {
	GinkgoHelper()
	cmd := exec.Command("make", args...)
	cmd.Dir = repoRoot()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// makeRecipe returns the recipe lines of one Makefile target: everything from
// the `name:` line to the next blank line.
func makeRecipe(name string) string {
	GinkgoHelper()
	mk := readRepoFile("Makefile")
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `:[^\n]*\n((?:[^\n]+\n)*)`)
	m := re.FindStringSubmatch(mk)
	Expect(m).NotTo(BeNil(), "target %q not found in Makefile", name)
	return m[0]
}

// mkTargetLine returns just the `name: prereq1 prereq2 ## comment` header
// line of one Makefile target, without its recipe body.
func mkTargetLine(name string) string {
	GinkgoHelper()
	mk := readRepoFile("Makefile")
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `:[^\n]*`)
	m := re.FindString(mk)
	Expect(m).NotTo(BeEmpty(), "target %q not found in Makefile", name)
	return m
}

// workflow is the subset of a GitHub Actions workflow file these specs read.
type workflow struct {
	On          map[string]any         `yaml:"on"`
	Permissions map[string]string      `yaml:"permissions"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Needs any               `yaml:"needs"`
	If    string            `yaml:"if"`
	Env   map[string]string `yaml:"env"`
	Steps []workflowStep    `yaml:"steps"`
}

type workflowStep struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	With map[string]any `yaml:"with"`
}

// loadWorkflow parses .github/workflows/<name>.
func loadWorkflow(name string) workflow {
	GinkgoHelper()
	var w workflow
	loadYAML(filepath.Join(".github", "workflows", name), &w)
	return w
}

// joinedRuns concatenates every step's run script for substring assertions.
func joinedRuns(steps []workflowStep) string {
	var sb strings.Builder
	for _, s := range steps {
		sb.WriteString(s.Run)
		sb.WriteString("\n")
	}
	return sb.String()
}
