package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const ToolDocumentCatalog = "document_catalog"

var documentCatalogTools = []mcpserver.Tool{{
	Name: ToolDocumentCatalog, Title: "Describe document catalog",
	Description: "Read the authorized document's typed field shapes, block kinds, relations, nested stable-list identities, and File ownership for advanced document_apply operations. Use document_read for existing stable handles. The compiler enforces domain constraints; this catalog does not specify defaults, required values, or enum ranges.",
	InputSchema: openInputSchema(), OutputSchema: documentCatalogOutputSchema(),
	SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(true, false, false), Meta: oauthSecurityMeta(),
}}

type DocumentCatalogApplication interface {
	Describe(context.Context, core.OpenRequest) (core.DescribeResult, error)
}

type DocumentCatalogTools struct{ application DocumentCatalogApplication }

func NewDocumentCatalogTools(application DocumentCatalogApplication) (*DocumentCatalogTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP document catalog application is required")
	}
	return &DocumentCatalogTools{application: application}, nil
}

func (*DocumentCatalogTools) ToolNames() []string { return toolDefinitionNames(documentCatalogTools) }

func (*DocumentCatalogTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(documentCatalogTools), nil
}

func (tools *DocumentCatalogTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if name != ToolDocumentCatalog {
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
	var input openArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateMCPDocumentReference(input.Document); err != nil {
		return executionError(err)
	}
	description, err := tools.application.Describe(ctx, core.OpenRequest{
		Document: core.DocumentIdentity{Domain: input.Profile, Reference: input.Document}, Locale: input.Locale,
	})
	if err != nil {
		return expectedToolError(err)
	}
	encoded, err := json.Marshal(projectDocumentCatalog(description))
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document catalog: %w", err)
	}
	return structuredResult(encoded, false)
}

type documentCatalogOutput struct {
	core.OpenMetadata
	BlockKinds     []core.BlockKind       `json:"block_kinds"`
	Fields         []catalogField         `json:"fields"`
	Relations      []catalogRelation      `json:"relations"`
	RelationFields []catalogRelationField `json:"relation_fields"`
}

type catalogShape struct {
	Kind         core.ValueKind       `json:"kind,omitempty"`
	Ownership    core.FieldOwnership  `json:"ownership"`
	Translatable bool                 `json:"translatable"`
	File         bool                 `json:"file"`
	Item         *catalogShape        `json:"item,omitempty"`
	Fields       []catalogNestedField `json:"fields,omitempty"`
	Identity     *catalogListIdentity `json:"identity,omitempty"`
}

type catalogNestedField struct {
	Field  core.FieldID `json:"field"`
	Schema catalogShape `json:"schema"`
}

type catalogListIdentity struct {
	Kind    core.ListIdentityKind `json:"kind"`
	Field   core.FieldID          `json:"field,omitempty"`
	Handles []core.RelationItemID `json:"handles,omitempty"`
}

type catalogField struct {
	BlockKind core.BlockKind `json:"block_kind"`
	Field     core.FieldID   `json:"field"`
	catalogShape
}

type catalogRelation struct {
	BlockKind core.BlockKind          `json:"block_kind"`
	Relation  core.RelationID         `json:"relation"`
	ItemKinds []core.RelationItemKind `json:"item_kinds"`
}

type catalogRelationField struct {
	BlockKind core.BlockKind        `json:"block_kind"`
	Relation  core.RelationID       `json:"relation"`
	ItemKind  core.RelationItemKind `json:"item_kind"`
	Field     core.FieldID          `json:"field"`
	catalogShape
}

func projectDocumentCatalog(description core.DescribeResult) documentCatalogOutput {
	catalog := description.Catalog
	output := documentCatalogOutput{
		OpenMetadata: description.Metadata, BlockKinds: append([]core.BlockKind{}, catalog.BlockKinds...),
		Fields: make([]catalogField, 0, len(catalog.Fields)), Relations: make([]catalogRelation, 0, len(catalog.Relations)),
		RelationFields: make([]catalogRelationField, 0, len(catalog.RelationFields)),
	}
	for _, rule := range catalog.Fields {
		shape := core.FieldSchema{Kind: rule.ValueKind, Ownership: rule.Ownership, Translatable: rule.Translatable, File: rule.File}
		if rule.Schema != nil {
			shape = *rule.Schema
		}
		output.Fields = append(output.Fields, catalogField{BlockKind: rule.BlockKind, Field: rule.Field, catalogShape: projectCatalogShape(shape)})
	}
	for _, rule := range catalog.Relations {
		items := append([]core.RelationItemKind{}, rule.ItemKinds...)
		sort.Slice(items, func(i, j int) bool { return items[i] < items[j] })
		output.Relations = append(output.Relations, catalogRelation{BlockKind: rule.BlockKind, Relation: rule.Relation, ItemKinds: items})
	}
	for _, rule := range catalog.RelationFields {
		shape := core.FieldSchema{Kind: rule.ValueKind, Ownership: rule.Ownership, Translatable: rule.Translatable, File: rule.File}
		if rule.Schema != nil {
			shape = *rule.Schema
		}
		output.RelationFields = append(output.RelationFields, catalogRelationField{BlockKind: rule.BlockKind, Relation: rule.Relation, ItemKind: rule.ItemKind, Field: rule.Field, catalogShape: projectCatalogShape(shape)})
	}
	sort.Slice(output.BlockKinds, func(i, j int) bool { return output.BlockKinds[i] < output.BlockKinds[j] })
	sort.Slice(output.Fields, func(i, j int) bool {
		a, b := output.Fields[i], output.Fields[j]
		if a.BlockKind != b.BlockKind {
			return a.BlockKind < b.BlockKind
		}
		return a.Field < b.Field
	})
	sort.Slice(output.Relations, func(i, j int) bool {
		a, b := output.Relations[i], output.Relations[j]
		if a.BlockKind != b.BlockKind {
			return a.BlockKind < b.BlockKind
		}
		return a.Relation < b.Relation
	})
	sort.Slice(output.RelationFields, func(i, j int) bool {
		a, b := output.RelationFields[i], output.RelationFields[j]
		if a.BlockKind != b.BlockKind {
			return a.BlockKind < b.BlockKind
		}
		if a.Relation != b.Relation {
			return a.Relation < b.Relation
		}
		if a.ItemKind != b.ItemKind {
			return a.ItemKind < b.ItemKind
		}
		return a.Field < b.Field
	})
	return output
}

func projectCatalogShape(schema core.FieldSchema) catalogShape {
	shape := catalogShape{Kind: schema.Kind, Ownership: schema.Ownership, Translatable: schema.Translatable, File: schema.File}
	if schema.Item != nil {
		item := projectCatalogShape(*schema.Item)
		shape.Item = &item
	}
	for _, field := range schema.Fields {
		shape.Fields = append(shape.Fields, catalogNestedField{Field: field.Field, Schema: projectCatalogShape(field.Schema)})
	}
	sort.Slice(shape.Fields, func(i, j int) bool { return shape.Fields[i].Field < shape.Fields[j].Field })
	if schema.Identity.Kind != "" {
		shape.Identity = &catalogListIdentity{Kind: schema.Identity.Kind, Field: schema.Identity.Field, Handles: append([]core.RelationItemID(nil), schema.Identity.Handles...)}
	}
	return shape
}
