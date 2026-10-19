package aidocumentadapter

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPageRichHeadingAndTableBatches(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "insert heading"
		if replace {
			name = "paragraph to heading"
		}
		t.Run(name, func(t *testing.T) {
			port, api, identity := newExactPagePortForLocale(t, "en", true)
			section, block := core.BlockID(uuid.NewString()), core.BlockID(uuid.NewString())
			ops := []core.Operation{core.InsertBlockOperation(section, "rich-text", "", "")}
			if replace {
				ops = append(ops, core.InsertBlockOperation(block, "paragraph", section, ""), core.ReplaceBlockKindOperation(block, "heading"), core.SetFieldOperation(block, "content", core.RichText(core.InlineText("Heading"))))
			} else {
				ops = append(ops, core.InsertBlockOperation(block, "heading", section, ""), core.SetFieldOperation(block, "content", core.RichText(core.InlineText("Heading"))))
			}
			ops = append(ops, core.SetFieldOperation(block, "level", core.Number("2")))
			service, err := core.NewService(port)
			require.NoError(t, err)
			validation, err := service.Validate(t.Context(), pageExactApplyRequest(identity, api.state, nil, ops...))
			require.NoError(t, err)
			require.Empty(t, validation.Issues)
			require.Len(t, api.mutations, 1)
			found := false
			for _, upsert := range api.mutations[0].Batch.Upserts {
				if upsert.ID.String() == string(block) {
					found = true
					require.Contains(t, string(upsert.SharedData), `"level":"LEVEL_2"`)
					require.Contains(t, upsert.Kind, "heading")
				}
			}
			require.True(t, found)
			found = false
			for _, upsert := range api.mutations[0].Batch.LocaleGroups[0].Upserts {
				if upsert.BlockID.String() == string(block) {
					found = true
					require.Contains(t, string(upsert.LocalizedData), "Heading")
				}
			}
			require.True(t, found)
		})
	}
	t.Run("new table structure and content", func(t *testing.T) {
		port, api, identity := newExactPagePortForLocale(t, "en", true)
		section, block := core.BlockID(uuid.NewString()), core.BlockID(uuid.NewString())
		row, cell := core.RelationItemID(uuid.NewString()), core.RelationItemID(uuid.NewString())
		rowValue := func(cellValue core.Value) core.Value {
			return core.Object(core.ObjectValue("rows", core.List(core.StableItem(row, core.Object(core.ObjectValue("id", core.Text(string(row))), core.ObjectValue("cells", core.List(core.StableItem(cell, cellValue))))))))
		}
		shared := rowValue(core.Object(core.ObjectValue("id", core.Text(string(cell))), core.ObjectValue("header", core.Boolean(true)), core.ObjectValue("colspan", core.Number("1")), core.ObjectValue("rowspan", core.Number("1"))))
		shared.Object = append(shared.Object, core.ObjectValue("headerRows", core.Number("1")))
		locale := rowValue(core.Object(core.ObjectValue("id", core.Text(string(cell))), core.ObjectValue("content", core.RichText(core.InlineText("Cell")))))
		request := pageExactApplyRequest(identity, api.state, nil, core.InsertBlockOperation(section, "rich-text", "", ""), core.InsertBlockOperation(block, "table", section, ""), core.SetFieldOperation(block, "table", shared), core.SetFieldOperation(block, "tableContent", locale))
		service, err := core.NewService(port)
		require.NoError(t, err)
		validation, err := service.Validate(t.Context(), request)
		require.NoError(t, err)
		require.Empty(t, validation.Issues)
		require.Len(t, api.mutations, 1)
		found := false
		for _, upsert := range api.mutations[0].Batch.Upserts {
			if upsert.ID.String() == string(block) {
				found = true
				require.Contains(t, string(upsert.SharedData), string(row))
				require.Contains(t, string(upsert.SharedData), string(cell))
				require.Contains(t, string(upsert.SharedData), `"header":true`)
			}
		}
		require.True(t, found)
		found = false
		for _, upsert := range api.mutations[0].Batch.LocaleGroups[0].Upserts {
			if upsert.BlockID.String() == string(block) {
				found = true
				require.Contains(t, string(upsert.LocalizedData), "Cell")
				require.Contains(t, string(upsert.LocalizedData), string(cell))
			}
		}
		require.True(t, found)
	})
}

func TestPageRichValidationRejectsBeforeCompilation(t *testing.T) {
	for _, test := range []struct {
		name   string
		parent string
		math   bool
		table  bool
	}{
		{name: "missing parent"}, {name: "non rich section", parent: "existing"}, {name: "nested inline math", math: true}, {name: "table cell inline math", math: true, table: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			port, api, identity := newExactPagePortForLocale(t, "en", true)
			block := core.BlockID(uuid.NewString())
			parent := core.BlockID(test.parent)
			var ops []core.Operation
			code := core.IssueInvalidBlockRelation
			if test.parent == "existing" {
				parent = core.BlockID(api.state.Document.Base.Nodes[0].Section.Id)
			}
			ops = append(ops, core.SetFieldOperation("document", "title", core.Text("private text")))
			index := 1
			if test.math {
				parent = core.BlockID(uuid.NewString())
				kind, field := core.BlockKind("paragraph"), core.FieldID("content")
				value := core.RichText(core.Bold(core.InlineMath("private formula")))
				if test.table {
					kind, field = "table", "tableContent"
					row, cell := core.RelationItemID(uuid.NewString()), core.RelationItemID(uuid.NewString())
					value = core.Object(core.ObjectValue("rows", core.List(core.StableItem(row, core.Object(
						core.ObjectValue("id", core.Text(string(row))),
						core.ObjectValue("cells", core.List(core.StableItem(cell, core.Object(core.ObjectValue("id", core.Text(string(cell))), core.ObjectValue("content", value))))),
					)))))
				}
				ops = append(ops, core.InsertBlockOperation(parent, "rich-text", "", ""), core.InsertBlockOperation(block, kind, parent, ""), core.SetFieldOperation(block, field, value))
				index = 3
				code = core.IssueInvalidOperation
			} else {
				ops = append(ops, core.InsertBlockOperation(block, "heading", parent, ""))
			}
			request := pageExactApplyRequest(identity, api.state, nil, ops...)
			service, err := core.NewService(port)
			require.NoError(t, err)
			validation, err := service.Validate(t.Context(), request)
			require.NoError(t, err)
			require.Len(t, validation.Issues, 1)
			require.Equal(t, code, validation.Issues[0].Code)
			require.Equal(t, index, validation.Issues[0].Operation)
			require.Equal(t, string(block), validation.Issues[0].Handle)
			require.Empty(t, api.mutations)
			_, err = service.Apply(t.Context(), request)
			var rejected *core.ValidationError
			require.ErrorAs(t, err, &rejected)
			require.Equal(t, validation.Issues, rejected.Result.Issues)
			require.Empty(t, api.mutations)
		})
	}
}

func TestPageRichCatalogGeneratedConstraints(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	catalog := codec.Catalog()
	for _, field := range catalog.Fields {
		if field.BlockKind == "heading" && field.Field == "level" {
			require.Equal(t, []any{float64(1), float64(2), float64(3)}, field.Schema.EnumValues)
		}
	}
	descriptor := contentv1.DescribePageCatalog()
	require.NotEmpty(t, descriptor.Sections)
	var found bool
	for _, field := range catalog.Fields {
		if field.BlockKind == "map" && field.Field == pageSectionDataField {
			for _, nested := range field.Schema.Fields {
				if nested.Field == "props" {
					for _, prop := range nested.Schema.Fields {
						if prop.Field == "zoom" {
							found = true
							require.Equal(t, float64(-2), *prop.Schema.Minimum)
							require.Equal(t, float64(22), *prop.Schema.Maximum)
						}
					}
				}
			}
		}
	}
	require.True(t, found)
}
