package simulate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"text/template"
)

const (
	// maxLogStages is the maximum number of stages allowed per simulate request.
	maxLogStages = 64
	// maxLogLines is the maximum number of sample lines allowed per request.
	maxLogLines = 20
	// maxLogLineLen is the maximum byte length of a single sample line.
	maxLogLineLen = 4096
)

// StageSpec describes a single loki.process pipeline stage for simulation.
type StageSpec struct {
	Type        string            `json:"type"`
	Expressions map[string]string `json:"expressions,omitempty"`
	Source      string            `json:"source,omitempty"`
	Separator   string            `json:"separator,omitempty"`
	Expression  string            `json:"expression,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	DropLabels  []string          `json:"drop_labels,omitempty"`
	DropValue   string            `json:"drop_value,omitempty"`
	Template    string            `json:"template,omitempty"`
	FirstLine   string            `json:"firstline,omitempty"`
}

// LogsRequest is the input for the log-stage trace endpoint.
type LogsRequest struct {
	Stages      []StageSpec `json:"stages"`
	SampleLines []string    `json:"sample_lines"`
}

// StageEffect records the effect of a single stage on a log line.
type StageEffect struct {
	StageIndex   int               `json:"stage_index"`
	StageType    string            `json:"stage_type"`
	Simulated    bool              `json:"simulated"`
	LineBefore   string            `json:"line_before"`
	LineAfter    string            `json:"line_after"`
	LabelsBefore map[string]string `json:"labels_before"`
	LabelsAfter  map[string]string `json:"labels_after"`
	Dropped      bool              `json:"dropped,omitempty"`
	Note         string            `json:"note,omitempty"`
}

// LineTrace is the per-line result of log-stage simulation.
type LineTrace struct {
	Input   string        `json:"input"`
	Steps   []StageEffect `json:"steps"`
	Output  string        `json:"output,omitempty"`
	Dropped bool          `json:"dropped"`
}

// LogsResponse is the response from the log-stage trace endpoint.
type LogsResponse struct {
	Traces []LineTrace `json:"traces"`
}

func parseLogfmt(line string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		start := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i == start || i >= len(line) {
			break
		}
		key := line[start:i]
		i++
		var value string
		if i < len(line) && line[i] == '"' {
			i++
			start = i
			for i < len(line) && line[i] != '"' {
				i++
			}
			value = line[start:i]
			if i < len(line) {
				i++
			}
		} else {
			start = i
			for i < len(line) && line[i] != ' ' {
				i++
			}
			value = line[start:i]
		}
		out[key] = value
	}
	return out
}

func cloneStrings(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func jsonValues(line string) (map[string]string, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range raw {
		out[k] = fmt.Sprint(v)
	}
	return out, nil
}

// LogTrace evaluates the given loki.process stages against each sample line,
// returning a per-line, per-stage trace. Unsupported stages appear as not_simulated steps.
func LogTrace(req LogsRequest) (LogsResponse, error) {
	if len(req.Stages) > maxLogStages {
		return LogsResponse{}, fmt.Errorf("too many stages: max %d", maxLogStages)
	}
	if len(req.SampleLines) > maxLogLines {
		return LogsResponse{}, fmt.Errorf("too many sample lines: max %d", maxLogLines)
	}
	for i, line := range req.SampleLines {
		if len(line) > maxLogLineLen {
			return LogsResponse{}, fmt.Errorf("sample line %d too long: max %d bytes", i, maxLogLineLen)
		}
	}

	response := LogsResponse{Traces: make([]LineTrace, 0, len(req.SampleLines))}
	for _, input := range req.SampleLines {
		line, extracted, labels := input, map[string]string{}, map[string]string{}
		trace := LineTrace{Input: input, Steps: make([]StageEffect, 0, len(req.Stages))}
		for i := range req.Stages {
			stage := &req.Stages[i]
			beforeLine, beforeLabels := line, cloneStrings(labels)
			effect := StageEffect{StageIndex: i, StageType: stage.Type, Simulated: true, LineBefore: beforeLine, LineAfter: beforeLine, LabelsBefore: beforeLabels, LabelsAfter: cloneStrings(labels)}
			var err error
			switch stage.Type {
			case "json":
				values, parseErr := jsonValues(line)
				if parseErr != nil {
					err = parseErr
				} else {
					for key, value := range values {
						extracted[key] = value
					}
				}
			case "logfmt":
				extracted = parseLogfmt(line)
			case "regex":
				r, e := regexp.Compile(stage.Expression)
				if e != nil {
					return response, fmt.Errorf("compiling regex stage: %w", e)
				}
				match := r.FindStringSubmatch(line)
				if match != nil {
					for n, name := range r.SubexpNames() {
						if n > 0 && name != "" {
							extracted[name] = match[n]
						}
					}
				}
			case "labels":
				for label, source := range stage.Labels {
					if value, ok := extracted[source]; ok {
						labels[label] = value
					}
				}
			case "label_drop":
				for _, label := range stage.DropLabels {
					delete(labels, label)
				}
			case "drop":
				drop := false
				for _, value := range extracted {
					drop = drop || value == stage.DropValue
				}
				drop = drop || line == stage.DropValue
				if stage.Expression != "" {
					var r *regexp.Regexp
					r, rErr := regexp.Compile(stage.Expression)
					if rErr != nil {
						return response, fmt.Errorf("compiling drop expression: %w", rErr)
					}
					drop = drop || r.MatchString(line)
				}
				if drop {
					trace.Dropped = true
					effect.Dropped = true
				}
			case "multiline":
				// multiline requires buffering multiple input lines — single-line tracing cannot
				// simulate this faithfully; expose as not_simulated.
				effect.Simulated = false
				effect.Note = "not_simulated — passes through unchanged in this preview"
			case "template":
				t, e := template.New("stage").Parse(stage.Template)
				if e != nil {
					return response, fmt.Errorf("parsing template stage: %w", e)
				}
				var b bytes.Buffer
				if e = t.Execute(&b, extracted); e != nil {
					return response, fmt.Errorf("executing template stage: %w", e)
				}
				line = b.String()
			default:
				effect.Simulated = false
				effect.Note = "not_simulated — passes through unchanged in this preview"
			}
			if err != nil {
				return response, fmt.Errorf("processing %s stage: %w", stage.Type, err)
			}
			effect.LineAfter, effect.LabelsAfter = line, cloneStrings(labels)
			trace.Steps = append(trace.Steps, effect)
			if trace.Dropped {
				break
			}
		}
		if !trace.Dropped {
			trace.Output = line
		}
		response.Traces = append(response.Traces, trace)
	}
	return response, nil
}
