package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	adapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

const richToolSection = "11111111-1111-4111-8111-111111111111"
const richToolHeading = "22222222-2222-4222-8222-222222222222"
const richToolParagraph = "33333333-3333-4333-8333-333333333333"

func TestFocusedRichToolsCompileForPageAndPost(t *testing.T) {
	for _, profile := range []string{"page", "post"} {
		for _, name := range []string{ToolHeadingCreate, ToolHeadingUpdate, ToolTableCreate} {
			t.Run(profile+"/"+name, func(t *testing.T) {
				revision := uuid.NewString()
				app := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: core.Revision(uuid.NewString()), Changed: true}}
				args := toolArguments(t, `{"document_type":"`+profile+`","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","expected_document_revision":"`+revision+`"}`)
				if name == ToolHeadingUpdate {
					args["block_id"] = json.RawMessage(`"` + richToolHeading + `"`)
				} else {
					args["after_block_id"] = json.RawMessage(`"` + richToolParagraph + `"`)
					if profile == "page" {
						args["parent_block_id"] = json.RawMessage(`"` + richToolSection + `"`)
					}
				}
				if name == ToolTableCreate {
					args["rows"] = json.RawMessage(`[["A","B"],["C",""]]`)
					args["header_row"] = json.RawMessage(`true`)
					args["header_column"] = json.RawMessage(`true`)
				} else {
					args["text"] = json.RawMessage(`"Heading"`)
					args["level"] = json.RawMessage(`3`)
					args["alignment"] = json.RawMessage(`"center"`)
				}
				result, err := mustAIDocumentTools(t, app).CallTool(t.Context(), mcpserver.Principal{}, name, args)
				if err != nil || result.IsError || app.applyCalls != 1 {
					t.Fatalf("result=%+v err=%v calls=%d", result, err, app.applyCalls)
				}
				request := app.applyRequest
				if request.ExpectedDocumentRevision != core.Revision(revision) || request.Profile != core.Domain(profile) {
					t.Fatalf("request=%+v", request)
				}
				block := core.BlockID(richToolHeading)
				if name != ToolHeadingUpdate {
					block = request.Operations[0].InsertBlock.Block
					if _, err := uuid.Parse(string(block)); err != nil || result.StructuredContent["block_id"] != string(block) {
						t.Fatalf("created handle=%s result=%+v", block, result)
					}
					if request.Operations[0].InsertBlock.After != richToolParagraph {
						t.Fatal("predecessor lost")
					}
				}
				batch := compileFocusedRichRequest(t, request)
				found := false
				for _, upsert := range batch.Upserts {
					if upsert.ID.String() == string(block) {
						found = true
						var data map[string]any
						if err := json.Unmarshal(upsert.SharedData, &data); err != nil {
							t.Fatal(err)
						}
						if name != ToolTableCreate {
							props := data["heading"].(map[string]any)["props"].(map[string]any)
							if props["level"] != "LEVEL_3" || props["textAlignment"] != "TEXT_ALIGNMENT_CENTER" {
								t.Fatalf("heading data=%s", upsert.SharedData)
							}
						}
					}
				}
				if !found {
					t.Fatal("focused block did not compile")
				}
				localeFound := false
				for _, group := range batch.LocaleGroups {
					for _, upsert := range group.Upserts {
						if upsert.BlockID.String() == string(block) {
							localeFound = true
							want := []string{"Heading"}
							if name == ToolTableCreate {
								want = []string{`"A"`, `"B"`, `"C"`}
							}
							for _, text := range want {
								if !strings.Contains(string(upsert.LocalizedData), text) {
									t.Fatalf("locale content missing %s: %s", text, upsert.LocalizedData)
								}
							}
						}
					}
				}
				if !localeFound {
					t.Fatal("focused block locale content missing")
				}
				if name == ToolTableCreate {
					table := request.Operations[1].SetField.Value
					rows := richObjectField(t, table, "rows").List
					if len(rows) != 2 {
						t.Fatal(rows)
					}
					ids := map[string]bool{}
					for r, row := range rows {
						for col, cell := range richObjectField(t, row.Value, "cells").List {
							if _, err := uuid.Parse(string(cell.ID)); err != nil || ids[string(cell.ID)] {
								t.Fatal("cell UUID is not unique")
							}
							ids[string(cell.ID)] = true
							if richObjectField(t, cell.Value, "header").Boolean != (r == 0 || col == 0) {
								t.Fatal("incorrect header flags")
							}
						}
					}
					if richObjectField(t, table, "headerRows").Text != "1" || richObjectField(t, table, "headerColumns").Text != "1" {
						t.Fatal("header counts missing")
					}
				}
			})
		}
	}
}

func compileFocusedRichRequest(t *testing.T, request core.ApplyRequest) contentblock.Batch {
	t.Helper()
	graph := &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{
		{Block: &contentv1.RichTextBlock{Id: richToolHeading, Value: &contentv1.RichTextBlock_Heading{Heading: &contentv1.HeadingBlock{Props: &contentv1.HeadingProps{}}}}, Placement: &contentv1.ContentBlockPlacement{}},
		{Block: &contentv1.RichTextBlock{Id: richToolParagraph, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}}, Placement: &contentv1.ContentBlockPlacement{Index: 1}},
	}}
	rich := &contentv1.LocalizedRichTextDocument{Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en", Base: graph, LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en"}}
	var catalog core.Catalog
	var nodes []core.Node
	var batch contentblock.Batch
	var issues []core.OperationIssue
	var err error
	if request.Profile == core.DomainPage {
		codec, e := adapter.NewPageCodec()
		if e != nil {
			t.Fatal(e)
		}
		document := &contentv1.LocalizedPageDocument{BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en", Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{Section: &contentv1.PageSection{Id: richToolSection, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{Props: &contentv1.RichTextSectionProps{}, Blocks: graph}}}, Placement: &contentv1.PageSectionPlacement{}}}}, LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en"}}
		catalog = codec.Catalog()
		nodes, err = codec.Project(document)
		if err != nil {
			t.Fatal(err)
		}
		batch, issues, err = codec.Compile(t.Context(), uuid.New(), document, core.LocaleRoleSource, request.ExpectedDocumentRevision, uuid.New(), request.Operations)
	} else {
		codec, e := adapter.NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
		if e != nil {
			t.Fatal(e)
		}
		catalog = codec.Catalog()
		nodes, err = codec.Project(rich)
		if err != nil {
			t.Fatal(err)
		}
		batch, issues, err = codec.Compile(uuid.New(), rich, core.LocaleRoleSource, request.ExpectedDocumentRevision, uuid.New(), request.Operations)
	}
	if err != nil || len(issues) != 0 {
		t.Fatalf("compile err=%v issues=%+v", err, issues)
	}
	validation := core.ValidateOperations(core.Document{Identity: request.Identity(), DocumentRevision: request.ExpectedDocumentRevision, SourceLocale: "en", Locale: "en", LocaleExists: true, Catalog: catalog, Nodes: nodes}, request)
	if !validation.Valid() {
		t.Fatalf("focused request invalid: %+v", validation)
	}
	return batch
}

func richObjectField(t *testing.T, value core.Value, field core.FieldID) core.Value {
	t.Helper()
	for _, nested := range value.Object {
		if nested.ID == field {
			return nested.Value
		}
	}
	t.Fatalf("missing %s", field)
	return core.Value{}
}

func TestTableAppendUsesLastSiblingAndChecksRevisions(t *testing.T) {
	for _, profile := range []string{"page", "post"} {
		for _, stale := range []bool{false, true} {
			t.Run(profile+"/"+map[bool]string{false: "append", true: "conflict"}[stale], func(t *testing.T) {
				parent := core.BlockID("")
				if profile == "page" {
					parent = richToolSection
				}
				revision := uuid.NewString()
				app := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: core.Revision(uuid.NewString())}, readResult: core.Projection{DocumentRevision: core.Revision(revision), Nodes: []core.Node{{ID: "document", Order: 99}, {ID: richToolParagraph, Parent: parent, Order: 1}, {ID: richToolHeading, Parent: parent, Order: 0}, {ID: "other-parent", Parent: "other", Order: 999}}}}
				args := toolArguments(t, `{"document_type":"`+profile+`","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","expected_document_revision":"`+revision+`","rows":[["Cell"]]}`)
				if parent != "" {
					args["parent_block_id"] = json.RawMessage(`"` + string(parent) + `"`)
				}
				if stale {
					app.readResult.DocumentRevision = "new-revision"
				}
				result, err := mustAIDocumentTools(t, app).CallTool(t.Context(), mcpserver.Principal{}, ToolTableCreate, args)
				if err != nil {
					t.Fatal(err)
				}
				if stale {
					if app.applyCalls != 0 || result.StructuredContent["status"] != "sync_required" {
						t.Fatalf("stale append applied: %+v", result)
					}
					return
				}
				if app.applyCalls != 1 || app.applyRequest.Operations[0].InsertBlock.After != richToolParagraph {
					t.Fatalf("wrong append placement: %+v", app.applyRequest)
				}
			})
		}
	}
}

func TestFocusedRichToolsRejectInvalidInputWithoutApply(t *testing.T) {
	for _, test := range []struct {
		name  string
		tool  string
		extra string
	}{
		{"missing Page parent", ToolHeadingCreate, `"text":"x","level":1`},
		{"invalid level", ToolHeadingUpdate, `"block_id":"heading","text":"x","level":4`},
		{"null alignment", ToolHeadingUpdate, `"block_id":"heading","text":"x","level":1,"alignment":null`},
		{"ragged table", ToolTableCreate, `"parent_block_id":"section","rows":[["a"],["b","c"]]`},
		{"null cell", ToolTableCreate, `"parent_block_id":"section","rows":[[null]]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := &recordingAIDocumentApplication{}
			args := toolArguments(t, `{"document_type":"page","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","expected_document_revision":"revision-a",`+test.extra+`}`)
			_, err := mustAIDocumentTools(t, app).CallTool(t.Context(), mcpserver.Principal{}, test.tool, args)
			var execution *mcpserver.ToolExecutionError
			if !errors.As(err, &execution) || app.applyCalls != 0 || app.readCalls != 0 {
				t.Fatalf("invalid input reached application: err=%v", err)
			}
		})
	}
}

func TestPageCatalogExposesEnumsBoundsIdentityAndParentKinds(t *testing.T) {
	codec, err := adapter.NewPageCodec()
	if err != nil {
		t.Fatal(err)
	}
	document := documentCatalogFixture()
	document.Catalog = codec.Catalog()
	document.Nodes = nil
	result, err := mustDocumentCatalogTools(t, &documentCatalogPort{document: document}).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentCatalog, toolArguments(t, `{"document_type":"page","document_id":"44444444-4444-4444-8444-444444444444","locale":"ko"}`))
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]map[string]any{}
	for _, raw := range result.StructuredContent["fields"].([]any) {
		field := raw.(map[string]any)
		fields[field["block_kind"].(string)+"/"+field["field"].(string)] = field
	}
	levels := fields["heading/level"]["enum_values"].([]any)
	if len(levels) != 3 || levels[0] != float64(1) || levels[2] != float64(3) || fields["heading/level"]["kind"] != "number" {
		t.Fatalf("heading levels=%v", levels)
	}
	table := fields["table/table"]
	var rows map[string]any
	for _, raw := range table["fields"].([]any) {
		field := raw.(map[string]any)
		shape := field["schema"].(map[string]any)
		if field["field"] == "headerRows" && shape["minimum"] != float64(0) {
			t.Fatal("headerRows minimum missing")
		}
		if field["field"] == "rows" {
			rows = shape
		}
		if field["field"] == "columnWidths" && shape["item"].(map[string]any)["exclusive_minimum"] != float64(0) {
			t.Fatal("positive column width bound missing")
		}
	}
	if rows["identity"].(map[string]any)["field"] != "id" {
		t.Fatal("row identity missing")
	}
	required := false
	for _, raw := range rows["item"].(map[string]any)["fields"].([]any) {
		field := raw.(map[string]any)
		if field["field"] == "id" {
			required = field["schema"].(map[string]any)["required"] == true
		}
	}
	if !required {
		t.Fatal("row identity must be marked required")
	}
	rules := map[string]map[string]any{}
	for _, raw := range result.StructuredContent["block_rules"].([]any) {
		rule := raw.(map[string]any)
		rules[rule["kind"].(string)] = rule
	}
	if rules["heading"]["category"] != "rich_block" || rules["heading"]["allowed_parents"].([]any)[0] != "rich-text" || rules["rich-text"]["category"] != "page_section" || rules["page-column"]["category"] != "page_column" {
		t.Fatalf("parent rules=%+v", rules)
	}
}

func TestFocusedRichRejectionLogsIssueIdentityWithoutText(t *testing.T) {
	// The production normalizer drops slice and struct values, so the log is
	// captured through it to check what actually reaches Loki.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&logs, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	app := &recordingAIDocumentApplication{applyError: &core.ValidationError{Result: core.ValidationResult{Issues: []core.OperationIssue{{Operation: 2, Code: core.IssueInvalidOperation, Handle: "heading", Message: "private document text"}}}}}
	args := toolArguments(t, `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","expected_document_revision":"revision-a","block_id":"heading","text":"private input text","level":2}`)
	result, err := mustAIDocumentTools(t, app).CallTool(t.Context(), mcpserver.Principal{}, ToolHeadingUpdate, args)
	if err != nil || !result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if strings.Contains(logs.String(), "private") {
		t.Fatal("rejection log contains document text")
	}
	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["msg"] != "MCP document mutation rejected" || entry["issues"] != "op2:invalid_operation@heading" || entry["issue_count"] != float64(1) ||
		entry["document_id"] != "44444444-4444-4444-8444-444444444444" || entry["locale"] != "en" || entry["domain"] != "post" {
		t.Fatalf("issue identities missing: %s", logs.String())
	}
}

func TestHeadingUpdateWithoutLevelWritesOnlyLocalizedText(t *testing.T) {
	// A translated heading keeps its shared level, so the update must not write it.
	app := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: core.Revision(uuid.NewString()), Changed: true}}
	args := toolArguments(t, `{"document_type":"page","document_id":"44444444-4444-4444-8444-444444444444","locale":"ko","expected_document_revision":"`+uuid.NewString()+`","text":"제목"}`)
	args["block_id"] = json.RawMessage(`"` + richToolHeading + `"`)
	result, err := mustAIDocumentTools(t, app).CallTool(t.Context(), mcpserver.Principal{}, ToolHeadingUpdate, args)
	if err != nil || result.IsError || app.applyCalls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, app.applyCalls)
	}
	operations := app.applyRequest.Operations
	if len(operations) != 1 || operations[0].SetField == nil || operations[0].SetField.Target.Field != "content" {
		t.Fatalf("translated heading update wrote shared fields: %+v", operations)
	}
}

func TestTableAppendTurnsAnInvalidatedOutlineCursorIntoSyncRequired(t *testing.T) {
	app := &recordingAIDocumentApplication{readError: &core.CursorError{Code: "invalid_cursor", CurrentDocumentRevision: "current"}}
	args := toolArguments(t, `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","expected_document_revision":"`+uuid.NewString()+`","rows":[["Cell"]]}`)
	result, err := mustAIDocumentTools(t, app).CallTool(t.Context(), mcpserver.Principal{}, ToolTableCreate, args)
	if err != nil {
		t.Fatal(err)
	}
	if app.applyCalls != 0 || result.StructuredContent["status"] != "sync_required" {
		t.Fatalf("invalidated cursor applied or returned %+v", result)
	}
}
