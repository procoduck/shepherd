package validate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/grafana/alloy/syntax/ast"

	"shepherd/internal/alloysyntax"
)

// ErrServedImportGit is a strict org's served text that still holds an
// import.git block, or a module it cannot inspect for one.
var ErrServedImportGit = errors.New("strict tenant mode serves no import.git")

// maxServedModuleDepth is how deep ServedFormProblem follows import.string
// contents: the lint's module depth (maxDepth). Deeper is refused.
const maxServedModuleDepth = maxDepth

// ServedFormProblem is the structural bound on what a strict org is served
// (#390): nil only when text — parsed, never searched — holds no import.git
// block, however the name is spelt (`import . git`, comments between the
// parts), at the top level, in any declare, or inside the content of any
// import.string at any depth (each content parsed the same way). An
// import.string whose content is not a plain string literal cannot be
// inspected, so it is refused, as is text that does not parse or modules
// nested deeper than the lint follows. Both the save (ValidatePipeline's
// served form) and every serve path (serve.ServedForm) apply it, whatever
// the inliner did.
func ServedFormProblem(text string) error {
	return servedFormProblem(text, 0)
}

func servedFormProblem(text string, depth int) error {
	if depth > maxServedModuleDepth {
		return fmt.Errorf("%w: modules nest more than %d deep", ErrServedImportGit, maxServedModuleDepth)
	}
	file, err := alloysyntax.ParseFile("<served>", []byte(text))
	if err != nil {
		return fmt.Errorf("%w: the text does not parse: %w", ErrServedImportGit, err)
	}
	return servedBody(file.Body, depth, 0)
}

func servedBody(body ast.Body, depth, nesting int) error {
	if nesting > alloysyntax.MaxNesting {
		return fmt.Errorf("%w: declares nest too deeply", ErrServedImportGit)
	}
	for _, st := range body {
		b, ok := st.(*ast.BlockStmt)
		if !ok {
			continue
		}
		switch strings.Join(b.Name, ".") {
		case "import.git":
			return fmt.Errorf("%w: import.git %q is still in the served text", ErrServedImportGit, b.Label)
		case "import.string":
			for _, inner := range b.Body {
				a, ok := inner.(*ast.AttributeStmt)
				if !ok || a.Name.Name != "content" {
					continue
				}
				content, ok := PlainStringLiteral(a.Value)
				if !ok {
					return fmt.Errorf("%w: import.string %q content is not a string literal, so it cannot be checked",
						ErrServedImportGit, b.Label)
				}
				if err := servedFormProblem(content, depth+1); err != nil {
					return fmt.Errorf("import.string %q: %w", b.Label, err)
				}
			}
		case "declare":
			if err := servedBody(b.Body, depth, nesting+1); err != nil {
				return err
			}
		}
	}
	return nil
}
