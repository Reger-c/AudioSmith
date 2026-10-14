package store

import (
	"context"
	"encoding/json"
	"time"
)

// LibWorkflow is the Projects page's category of workflows (M6).
const LibWorkflow = "workflow"

// WorkflowRecord is a workflow as the store keeps it (M6): the documents
// the workflow service owns (internal/workflow.Workflow and its last Run
// as JSON; the store does not read them) and the columns lists show.
type WorkflowRecord struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
	Name      string
	Summary   string // the steps, "Start → Speak → Export"
	// State, Items and Done sum up the last run.
	State string
	Items int
	Done  int
	// Doc is the workflow, Run its last run (nil before one); List
	// leaves both nil.
	Doc json.RawMessage
	Run json.RawMessage
}

// Workflows stores workflows. Like Dubs, it is not part of Store:
// sqlitestore.(*Store).Workflows returns it.
type Workflows interface {
	// PutDoc inserts or updates a workflow's document, name, summary and
	// times, keeping its run.
	PutDoc(ctx context.Context, r WorkflowRecord) error
	// PutRun updates the run and its summary (State, Items, Done) of a
	// saved workflow (core.ErrNotFound if there is none).
	PutRun(ctx context.Context, r WorkflowRecord) error
	Get(ctx context.Context, id string) (WorkflowRecord, error)
	// List returns every workflow, most recently changed first, without
	// documents.
	List(ctx context.Context) ([]WorkflowRecord, error)
	Delete(ctx context.Context, id string) error
	// Dir is where the workflows' files live (<dir>/<id>/). Pure.
	Dir() string
}
