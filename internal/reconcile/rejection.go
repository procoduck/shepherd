package reconcile

import "fmt"

// KindServedConfigRejected fires when the collector reported that it failed
// to load the config Shepherd serves it (remote_config_status FAILED, #297):
// it is still running its previous config, so what is served and what is
// observed running cannot agree until the rejection is fixed. Served-then-
// Observed. When the load error's line falls inside a pipeline's declare
// block, the finding names that pipeline (PipelineName).
const KindServedConfigRejected Kind = "served_config_rejected"

// Rejection is a collector's reported load failure, already attributed by
// the caller: internal/merge.ErrorPosition reads the line the error names,
// and internal/merge.Locate maps it into the served content. This package
// only phrases it — it never parses configs or error text itself.
type Rejection struct {
	// AboutCurrent is true when the rejected config is the one served now
	// (the status was reported with the served hash). A rejection of an
	// earlier config cannot be attributed against the current content.
	AboutCurrent bool
	// MergedLine is the 1-based line of the served config the error names,
	// or 0 when it names none.
	MergedLine int
	// PipelineName is the pipeline whose declare block contains MergedLine,
	// or "" when no block does.
	PipelineName string
	// PipelineLine is MergedLine as a line of the pipeline's own text, or 0
	// when MergedLine is part of the declare wrapper.
	PipelineLine int
}

// RejectionFinding phrases a collector's rejection of its served config as a
// KindServedConfigRejected finding. Its summary leads with "rejected in
// pipeline "<name>" (line N of its text…" whenever the line can be
// attributed, so every surface can show it verbatim.
func RejectionFinding(r Rejection) Finding {
	f := Finding{
		Kind:    KindServedConfigRejected,
		Sources: [2]Source{SourceServed, SourceObserved},
	}
	const running = "the collector is still running its previous config"
	switch {
	case !r.AboutCurrent:
		f.Summary = "This collector rejected an earlier served config and has not reported on the one served " +
			"now; the line its error names cannot be attributed to the current pipelines."
	case r.MergedLine == 0:
		f.Summary = "This collector rejected its served config; " + running +
			". The error names no line, so it cannot be attributed to a pipeline."
	case r.PipelineName == "":
		f.Summary = fmt.Sprintf("This collector rejected its served config at line %d of the merged config, "+
			"outside every pipeline's block (Shepherd's header or its self-monitoring baseline); %s.",
			r.MergedLine, running)
	case r.PipelineLine == 0:
		f.PipelineName = r.PipelineName
		f.Summary = fmt.Sprintf("Collector rejected in pipeline %q (its declare wrapper, line %d of the merged "+
			"config); %s.", r.PipelineName, r.MergedLine, running)
	default:
		f.PipelineName = r.PipelineName
		f.Summary = fmt.Sprintf("Collector rejected in pipeline %q (line %d of its text, line %d of the merged "+
			"config); %s.", r.PipelineName, r.PipelineLine, r.MergedLine, running)
	}
	return f
}
