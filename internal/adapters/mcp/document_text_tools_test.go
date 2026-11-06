package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/documenttext"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

const (
	textDocumentA = "11111111-1111-4111-8111-111111111111"
	textDocumentB = "22222222-2222-4222-8222-222222222222"
)

// textDocumentStore holds text per document and locale and behaves like the
// domains in what these tools depend on: a read carries revisions, Apply refuses
// old ones, and writing the source moves every translation's revision.
// textParagraphBlock is a content block, whose handle is a UUID.
const textParagraphBlock = "55555555-5555-4555-8555-555555555555"

type textDocumentStore struct {
	text       map[string]map[core.Locale]string
	source     core.Locale
	revision   map[string]int
	targetRev  map[string]map[core.Locale]int
	applied    []core.ApplyRequest
	translated []translationapp.EditTranslationUnitsCommand
	// conflictOnce makes the first Apply fail as a concurrent edit would.
	conflictOnce  bool
	applyCalls    int
	failApply     map[string]error
	failTranslate error
}

func newTextDocumentStore(documents ...string) *textDocumentStore {
	store := &textDocumentStore{
		text: map[string]map[core.Locale]string{}, source: "en", revision: map[string]int{},
		targetRev: map[string]map[core.Locale]int{}, failApply: map[string]error{},
	}
	for _, id := range documents {
		store.text[id] = map[core.Locale]string{"en": "Sign up today", "ko": "오늘 가입하세요"}
		store.targetRev[id] = map[core.Locale]int{"ko": 1}
		store.revision[id] = 1
	}
	return store
}

func (store *textDocumentStore) snapshot() *textDocumentStore {
	copied := *store
	copied.text = map[string]map[core.Locale]string{}
	copied.targetRev = map[string]map[core.Locale]int{}
	copied.revision = map[string]int{}
	for id, locales := range store.text {
		copied.text[id] = map[core.Locale]string{}
		copied.targetRev[id] = map[core.Locale]int{}
		for locale, text := range locales {
			copied.text[id][locale] = text
		}
		for locale, revision := range store.targetRev[id] {
			copied.targetRev[id][locale] = revision
		}
		copied.revision[id] = store.revision[id]
	}
	copied.applied = slices.Clone(store.applied)
	copied.translated = slices.Clone(store.translated)
	return &copied
}

// transaction restores everything on an error, as a rollback does.
func (store *textDocumentStore) transaction(ctx context.Context, work func(context.Context) error) error {
	before := store.snapshot()
	if err := work(ctx); err != nil {
		store.text, store.targetRev, store.revision = before.text, before.targetRev, before.revision
		store.applied, store.translated = before.applied, before.translated
		return err
	}
	return nil
}

func revisionOf(value int) core.Revision {
	return core.Revision(strings.Repeat("r", value))
}

func (store *textDocumentStore) Open(_ context.Context, request core.OpenRequest) (core.OpenMetadata, error) {
	id := string(request.Document.Reference)
	return core.OpenMetadata{DocumentRevision: revisionOf(store.revision[id]), SourceLocale: store.source}, nil
}

func (store *textDocumentStore) Read(_ context.Context, request core.ReadRequest) (core.Projection, error) {
	id := string(request.Document.Reference)
	projection := core.Projection{DocumentRevision: revisionOf(store.revision[id]), SourceLocale: store.source}
	if request.Locale != store.source {
		target := revisionOf(store.targetRev[id][request.Locale])
		projection.TargetRevision = &target
	}
	if text, ok := store.text[id][request.Locale]; ok {
		projection.Nodes = []core.Node{{ID: textParagraphBlock, Kind: "paragraph", Localized: []core.FieldValue{{
			ID: "content", Value: core.RichText(core.InlineText("See "), core.Link("https://old.example", core.InlineText("docs")), core.InlineText(" "+text)),
		}}}}
	}
	return projection, nil
}

func (store *textDocumentStore) Validate(context.Context, core.ApplyRequest) (core.ValidationResult, error) {
	return core.ValidationResult{}, errors.New("unused")
}

func (store *textDocumentStore) Apply(_ context.Context, request core.ApplyRequest) (core.ApplyResult, error) {
	id := string(request.Document)
	store.applyCalls++
	if store.conflictOnce {
		store.conflictOnce = false
		return core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{Code: core.ConflictDocumentRevision, CurrentDocumentRevision: revisionOf(store.revision[id])}}
	}
	if err := store.failApply[id]; err != nil {
		return core.ApplyResult{}, err
	}
	if request.ExpectedDocumentRevision != revisionOf(store.revision[id]) {
		return core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{Code: core.ConflictDocumentRevision, CurrentDocumentRevision: revisionOf(store.revision[id])}}
	}
	if request.Locale != store.source && (request.ExpectedTargetRevision == nil || *request.ExpectedTargetRevision != revisionOf(store.targetRev[id][request.Locale])) {
		return core.ApplyResult{}, &core.ConflictError{Conflict: core.Conflict{Code: core.ConflictTargetRevision, CurrentDocumentRevision: revisionOf(store.revision[id])}}
	}
	store.applied = append(store.applied, request)
	if request.Locale == store.source {
		store.revision[id]++
		for locale := range store.targetRev[id] {
			store.targetRev[id][locale]++
		}
	} else {
		store.targetRev[id][request.Locale]++
	}
	result := core.ApplyResult{DocumentRevision: revisionOf(store.revision[id]), Changed: true}
	if request.Locale != store.source {
		target := revisionOf(store.targetRev[id][request.Locale])
		result.TargetRevision = &target
	}
	return result, nil
}

func (store *textDocumentStore) EditEntityTranslationUnits(_ context.Context, command translationapp.EditTranslationUnitsCommand) (map[string]translationapp.XLIFFImport, error) {
	if store.failTranslate != nil {
		return nil, store.failTranslate
	}
	store.translated = append(store.translated, command)
	return map[string]translationapp.XLIFFImport{"ko": {TargetRevision: "translated", Changed: true}}, nil
}

func mustTextTools(t *testing.T, store *textDocumentStore) *DocumentTextTools {
	t.Helper()
	units, err := NewDocumentTranslationUnits(store)
	if err != nil {
		t.Fatal(err)
	}
	editor, err := documenttext.NewService(store, units, store.transaction)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := NewDocumentTextTools(editor)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

type textEditOutput struct {
	Documents []textEditDocumentResult `json:"documents"`
	Applied   int                      `json:"applied"`
	Failed    int                      `json:"failed"`
}

func callTextTool(t *testing.T, store *textDocumentStore, tool, arguments string) (mcpserver.ToolResult, textEditOutput) {
	t.Helper()
	result, err := mustTextTools(t, store).CallTool(t.Context(), mcpserver.Principal{}, tool, toolArguments(t, arguments))
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var output textEditOutput
	if text, ok := result.Content[0]["text"].(string); ok {
		if err := json.Unmarshal([]byte(text), &output); err != nil {
			t.Fatal(err)
		}
	}
	return result, output
}

func replaceEditJSON(document string, locale, find, replace string) string {
	return `{"document_type":"page","document_id":"` + document + `","locale":"` + locale + `","find":"` + find + `","replace":"` + replace + `"}`
}

func editsJSON(edits ...string) string {
	return `{"edits":[` + strings.Join(edits, ",") + `]}`
}

func TestTextReplaceChangesManyDocumentsInOneCallWithNoRead(t *testing.T) {
	store := newTextDocumentStore(textDocumentA, textDocumentB)

	result, output := callTextTool(t, store, ToolTextReplace, editsJSON(
		replaceEditJSON(textDocumentA, "en", "Sign up", "Join"),
		replaceEditJSON(textDocumentB, "en", "Sign up", "Join"),
	))

	if result.IsError || output.Applied != 2 || output.Failed != 0 || len(output.Documents) != 2 {
		t.Fatalf("output = %+v", output)
	}
	if len(store.applied) != 2 || store.applied[0].Document != textDocumentA || store.applied[1].Document != textDocumentB {
		t.Fatalf("applied = %+v", store.applied)
	}
	set := store.applied[0].Operations[0].SetField
	if set.Target.Block != textParagraphBlock || !strings.Contains(string(mustJSON(t, set.Value)), "Join today") {
		t.Fatalf("set = %+v", set)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// One document's failure must neither hide what the others did nor be lost.
func TestTextReplaceReportsEachDocumentSeparately(t *testing.T) {
	store := newTextDocumentStore(textDocumentA, textDocumentB)

	result, output := callTextTool(t, store, ToolTextReplace, editsJSON(
		replaceEditJSON(textDocumentA, "en", "Sign up", "Join"),
		replaceEditJSON(textDocumentB, "en", "missing words", "Join"),
	))

	if result.IsError || output.Applied != 1 || output.Failed != 1 {
		t.Fatalf("result = %+v, output = %+v", result, output)
	}
	failed := output.Documents[1]
	if failed.Status != documentStatusFailed || failed.EditIndex == nil || *failed.EditIndex != 1 || !strings.Contains(failed.Error, "does not occur") {
		t.Fatalf("failed = %+v", failed)
	}
	if store.text[textDocumentA] == nil || len(store.applied) != 1 {
		t.Fatalf("the good document should have applied: %+v", store.applied)
	}
}

func TestTextReplaceIsAnErrorOnlyWhenNothingApplied(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	result, output := callTextTool(t, store, ToolTextReplace, editsJSON(replaceEditJSON(textDocumentA, "en", "missing", "x")))
	if !result.IsError || output.Failed != 1 {
		t.Fatalf("result = %+v, output = %+v", result, output)
	}
}

// Two writes of one whole field value would lose the first edit.
func TestTextReplaceCombinesEditsOfOneLocaleIntoOneWrite(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)

	_, output := callTextTool(t, store, ToolTextReplace, editsJSON(
		replaceEditJSON(textDocumentA, "en", "Sign up", "Join"),
		replaceEditJSON(textDocumentA, "en", "today", "now"),
	))

	if output.Applied != 1 || len(store.applied) != 1 || len(store.applied[0].Operations) != 1 {
		t.Fatalf("applied = %+v", store.applied)
	}
	if text := string(mustJSON(t, store.applied[0].Operations[0].SetField.Value)); !strings.Contains(text, "Join now") {
		t.Fatalf("value = %s", text)
	}
}

// Writing the source moves every translation's revision, so a translation read
// before that must be written first or the source edit would refuse it.
func TestTextReplaceWritesTranslationsBeforeTheSource(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)

	result, output := callTextTool(t, store, ToolTextReplace, editsJSON(
		replaceEditJSON(textDocumentA, "en", "Sign up", "Join"),
		replaceEditJSON(textDocumentA, "ko", "가입", "참여"),
	))

	if result.IsError || output.Applied != 1 {
		t.Fatalf("output = %+v", output)
	}
	if len(store.applied) != 2 || store.applied[0].Locale != "ko" || store.applied[1].Locale != "en" {
		t.Fatalf("write order = %+v", store.applied)
	}
}

func TestTextReplaceWritesTranslationsTogetherWithTheSource(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)

	result, output := callTextTool(t, store, ToolTextReplace, `{"edits":[{"document_type":"page","document_id":"`+textDocumentA+`","locale":"en","find":"Sign up","replace":"Join","translations":{"ko":"오늘 참여하세요"}}]}`)

	if result.IsError || output.Applied != 1 || output.Documents[0].Translations["ko"].TargetRevision != "translated" {
		t.Fatalf("output = %+v", output)
	}
	if len(store.translated) != 1 {
		t.Fatalf("translated = %+v", store.translated)
	}
	edit := store.translated[0].Edits[0]
	if edit.SourceContains != "Join" || edit.Block != textParagraphBlock || edit.Targets["ko"] != "오늘 참여하세요" {
		t.Fatalf("unit edit = %+v", edit)
	}
}

// The point of the shared transaction: a failed translation write must not leave
// the source changed. The fake rolls back only if the tool returns the error
// through the transaction.
func TestTextReplaceLeavesTheSourceUnchangedWhenItsTranslationFails(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	store.failTranslate = domainerr.InvalidField("edits", "no unit has the source text")

	result, output := callTextTool(t, store, ToolTextReplace, `{"edits":[{"document_type":"page","document_id":"`+textDocumentA+`","locale":"en","find":"Sign up","replace":"Join","translations":{"ko":"x"}}]}`)

	if !result.IsError || output.Failed != 1 || !strings.Contains(output.Documents[0].Error, "no unit has the source text") {
		t.Fatalf("result = %+v, output = %+v", result, output)
	}
	if len(store.applied) != 0 || store.revision[textDocumentA] != 1 {
		t.Fatalf("the source edit survived: %+v, revision %d", store.applied, store.revision[textDocumentA])
	}
}

func TestTextReplaceRefusesTranslationsOnATranslationEdit(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)

	_, output := callTextTool(t, store, ToolTextReplace, `{"edits":[{"document_type":"page","document_id":"`+textDocumentA+`","locale":"ko","find":"가입","replace":"참여","translations":{"ja":"x"}}]}`)

	if output.Failed != 1 || !strings.Contains(output.Documents[0].Error, "source locale en") || len(store.applied) != 0 {
		t.Fatalf("output = %+v, applied = %+v", output, store.applied)
	}
}

func TestTextReplaceRetriesOnceWhenTheDocumentMoves(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	store.conflictOnce = true

	result, output := callTextTool(t, store, ToolTextReplace, editsJSON(replaceEditJSON(textDocumentA, "en", "Sign up", "Join")))

	if result.IsError || output.Applied != 1 || len(store.applied) != 1 || store.applyCalls != 2 {
		t.Fatalf("output = %+v, applied = %+v, calls = %d", output, store.applied, store.applyCalls)
	}
}

func TestTextReplaceGivesUpAfterOneRetry(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	store.failApply[textDocumentA] = &core.ConflictError{Conflict: core.Conflict{Code: core.ConflictDocumentRevision, CurrentDocumentRevision: "new"}}

	_, output := callTextTool(t, store, ToolTextReplace, editsJSON(replaceEditJSON(textDocumentA, "en", "Sign up", "Join")))

	if output.Failed != 1 || store.applyCalls != 2 || !strings.Contains(output.Documents[0].Error, "send it again") {
		t.Fatalf("output = %+v, calls = %d", output, store.applyCalls)
	}
}

func TestTextReplaceKeepsAnUnexpectedFailureOutOfTheResult(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	store.failApply[textDocumentA] = errors.New("pq: secret connection detail")

	_, output := callTextTool(t, store, ToolTextReplace, editsJSON(replaceEditJSON(textDocumentA, "en", "Sign up", "Join")))

	if output.Failed != 1 || strings.Contains(output.Documents[0].Error, "secret") {
		t.Fatalf("output = %+v", output)
	}
}

func TestTextEditsAreBounded(t *testing.T) {
	store := newTextDocumentStore()
	edits := make([]string, 0, documenttext.MaxDocuments+1)
	for index := 0; index <= documenttext.MaxDocuments; index++ {
		edits = append(edits, replaceEditJSON(syntheticUUID(index), "en", "a", "b"))
	}
	if _, err := mustTextTools(t, store).CallTool(t.Context(), mcpserver.Principal{}, ToolTextReplace, toolArguments(t, editsJSON(edits...))); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("err = %v", err)
	}
	if _, err := mustTextTools(t, store).CallTool(t.Context(), mcpserver.Principal{}, ToolTextReplace, toolArguments(t, `{"edits":[]}`)); err == nil {
		t.Fatal("an empty call succeeded")
	}
	if _, err := mustTextTools(t, store).CallTool(t.Context(), mcpserver.Principal{}, ToolTextReplace, toolArguments(t, editsJSON(replaceEditJSON("not-a-uuid", "en", "a", "b")))); err == nil {
		t.Fatal("a document that is not a UUID was accepted")
	}
}

func syntheticUUID(index int) string {
	const hex = "0123456789abcdef"
	return "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa" + string(hex[index/16]) + string(hex[index%16])
}

func TestTextFormatChangesALinkAcrossLocalesWithNoRewrite(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	arguments := func(locale string) string {
		return `{"document_type":"page","document_id":"` + textDocumentA + `","locale":"` + locale + `","find_link":"https://old.example","link":"https://new.example"}`
	}

	result, output := callTextTool(t, store, ToolTextFormat, editsJSON(arguments("en"), arguments("ko")))

	if result.IsError || output.Applied != 1 || len(store.applied) != 2 {
		t.Fatalf("output = %+v", output)
	}
	for _, request := range store.applied {
		text := string(mustJSON(t, request.Operations[0].SetField.Value))
		if !strings.Contains(text, "https://new.example") || strings.Contains(text, "https://old.example") || !strings.Contains(text, `"text":"docs"`) {
			t.Fatalf("value = %s", text)
		}
	}
}

func TestTextFormatAddsAMarkToPartOfARun(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)

	_, output := callTextTool(t, store, ToolTextFormat, `{"edits":[{"document_type":"page","document_id":"`+textDocumentA+`","locale":"en","find":"today","add_marks":["bold"]}]}`)

	if output.Applied != 1 {
		t.Fatalf("output = %+v", output)
	}
	text := string(mustJSON(t, store.applied[0].Operations[0].SetField.Value))
	if !strings.Contains(text, `{"type":"bold","content":[{"type":"text","text":"today"}]}`) {
		t.Fatalf("value = %s", text)
	}
}

func TestTextFormatRefusesAnEditThatChangesNothing(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	_, output := callTextTool(t, store, ToolTextFormat, `{"edits":[{"document_type":"page","document_id":"`+textDocumentA+`","locale":"en","find":"today"}]}`)
	if output.Failed != 1 || !strings.Contains(output.Documents[0].Error, "name the formatting") {
		t.Fatalf("output = %+v", output)
	}
}

func TestTextEditDryRunChangesNothingAndShowsTheChange(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)

	result, output := callTextTool(t, store, ToolTextReplace, `{"dry_run":true,"edits":[`+replaceEditJSON(textDocumentA, "en", "Sign up", "Join")+`]}`)

	if result.IsError || output.Applied != 1 || output.Documents[0].Status != documentStatusWouldApply {
		t.Fatalf("output = %+v", output)
	}
	if len(store.applied) != 0 || store.revision[textDocumentA] != 1 {
		t.Fatalf("a dry run wrote: %+v, revision %d", store.applied, store.revision[textDocumentA])
	}
	changes := output.Documents[0].Locales[0].Changes
	if len(changes) != 1 || !strings.Contains(changes[0].Before, "Sign up today") || !strings.Contains(changes[0].After, "Join today") {
		t.Fatalf("changes = %+v", changes)
	}
}

// A dry run must check what the real edit would, including the translation write.
func TestTextEditDryRunReportsAFailureTheRealEditWouldHit(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	store.failTranslate = domainerr.InvalidField("edits", "no unit has the source text")

	_, output := callTextTool(t, store, ToolTextReplace, `{"dry_run":true,"edits":[{"document_type":"page","document_id":"`+textDocumentA+`","locale":"en","find":"Sign up","replace":"Join","translations":{"ko":"x"}}]}`)

	if output.Failed != 1 || !strings.Contains(output.Documents[0].Error, "no unit has the source text") {
		t.Fatalf("output = %+v", output)
	}
}

func TestTextEditLeavesPreviewsOutOfARealEdit(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	_, output := callTextTool(t, store, ToolTextReplace, editsJSON(replaceEditJSON(textDocumentA, "en", "Sign up", "Join")))
	if output.Documents[0].Locales[0].Changes != nil {
		t.Fatalf("changes = %+v", output.Documents[0].Locales[0].Changes)
	}
}

// Writing the source moves every translation's revision, so a result must not hand
// back one that write has already replaced.
func TestTextReplaceLeavesOutRevisionsTheSourceWriteMoved(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)

	result, output := callTextTool(t, store, ToolTextReplace, `{"edits":[`+replaceEditJSON(textDocumentA, "en", "Sign up", "Join")+`,`+replaceEditJSON(textDocumentA, "ko", "오늘", "지금")+`]}`)

	if result.IsError || output.Applied != 1 {
		t.Fatalf("result = %+v", result)
	}
	current := revisionOf(store.revision[textDocumentA])
	for _, locale := range output.Documents[0].Locales {
		if locale.DocumentRevision != current || locale.TargetRevision != nil {
			t.Fatalf("locale %s = %+v, current document revision %s", locale.Locale, locale, current)
		}
	}
}

// The writes of a dry run rolled back, so a revision from them exists nowhere and
// would only draw a conflict from the next edit.
func TestTextEditDryRunReturnsNoRevisions(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)

	_, output := callTextTool(t, store, ToolTextReplace, `{"dry_run":true,"edits":[`+replaceEditJSON(textDocumentA, "ko", "오늘", "지금")+`]}`)

	locale := output.Documents[0].Locales[0]
	if locale.DocumentRevision != "" || locale.TargetRevision != nil || len(locale.Changes) == 0 {
		t.Fatalf("locale = %+v", locale)
	}
}

func TestTextFormatRefusesASelectionMadeOnlyOfSpaces(t *testing.T) {
	store := newTextDocumentStore(textDocumentA)
	_, err := mustTextTools(t, store).CallTool(t.Context(), mcpserver.Principal{}, ToolTextFormat, toolArguments(t, `{"edits":[{"document_type":"page","document_id":"`+textDocumentA+`","locale":"en","find":"   ","add_marks":["bold"]}]}`))
	var execution *mcpserver.ToolExecutionError
	if !errors.As(err, &execution) || !strings.Contains(execution.Message, "needs block_id") {
		t.Fatalf("err = %v", err)
	}
}
