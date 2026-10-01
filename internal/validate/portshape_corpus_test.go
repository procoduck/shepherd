package validate_test

import (
	"go/ast"
	goparser "go/parser"
	"go/token"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	alloyparser "github.com/grafana/alloy/syntax/parser"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
)

// reproMarker is carried (as an Alloy comment) by every deliberately wrong
// #233 config in a test file that also holds valid fixtures, so the corpus
// walk below can tell the two apart per literal.
const reproMarker = "#233 reproduction"

// portShapeSpecFiles hold only deliberately wrong configs that prove the check
// fires; the corpus walk skips them whole.
var portShapeSpecFiles = map[string]bool{
	"internal/validate/portshape_test.go": true,
	"internal/visual/portshape_test.go":   true,
}

// legacyTruePositives are Alloy texts in the repo written in the bracket-wrapped
// `targets = [x.targets]` form that Shepherd's renderer emitted before refValue
// was fixed (docs/proofs/sandbox-sim-e2e.md §1) — configs real Alloy refuses at
// load. Each is kept on purpose and none is validated or served. The spec
// asserts each one IS flagged: they are the corpus's own evidence that the
// check finds the real bug, not just the synthetic one.
var legacyTruePositives = map[string]string{
	"docs/proofs/transform-secret-drop.md": "a proof quoting the transform's output from before refValue's fix",
	"internal/simulate/loki-enrich.alloy":  "a v1.18.1-era generated fixture nothing loads; kept as it was rendered",
	"internal/visual/parse_test.go":        "ParseAlloy inputs: importing the legacy form is the migration path to the runnable one",
}

// The port-shape check refuses saves and git syncs, so a false positive is a
// user locked out of their own pipeline. This spec runs it over every Alloy
// text the repository carries — the visual-builder corpus goldens, every
// wizard's goldens, the receiver/simulate/dev/e2e .alloy files, every Alloy
// string literal in Go source (seed pipelines in internal/cli/dev.go, e2e and
// gitsync fixtures, test inputs), every ```alloy block in the Markdown docs and
// every <pre> block in the docs-site sources (scripts/docs-content) — and
// requires that none of them produce a diagnostic, legacy true positives aside.
//
// Discovery is by walking the tree rather than a hand-kept list, so a new
// corpus is covered the day it lands.
var _ = Describe("port-shape check over the repository's corpora", func() {
	It("finds no false positive, and flags every legacy list-of-lists config", func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		Expect(err).NotTo(HaveOccurred())

		type unit struct{ file, where, content string }
		var units []unit
		fenced := regexp.MustCompile("(?s)```alloy\\s*\\n(.*?)```")
		pre := regexp.MustCompile(`(?s)<pre[^>]*>(.*?)</pre>`)
		tag := regexp.MustCompile(`<[^>]+>`)

		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				switch d.Name() {
				case ".git", "node_modules", ".claude", "dist", "site":
					return filepath.SkipDir
				}
				return nil
			}
			read := func() string {
				b, err := os.ReadFile(path)
				Expect(err).NotTo(HaveOccurred())
				return string(b)
			}
			switch {
			case strings.HasSuffix(path, ".alloy"):
				units = append(units, unit{rel, rel, read()})
			case strings.HasSuffix(path, ".md"):
				for i, m := range fenced.FindAllStringSubmatch(read(), -1) {
					units = append(units, unit{rel, rel + "#alloy-" + strconv.Itoa(i), m[1]})
				}
			case strings.HasSuffix(path, ".html") && strings.HasPrefix(rel, "scripts/docs-content/"):
				for i, m := range pre.FindAllStringSubmatch(read(), -1) {
					units = append(units, unit{rel, rel + "#pre-" + strconv.Itoa(i), html.UnescapeString(tag.ReplaceAllString(m[1], ""))})
				}
			case strings.HasSuffix(path, ".go"):
				if portShapeSpecFiles[rel] {
					return nil
				}
				fset := token.NewFileSet()
				f, perr := goparser.ParseFile(fset, path, nil, goparser.SkipObjectResolution)
				if perr != nil {
					return nil // a Go file that does not parse is not a corpus
				}
				ast.Inspect(f, func(n ast.Node) bool {
					lit, ok := n.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					s, uerr := strconv.Unquote(lit.Value)
					if uerr != nil || !strings.Contains(s, "{") || strings.Contains(s, reproMarker) {
						return true
					}
					units = append(units, unit{rel, rel + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line), s})
					return true
				})
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())

		checked := 0
		var falsePositives []string
		flaggedLegacy := map[string]bool{}
		for _, u := range units {
			// Only text that parses as Alloy is a corpus; Stage 1 owns the rest.
			if _, perr := alloyparser.ParseFile("<corpus>", []byte(u.content)); perr != nil {
				continue
			}
			checked++
			for _, content := range []string{u.content, validate.WrapForValidation("corpus", u.content)} {
				for _, d := range validate.PortShapes(content) {
					if _, legacy := legacyTruePositives[u.file]; legacy {
						Expect(d.Message).To(ContainSubstring("is a list of lists"), u.where)
						flaggedLegacy[u.file] = true
						continue
					}
					falsePositives = append(falsePositives, u.where+": "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Col)+": "+d.Message)
				}
			}
		}
		GinkgoWriter.Printf("port-shape corpus: %d Alloy texts checked\n", checked)
		// A floor, so a broken walk cannot pass by checking nothing.
		Expect(checked).To(BeNumerically(">=", 150))
		Expect(falsePositives).To(BeEmpty())

		var missed []string
		for file := range legacyTruePositives {
			if !flaggedLegacy[file] {
				missed = append(missed, file)
			}
		}
		sort.Strings(missed)
		Expect(missed).To(BeEmpty(), "a legacy list-of-lists config the check no longer flags")
	})
})
