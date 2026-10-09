package mgmtapi

import (
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
)

// pipelineRef is one pipeline a refusal is about.
type pipelineRef struct{ id, name string }

// withPipelinesDetail attaches the pipelines a refusal names to err as a
// Connect error detail (M4, review of #291), so the UI can link each one
// to its page without parsing the message — which also quotes Alloy labels
// and the destination's own name. The detail is a google.protobuf.Struct,
// a well-known type both sides already have, so it needs no new proto:
//
//	{"pipelines": [{"id": "<uuid>", "name": "<name>"}, …]}
//
// The message is unchanged and stays complete on its own; a client that
// ignores details loses nothing. A detail that cannot be built is skipped.
func withPipelinesDetail(err *connect.Error, refs []pipelineRef) *connect.Error {
	if len(refs) == 0 {
		return err
	}
	list := make([]any, len(refs))
	for i, r := range refs {
		list[i] = map[string]any{"id": r.id, "name": r.name}
	}
	st, sErr := structpb.NewStruct(map[string]any{"pipelines": list})
	if sErr != nil {
		return err
	}
	detail, dErr := connect.NewErrorDetail(st)
	if dErr != nil {
		return err
	}
	err.AddDetail(detail)
	return err
}
