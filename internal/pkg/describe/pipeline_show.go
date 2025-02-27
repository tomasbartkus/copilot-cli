// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package describe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/aws/copilot-cli/internal/pkg/aws/sessions"
	"github.com/aws/copilot-cli/internal/pkg/describe/stack"

	// TODO refactor this into our own pkg
	"github.com/aws/copilot-cli/internal/pkg/aws/codepipeline"
	"github.com/aws/copilot-cli/internal/pkg/term/color"
)

type pipelineGetter interface {
	GetPipeline(pipelineName string) (*codepipeline.Pipeline, error)
}

// Pipeline contains serialized parameters for a pipeline.
type Pipeline struct {
	codepipeline.Pipeline

	Resources []*stack.Resource `json:"resources,omitempty"`
}

// PipelineDescriber retrieves information about an application.
type PipelineDescriber struct {
	pipelineName  string
	showResources bool

	pipelineSvc pipelineGetter
	cfn         stackDescriber
}

// NewPipelineDescriber instantiates a new pipeline describer
func NewPipelineDescriber(pipelineName string, showResources bool) (*PipelineDescriber, error) {
	sess, err := sessions.ImmutableProvider().Default()
	if err != nil {
		return nil, err
	}

	pipelineSvc := codepipeline.New(sess)

	return &PipelineDescriber{
		pipelineName:  pipelineName,
		pipelineSvc:   pipelineSvc,
		showResources: showResources,
		cfn:           stack.NewStackDescriber(pipelineName, sess),
	}, nil
}

// Describe returns description of a pipeline.
func (d *PipelineDescriber) Describe() (HumanJSONStringer, error) {
	cp, err := d.pipelineSvc.GetPipeline(d.pipelineName)
	if err != nil {
		return nil, fmt.Errorf("get pipeline: %w", err)
	}
	var resources []*stack.Resource
	if d.showResources {
		stackResources, err := d.cfn.Resources()
		if err != nil && !IsStackNotExistsErr(err) {
			return nil, fmt.Errorf("retrieve pipeline resources: %w", err)
		}
		resources = stackResources
	}
	pipeline := &Pipeline{*cp, resources}
	return pipeline, nil
}

// JSONString returns the stringified Pipeline struct with JSON format.
func (p *Pipeline) JSONString() (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal pipeline: %w", err)
	}
	return fmt.Sprintf("%s\n", b), nil
}

// HumanString returns the stringified Pipeline struct with human readable format.
func (p *Pipeline) HumanString() string {
	var b bytes.Buffer
	// TODO tweak the spacing
	writer := tabwriter.NewWriter(&b, minCellWidth, tabWidth, cellPaddingWidth, paddingChar, noAdditionalFormatting)
	fmt.Fprint(writer, color.Bold.Sprint("About\n\n"))
	writer.Flush()
	fmt.Fprintf(writer, "  %s\t%s\n", "Name", p.Name)
	fmt.Fprintf(writer, "  %s\t%s\n", "Region", p.Region)
	fmt.Fprintf(writer, "  %s\t%s\n", "AccountID", p.AccountID)
	fmt.Fprintf(writer, "  %s\t%s\n", "Created At", humanizeTime(p.CreatedAt))
	fmt.Fprintf(writer, "  %s\t%s\n", "Updated At", humanizeTime(p.UpdatedAt))
	writer.Flush()
	fmt.Fprint(writer, color.Bold.Sprint("\nStages\n\n"))
	writer.Flush()
	headers := []string{"Name", "Category", "Provider", "Details"}
	fmt.Fprintf(writer, "  %s\n", strings.Join(headers, "\t"))
	fmt.Fprintf(writer, "  %s\n", strings.Join(underline(headers), "\t"))
	for _, stage := range p.Stages {
		fmt.Fprintf(writer, "  %s", stage.HumanString())
	}
	writer.Flush()
	if len(p.Resources) != 0 {
		fmt.Fprint(writer, color.Bold.Sprint("\nResources\n"))
		writer.Flush()
		for _, r := range p.Resources {
			fmt.Fprintf(writer, "    %s", r.HumanString())
		}

	}
	writer.Flush()
	return b.String()
}
