package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/stretchr/testify/require"
)

func TestSchemasReferenceGuidanceKeepsDocumentDiscoveryOnDocumentSelectors(t *testing.T) {
	documentSelectors := 0
	neutralUUIDs := 0
	for _, provider := range referenceGuidanceProviders() {
		tools, err := provider.ListTools(t.Context(), mcpserver.Principal{})
		require.NoError(t, err)
		for _, tool := range tools {
			for name, raw := range map[string]json.RawMessage{"input": tool.InputSchema, "output": tool.OutputSchema} {
				t.Run(tool.Name+"/"+name, func(t *testing.T) {
					var schema any
					require.NoError(t, json.Unmarshal(raw, &schema))
					walkReferenceSchema(schema, "$", func(path string, node map[string]any) {
						description, _ := node["description"].(string)
						if strings.Contains(description, "document_list") {
							require.Equal(t, "input", name, "%s advertises input discovery in output", path)
							switch path {
							case "$.properties.d", "$.properties.document_id", "$.properties.exclude_document_id", "$.properties.release_id":
								documentSelectors++
							case "$.properties.expected_configuration_revision":
								// document_list legitimately includes the observed Post settings revision.
							default:
								t.Errorf("%s incorrectly resolves a non-document selector through document_list", path)
							}
						}
						if node["format"] == "uuid" && description == "" {
							neutralUUIDs++
						}
					})
				})
			}
		}
	}
	require.Positive(t, documentSelectors)
	require.Positive(t, neutralUUIDs)
}

func TestSchemasReferenceGuidanceNamesOwningDiscoveryAndPreservesUUIDShape(t *testing.T) {
	var uuid, document map[string]any
	require.NoError(t, json.Unmarshal([]byte(uuidJSONSchema), &uuid))
	require.NoError(t, json.Unmarshal([]byte(documentReferenceJSONSchema), &document))
	delete(document, "description")
	require.Equal(t, document, uuid)

	// Representative consumers cover Files, Members, event types, and map resources.
	for _, test := range []struct {
		provider            ToolProvider
		tool, field, source string
	}{
		{&MapPlaceManagementTools{}, ToolMapPlaceCreate, "image_file_id", "file_list"},
		{&MemberAdminTools{}, ToolMemberAdminGet, "member_id", "member_admin_list"},
		{&ProgramEventManagementTools{}, ToolProgramEventCreate, "type_id", "program_event_type_list"},
		{&MapThemeManagementTools{}, ToolMapThemeGet, "theme_id", "map_theme_list"},
		{&ReferenceDiscoveryTools{}, ToolFileList, "folder_id", "item_type=folder"},
	} {
		t.Run(test.tool+"/"+test.field, func(t *testing.T) {
			tools, err := test.provider.ListTools(t.Context(), mcpserver.Principal{})
			require.NoError(t, err)
			found := false
			for _, tool := range tools {
				if tool.Name != test.tool {
					continue
				}
				found = true
				var schema map[string]any
				require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))
				field := schema["properties"].(map[string]any)[test.field].(map[string]any)
				require.Contains(t, field["description"], test.source)
				require.Equal(t, uuid, field["allOf"].([]any)[0])
			}
			require.True(t, found)
		})
	}
}

func TestSchemasReferenceGuidanceMetadataRelationsUseReferenceSearch(t *testing.T) {
	tools, err := (&AIDocumentTools{}).ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	var uuid map[string]any
	require.NoError(t, json.Unmarshal([]byte(uuidJSONSchema), &uuid))
	found := false
	for _, tool := range tools {
		if tool.Name != ToolMetadataUpdate {
			continue
		}
		found = true
		var schema map[string]any
		require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))
		properties := schema["properties"].(map[string]any)
		for field, kind := range map[string]string{"category_ids": "category", "tag_ids": "tag"} {
			relation := properties[field].(map[string]any)
			require.Contains(t, relation["description"], "reference_search with reference_type="+kind)
			require.Equal(t, "array", relation["type"])
			require.Equal(t, float64(256), relation["maxItems"])
			require.Equal(t, true, relation["uniqueItems"])
			require.Equal(t, uuid, relation["items"])
		}
	}
	require.True(t, found)
}

func referenceGuidanceProviders() []ToolProvider {
	return []ToolProvider{
		&ProgramEventMediaTools{}, &ContentManagementTools{}, &ReleaseManagementTools{},
		&MemberTagTools{}, &ReleaseRelationTools{}, &ProgramEventManagementTools{},
		&MapPlaceManagementTools{}, &MemberAdminTools{}, &ContentRelatedTools{}, &MapThemeManagementTools{},
		&ReferenceDiscoveryTools{}, &AIDocumentTools{}, &DocumentDiscoveryTools{},
	}
}

func walkReferenceSchema(value any, path string, visit func(string, map[string]any)) {
	switch value := value.(type) {
	case map[string]any:
		visit(path, value)
		for key, child := range value {
			walkReferenceSchema(child, path+"."+key, visit)
		}
	case []any:
		for _, child := range value {
			walkReferenceSchema(child, path+"[]", visit)
		}
	}
}
