// Package simulate provides deterministic S2 stage-trace evaluation.
package simulate

import (
	"fmt"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/relabel"
)

const (
	// maxRelabelRules is the maximum number of rules allowed per simulate request.
	maxRelabelRules = 128
	// maxRelabelTargets is the maximum number of sample targets allowed per request.
	maxRelabelTargets = 50
	// maxLabelCount is the maximum number of labels on a single target.
	maxLabelCount = 64
)

// RelabelRule is a single Prometheus relabel rule submitted for simulation.
type RelabelRule struct {
	SourceLabels []string `json:"source_labels"`
	Separator    string   `json:"separator"`
	Regex        string   `json:"regex"`
	TargetLabel  string   `json:"target_label"`
	Replacement  string   `json:"replacement"`
	Action       string   `json:"action"`
	Modulus      uint64   `json:"modulus,omitempty"`
}

// RelabelRequest is the input for the relabel trace endpoint.
type RelabelRequest struct {
	Rules         []RelabelRule       `json:"rules"`
	SampleTargets []map[string]string `json:"sample_targets"`
}

// RelabelStep records the effect of a single rule on a target's label set.
type RelabelStep struct {
	RuleIndex int               `json:"rule_index"`
	Action    string            `json:"action"`
	Before    map[string]string `json:"before"`
	After     map[string]string `json:"after,omitempty"`
	Kept      bool              `json:"kept"`
}

// TargetTrace is the per-target result of relabel simulation.
type TargetTrace struct {
	Input  map[string]string `json:"input"`
	Steps  []RelabelStep     `json:"steps"`
	Output map[string]string `json:"output,omitempty"`
	Kept   bool              `json:"kept"`
}

// RelabelResponse is the response from the relabel trace endpoint.
type RelabelResponse struct {
	Traces []TargetTrace `json:"traces"`
}

// validRelabelActions is the set of allowed relabel action strings.
var validRelabelActions = map[string]bool{
	"replace": true, "keep": true, "drop": true, "labelmap": true,
	"labeldrop": true, "labelkeep": true, "lowercase": true, "uppercase": true,
	"keepequal": true, "dropequal": true, "hashmod": true,
}

// configForRule builds a validated relabel.Config from a RelabelRule.
// Returns an error rather than panicking on invalid regex.
func configForRule(r RelabelRule) (*relabel.Config, error) {
	separator := r.Separator
	if separator == "" {
		separator = ";"
	}
	replacement := r.Replacement
	if replacement == "" {
		replacement = "$1"
	}
	action := r.Action
	if action == "" {
		action = "replace"
	}
	if !validRelabelActions[action] {
		return nil, fmt.Errorf("unknown relabel action %q", action)
	}
	regexStr := r.Regex
	if regexStr == "" {
		regexStr = "(.*)"
	}
	re, err := relabel.NewRegexp(regexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", regexStr, err)
	}
	sourceLabels := make(model.LabelNames, len(r.SourceLabels))
	for i, source := range r.SourceLabels {
		sourceLabels[i] = model.LabelName(source)
	}
	return &relabel.Config{
		SourceLabels:         sourceLabels,
		Separator:            separator,
		Regex:                re,
		TargetLabel:          r.TargetLabel,
		Replacement:          replacement,
		Action:               relabel.Action(action),
		Modulus:              r.Modulus,
		NameValidationScheme: model.LegacyValidation,
	}, nil
}

func mapLabels(ls labels.Labels) map[string]string {
	out := make(map[string]string, ls.Len())
	ls.Range(func(l labels.Label) { out[l.Name] = l.Value })
	return out
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// RelabelTrace evaluates the given relabel rules against each sample target,
// returning a per-target, per-rule trace using the real Prometheus relabel library.
func RelabelTrace(req RelabelRequest) (RelabelResponse, error) {
	if len(req.Rules) > maxRelabelRules {
		return RelabelResponse{}, fmt.Errorf("too many rules: max %d", maxRelabelRules)
	}
	if len(req.SampleTargets) > maxRelabelTargets {
		return RelabelResponse{}, fmt.Errorf("too many sample targets: max %d", maxRelabelTargets)
	}

	// Pre-validate and compile all rules once before iterating targets.
	configs := make([]*relabel.Config, len(req.Rules))
	for i, rule := range req.Rules {
		cfg, err := configForRule(rule)
		if err != nil {
			return RelabelResponse{}, fmt.Errorf("rule %d: %w", i, err)
		}
		configs[i] = cfg
	}

	response := RelabelResponse{Traces: make([]TargetTrace, 0, len(req.SampleTargets))}
	for _, target := range req.SampleTargets {
		if len(target) > maxLabelCount {
			return RelabelResponse{}, fmt.Errorf("target has too many labels: max %d", maxLabelCount)
		}
		base := cloneMap(target)
		trace := TargetTrace{
			Input: cloneMap(base),
			Steps: make([]RelabelStep, 0, len(req.Rules)),
			Kept:  true,
		}
		current := labels.FromMap(base)
		for i, rule := range req.Rules {
			before := mapLabels(current)
			builder := labels.NewBuilder(current)
			kept := relabel.ProcessBuilder(builder, configs[i])
			action := rule.Action
			if action == "" {
				action = "replace"
			}
			step := RelabelStep{RuleIndex: i, Action: action, Before: before, Kept: kept}
			if kept {
				current = builder.Labels()
				step.After = mapLabels(current)
			} else {
				trace.Kept = false
			}
			trace.Steps = append(trace.Steps, step)
			if !kept {
				break
			}
		}
		if trace.Kept {
			trace.Output = mapLabels(current)
		}
		response.Traces = append(response.Traces, trace)
	}
	return response, nil
}
