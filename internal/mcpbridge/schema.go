package mcpbridge

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolSchema returns the tool with its input schema derived from the handler's
// input struct. The result matches what the SDK infers on its own, except that
// multi-type `type` keywords are rewritten into `anyOf` branches; see
// splitMultiType for the reason.
//
// Inference failures panic, exactly as they already do inside [mcp.AddTool]:
// the input type is fixed at compile time, so a failure is a programming error
// rather than a runtime one.
func toolSchema[In any](tool *mcp.Tool) *mcp.Tool {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("mcpbridge: tool %q: infer input schema for %T: %v", tool.Name, *new(In), err))
	}
	splitMultiType(schema)
	tool.InputSchema = schema
	return tool
}

// splitMultiType rewrites every node whose `type` lists several types into an
// equivalent `anyOf` of single-type branches.
//
// A multi-type `type` such as ["null","integer"] is legal JSON Schema and some
// providers recommend it for nullable fields, but it is poorly portable: a
// client that maps tool schemas onto a single-`type` dialect (for example the
// OpenAPI subset used for Gemini function declarations) may reject the tool or
// drop the constraint. Declaring one type per branch means no dialect has to
// understand a type array. This only changes the spelling: the property still
// accepts `null`, and it stays required, because optional (absent) is a
// different contract than nullable.
func splitMultiType(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	switch len(s.Types) {
	case 0:
	case 1:
		s.Type, s.Types = s.Types[0], nil
	default:
		branches := make([]*jsonschema.Schema, 0, len(s.Types))
		for _, typ := range s.Types {
			branches = append(branches, &jsonschema.Schema{Type: typ})
		}
		s.Types = nil
		s.AnyOf = append(branches, s.AnyOf...)
	}

	// Every field that can hold a sub-schema has to be visited, otherwise a
	// nested multi-type property would keep the non-portable spelling.
	for _, group := range []map[string]*jsonschema.Schema{
		s.Properties, s.PatternProperties, s.Defs, s.Definitions, s.DependentSchemas,
	} {
		for _, child := range group {
			splitMultiType(child)
		}
	}
	for _, list := range [][]*jsonschema.Schema{
		s.PrefixItems, s.ItemsArray, s.AllOf, s.AnyOf, s.OneOf,
	} {
		for _, child := range list {
			splitMultiType(child)
		}
	}
	for _, child := range []*jsonschema.Schema{
		s.Items, s.AdditionalItems, s.AdditionalProperties, s.PropertyNames,
		s.Contains, s.UnevaluatedItems, s.UnevaluatedProperties, s.ContentSchema,
		s.Not, s.If, s.Then, s.Else,
	} {
		splitMultiType(child)
	}
}
