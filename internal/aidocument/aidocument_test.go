package aidocument

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func testDocument(locale Locale, exists bool) Document {
	document := Document{
		Identity:         DocumentIdentity{Domain: DomainPost, Reference: "doc-handle"},
		DocumentRevision: "rev-7",
		SourceLocale:     "ko",
		Locale:           locale,
		LocaleExists:     exists,
		Catalog: Catalog{
			Fingerprint: "catalog-a1",
			BlockKinds:  []BlockKind{"document", "paragraph", "heading"},
			Fields: []FieldRule{
				{BlockKind: "document", Field: "slug", ValueKind: ValueKindText, Ownership: FieldOwnershipShared},
				{BlockKind: "document", Field: "source_note", ValueKind: ValueKindText, Ownership: FieldOwnershipSource},
				{BlockKind: "document", Field: "title", ValueKind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true},
				{BlockKind: "document", Field: "hero", Ownership: FieldOwnershipShared, File: true},
				{BlockKind: "paragraph", Field: "style", ValueKind: ValueKindText, Ownership: FieldOwnershipShared},
				{BlockKind: "paragraph", Field: "content", ValueKind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true},
				{BlockKind: "paragraph", Field: "rich", ValueKind: ValueKindInline, Ownership: FieldOwnershipLocale, Translatable: true},
				{BlockKind: "paragraph", Field: "internal_note", ValueKind: ValueKindText, Ownership: FieldOwnershipLocale},
				{BlockKind: "paragraph", Field: "image", Ownership: FieldOwnershipShared, File: true},
				{BlockKind: "heading", Field: "content", ValueKind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true},
			},
			Relations: []RelationRule{
				{BlockKind: "document", Relation: "credits", ItemKinds: []RelationItemKind{"credit"}},
				{BlockKind: "paragraph", Relation: "links", ItemKinds: []RelationItemKind{"link"}},
			},
			RelationFields: []RelationFieldRule{
				{BlockKind: "document", Relation: "credits", ItemKind: "credit", Field: "role", ValueKind: ValueKindText, Ownership: FieldOwnershipShared},
				{BlockKind: "document", Relation: "credits", ItemKind: "credit", Field: "bio", ValueKind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true},
				{BlockKind: "document", Relation: "credits", ItemKind: "credit", Field: "avatar", Ownership: FieldOwnershipShared, File: true},
				{BlockKind: "paragraph", Relation: "links", ItemKind: "link", Field: "label", ValueKind: ValueKindText, Ownership: FieldOwnershipLocale, Translatable: true},
			},
		},
		Nodes: []Node{
			{
				ID: "paragraph-b", Kind: "paragraph", Parent: "root", Order: 1,
				Shared:    []FieldValue{{ID: "style", Value: Text("lead")}},
				Localized: []FieldValue{{ID: "content", Value: Text("")}},
				Files:     []FileBinding{{Field: "image", File: "file-handle-b"}},
			},
			{
				ID: "root", Kind: "document", Order: 0,
				Shared:    []FieldValue{{ID: "slug", Value: Text("hello")}},
				Localized: []FieldValue{{ID: "title", Value: Text("Hello")}},
				Files:     []FileBinding{{Field: "hero", File: "file-handle-hero"}},
				Relations: []Relation{{ID: "credits", Items: []RelationItem{
					{ID: "credit-primary", Kind: "credit", Order: 0, Shared: []FieldValue{{ID: "role", Value: Text("author")}}, Localized: []FieldValue{{ID: "bio", Value: Text("")}}, Files: []FileBinding{{Field: "avatar", File: "file-avatar"}}},
					{ID: "credit-secondary", Kind: "credit", Order: 1, Shared: []FieldValue{{ID: "role", Value: Text("editor")}}},
				}}},
			},
			{
				ID: "paragraph-a", Kind: "paragraph", Parent: "root", Order: 0,
				Localized: []FieldValue{
					{ID: "content", Value: Text("First")},
					{ID: "rich", Value: RichText(InlineText("See "), Bold(InlineText("this")), Link("https://example.com", Italic(InlineText("link"))), HardBreak(), InlineMath("x^2"), Placeholder("member-name"))},
				},
			},
		},
	}
	if locale != document.SourceLocale && exists {
		targetRevision := Revision("target-rev-7")
		document.TargetRevision = &targetRevision
	}
	if locale != document.SourceLocale && !exists {
		for index := range document.Nodes {
			document.Nodes[index].Localized = nil
			for relationIndex := range document.Nodes[index].Relations {
				for itemIndex := range document.Nodes[index].Relations[relationIndex].Items {
					document.Nodes[index].Relations[relationIndex].Items[itemIndex].Localized = nil
				}
			}
		}
	}
	return document
}

func applyRequest(locale Locale, revision Revision, operations ...Operation) ApplyRequest {
	request := ApplyRequest{
		Protocol: ProtocolVersion, Profile: DomainPost, Document: "doc-handle",
		Locale: locale, ExpectedDocumentRevision: revision, Operations: operations,
	}
	if locale != "ko" {
		targetRevision := Revision("target-rev-7")
		request.ExpectedTargetRevision = &targetRevision
	}
	return request
}

func missingTargetRequest(locale Locale, revision Revision, operations ...Operation) ApplyRequest {
	request := applyRequest(locale, revision, operations...)
	request.ExpectedTargetRevision = nil
	return request
}

func decodeOpenMetadataForTest(data []byte) (OpenMetadata, error) {
	var metadata OpenMetadata
	if err := decodeStrict(data, &metadata); err != nil {
		return OpenMetadata{}, err
	}
	if err := metadata.validate(); err != nil {
		return OpenMetadata{}, err
	}
	return metadata, nil
}

func decodeProjectionForTest(data []byte) (Projection, error) {
	var projection Projection
	if err := decodeStrict(data, &projection); err != nil {
		return Projection{}, err
	}
	if err := projection.validate(); err != nil {
		return Projection{}, err
	}
	projection.Nodes = canonicalNodes(projection.Nodes)
	return projection, nil
}

func TestProjectionRoundTripIsDeterministicCompactAndPreservesExplicitEmpty(t *testing.T) {
	document := testDocument("en", true)
	projection := Projection{
		Protocol: ProtocolVersion, Profile: document.Identity.Domain, Catalog: document.Catalog.Fingerprint,
		Document: document.Identity.Reference, DocumentRevision: document.DocumentRevision, TargetRevision: document.TargetRevision, SourceLocale: document.SourceLocale,
		Locale: document.Locale, LocaleRole: document.Role(), LocaleExists: document.LocaleExists,
		Mode: ReadBlocks, Nodes: document.Nodes,
	}

	first, err := EncodeProjection(projection)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeProjectionForTest(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeProjection(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("encoding is not byte deterministic:\n%s\n%s", first, second)
	}
	permuted := projection
	permuted.Nodes = append([]Node(nil), projection.Nodes...)
	for left, right := 0, len(permuted.Nodes)-1; left < right; left, right = left+1, right-1 {
		permuted.Nodes[left], permuted.Nodes[right] = permuted.Nodes[right], permuted.Nodes[left]
	}
	for index := range permuted.Nodes {
		for left, right := 0, len(permuted.Nodes[index].Localized)-1; left < right; left, right = left+1, right-1 {
			permuted.Nodes[index].Localized[left], permuted.Nodes[index].Localized[right] = permuted.Nodes[index].Localized[right], permuted.Nodes[index].Localized[left]
		}
	}
	third, err := EncodeProjection(permuted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, third) {
		t.Fatalf("equivalent node and field order encoded differently:\n%s\n%s", first, third)
	}
	if !bytes.Contains(first, []byte(`["content",["t",""]]`)) {
		t.Fatalf("explicit empty value was not preserved: %s", first)
	}
	for _, forbidden := range []string{"tiptap", "prosemirror", "yjs", "rawHtml", "base64", "data:"} {
		if strings.Contains(strings.ToLower(string(first)), strings.ToLower(forbidden)) {
			t.Fatalf("canonical projection exposed forbidden representation %q: %s", forbidden, first)
		}
	}

	var shape map[string]json.RawMessage
	if err := json.Unmarshal(first, &shape); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"c", "d", "dr", "l", "le", "lr", "m", "n", "next", "p", "s", "tr", "v"}
	if len(shape) != len(wantKeys) {
		t.Fatalf("unexpected envelope shape: %v", shape)
	}
	for _, key := range wantKeys {
		if _, ok := shape[key]; !ok {
			t.Fatalf("missing compact envelope key %q", key)
		}
	}
	var nodeShape []json.RawMessage
	if err := json.Unmarshal(shape["n"], &nodeShape); err != nil || len(nodeShape) == 0 {
		t.Fatalf("invalid node list: %v", err)
	}
	var firstNode []json.RawMessage
	if err := json.Unmarshal(nodeShape[0], &firstNode); err != nil || len(firstNode) != 8 {
		t.Fatalf("node is not the eight-slot compact tuple: %v %s", err, nodeShape[0])
	}

	verbose, err := json.Marshal(struct {
		Protocol         string
		Profile          Domain
		Catalog          string
		Document         DocumentReference
		DocumentRevision Revision
		TargetRevision   *Revision
		SourceLocale     Locale
		Locale           Locale
		LocaleRole       LocaleRole
		LocaleExists     bool
		ReadMode         ReadMode
		DocumentNodes    []Node
	}{ProtocolVersion, projection.Profile, projection.Catalog, projection.Document, projection.DocumentRevision, projection.TargetRevision,
		projection.SourceLocale, projection.Locale, projection.LocaleRole, projection.LocaleExists, projection.Mode, projection.Nodes})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) >= len(verbose) {
		t.Fatalf("compact projection is not smaller than equivalent descriptive envelope: compact=%d verbose=%d", len(first), len(verbose))
	}
}

func TestApplyRequestRoundTripsEveryTypedOperation(t *testing.T) {
	request := applyRequest("ko", "rev-7",
		SetFieldOperation("root", "title", Text("")),
		SetFieldOperation("paragraph-a", "rich", RichText(InlineText("Hello "), Bold(InlineText("world")), Placeholder("member-name"))),
		SetFieldOperation("paragraph-b", "rich", RichText()),
		UnsetFieldOperation("root", "slug"),
		SetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("caption")}, Text("caption")),
		UnsetNestedFieldOperation("root", "settings", []FieldPathSegment{ObjectPath("caption")}),
		InsertBlockOperation("paragraph-new", "paragraph", "root", "paragraph-a"),
		MoveBlockOperation("paragraph-b", "", "root"),
		ReplaceBlockKindOperation("paragraph-a", "heading"),
		InsertRelationItemOperation("root", "credits", "credit-new", "credit", "credit-primary"),
		SetRelationFieldOperation("root", "credits", "credit-new", "bio", Text("")),
		UnsetRelationFieldOperation("root", "credits", "credit-new", "bio"),
		MoveRelationItemOperation("root", "credits", "credit-secondary", "root", "credits", "credit-new"),
		AttachRelationFileOperation("root", "credits", "credit-new", "avatar", "verified-avatar"),
		DetachRelationFileOperation("root", "credits", "credit-new", "avatar"),
		DeleteRelationItemOperation("root", "credits", "credit-new"),
		AttachFileOperation("root", "hero", "verified-file-handle"),
		DetachFileOperation("root", "hero"),
		DeleteBlockOperation("paragraph-new"),
	)
	encoded, err := EncodeApplyRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeApplyRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := EncodeApplyRequest(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("apply encoding is not deterministic:\n%s\n%s", encoded, reencoded)
	}
	if !bytes.Contains(encoded, []byte(`["fs",["root","","","title"],["t",""]]`)) {
		t.Fatalf("apply encoding lost explicit empty: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`["fs",["paragraph-b","","","rich"],["i",[]]]`)) {
		t.Fatalf("apply encoding lost explicit empty rich value: %s", encoded)
	}
	if strings.Contains(string(encoded), "path") || strings.Contains(string(encoded), "html") {
		t.Fatalf("apply wire exposed an untyped path or HTML: %s", encoded)
	}

	for _, localeOperation := range []Operation{CreateTranslationOperation(), DeleteTranslationOperation()} {
		wire, err := EncodeApplyRequest(applyRequest("en", "rev-7", localeOperation))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeApplyRequest(wire); err != nil {
			t.Fatalf("translation locale operation did not round-trip: %v", err)
		}
	}
}

func TestTargetValidationCannotAlterSourceOwnedGraph(t *testing.T) {
	target := testDocument("en", true)
	tests := []struct {
		name string
		op   Operation
		code IssueCode
	}{
		{"insert block", InsertBlockOperation("new-block", "paragraph", "root", ""), IssueSourceAuthorityRequired},
		{"delete block", DeleteBlockOperation("paragraph-a"), IssueSourceAuthorityRequired},
		{"move block", MoveBlockOperation("paragraph-a", "", ""), IssueSourceAuthorityRequired},
		{"replace block kind", ReplaceBlockKindOperation("paragraph-a", "heading"), IssueSourceAuthorityRequired},
		{"insert relation item", InsertRelationItemOperation("root", "credits", "credit-new", "credit", ""), IssueSourceAuthorityRequired},
		{"delete relation item", DeleteRelationItemOperation("root", "credits", "credit-primary"), IssueSourceAuthorityRequired},
		{"move relation item", MoveRelationItemOperation("root", "credits", "credit-primary", "root", "credits", ""), IssueSourceAuthorityRequired},
		{"set shared field", SetFieldOperation("root", "slug", Text("new")), IssueTargetFieldForbidden},
		{"set shared relation field", SetRelationFieldOperation("root", "credits", "credit-primary", "role", Text("owner")), IssueTargetFieldForbidden},
		{"unset field", UnsetFieldOperation("root", "slug"), IssueTargetFieldForbidden},
		{"unset locale field", UnsetFieldOperation("paragraph-a", "content"), IssueTargetFieldForbidden},
		{"unset locale relation field", UnsetRelationFieldOperation("root", "credits", "credit-primary", "bio"), IssueTargetFieldForbidden},
		{"set non-translatable locale field", SetFieldOperation("paragraph-a", "internal_note", Text("secret")), IssueTargetFieldForbidden},
		{"attach file", AttachFileOperation("root", "hero", "file-handle"), IssueSourceAuthorityRequired},
		{"detach file", DetachFileOperation("root", "hero"), IssueSourceAuthorityRequired},
		{"attach relation file", AttachRelationFileOperation("root", "credits", "credit-primary", "avatar", "file-handle"), IssueSourceAuthorityRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ValidateOperations(target, applyRequest("en", "rev-7", test.op))
			if result.Valid() || len(result.Issues) != 1 || result.Issues[0].Code != test.code {
				t.Fatalf("unexpected validation result: %+v", result)
			}
		})
	}

	valid := ValidateOperations(target, applyRequest("en", "rev-7",
		SetFieldOperation("paragraph-a", "content", Text("Translated")),
		SetFieldOperation("paragraph-b", "content", Text("")),
		SetRelationFieldOperation("root", "credits", "credit-primary", "bio", Text("Translated bio")),
	))
	if !valid.Valid() {
		t.Fatalf("target locale-owned values should be valid: %+v", valid)
	}
}

func TestSourceValidationSupportsTypedGraphFieldAndFileOperations(t *testing.T) {
	source := testDocument("ko", true)
	request := applyRequest("ko", "rev-7",
		InsertBlockOperation("paragraph-new", "paragraph", "root", "paragraph-a"),
		SetFieldOperation("paragraph-new", "content", Text("새 문단")),
		MoveBlockOperation("paragraph-new", "root", "paragraph-b"),
		SetFieldOperation("root", "slug", Text("new-slug")),
		SetFieldOperation("root", "source_note", Text("source-only")),
		UnsetFieldOperation("root", "slug"),
		UnsetFieldOperation("root", "source_note"),
		AttachFileOperation("root", "hero", "verified-file"),
		DetachFileOperation("root", "hero"),
		InsertRelationItemOperation("root", "credits", "credit-new", "credit", "credit-primary"),
		SetRelationFieldOperation("root", "credits", "credit-new", "role", Text("photographer")),
		MoveRelationItemOperation("root", "credits", "credit-new", "root", "credits", "credit-secondary"),
		AttachRelationFileOperation("root", "credits", "credit-new", "avatar", "verified-avatar"),
		DetachRelationFileOperation("root", "credits", "credit-new", "avatar"),
		DeleteRelationItemOperation("root", "credits", "credit-new"),
		DeleteBlockOperation("paragraph-new"),
	)
	result := ValidateOperations(source, request)
	if !result.Valid() {
		t.Fatalf("source operations should validate: %+v", result)
	}
}

func TestInlineMarksPreserveTransportShapeWithoutDuplicatingGeneratedColorPolicy(t *testing.T) {
	valid := RichText(
		Underline(InlineText("u")),
		Strike(InlineText("s")),
		InlineCode(InlineText("code")),
		TextColor("#aabbcc", InlineText("foreground")),
		BackgroundColor("yellow", InlineText("background")),
	)
	if err := valid.validate(); err != nil {
		t.Fatalf("generated editor inline marks should validate: %v", err)
	}
	for _, color := range []string{"#ABC", "rgb(0,0,0)", "unknown", "#12", "#ggg"} {
		if err := TextColor(color, InlineText("value")).validate(0); err != nil {
			t.Fatalf("generated color policy must not be duplicated for %q: %v", color, err)
		}
	}
	for _, color := range []string{"", strings.Repeat("x", 65)} {
		if err := TextColor(color, InlineText("value")).validate(0); err == nil {
			t.Fatalf("invalid transport mark parameter %q was accepted", color)
		}
	}
}

func TestInvalidOperationsReportExactOperationAndStableHandle(t *testing.T) {
	document := testDocument("ko", true)
	request := applyRequest("ko", "rev-7",
		SetFieldOperation("missing-block", "content", Text("value")),
		SetFieldOperation("root", "title", Boolean(true)),
		MoveBlockOperation("root", "paragraph-a", ""),
		AttachFileOperation("root", "hero", "data:text/plain;base64,SGVsbG8="),
	)
	result := ValidateOperations(document, request)
	if result.Valid() || len(result.Issues) != 4 {
		t.Fatalf("expected four per-operation issues: %+v", result)
	}
	wantCodes := []IssueCode{IssueUnknownBlock, IssueValueKindMismatch, IssueBlockCycle, IssueInvalidFileReference}
	for index, issue := range result.Issues {
		if issue.Operation != index || issue.Code != wantCodes[index] || issue.Handle == "" {
			t.Fatalf("issue %d is not structured as expected: %+v", index, issue)
		}
	}
}

func TestRevisionCASConflictIncludesCurrentRevisionAndAffectedHandles(t *testing.T) {
	document := testDocument("en", true)
	request := applyRequest("en", "rev-stale",
		SetFieldOperation("paragraph-a", "content", Text("new")),
		SetFieldOperation("paragraph-a", "content", Text("newer")),
	)
	result := ValidateOperations(document, request)
	if result.Conflict == nil || result.Conflict.Code != ConflictDocumentRevision || result.Conflict.CurrentDocumentRevision != "rev-7" ||
		result.Conflict.CurrentTargetRevision == nil || *result.Conflict.CurrentTargetRevision != *document.TargetRevision {
		t.Fatalf("missing structured conflict: %+v", result)
	}
	if len(result.Conflict.AffectedHandles) != 1 || result.Conflict.AffectedHandles[0] != "field:paragraph-a/content" {
		t.Fatalf("affected handles were not stable and deduplicated: %+v", result.Conflict)
	}
	relationConflict := ValidateOperations(testDocument("ko", true), applyRequest("ko", "rev-stale",
		MoveRelationItemOperation("root", "credits", "credit-primary", "root", "credits", "credit-secondary")))
	want := []string{"relation-item:root/credits/credit-primary", "relation:root/credits"}
	if relationConflict.Conflict == nil || !slices.Equal(relationConflict.Conflict.AffectedHandles, want) {
		t.Fatalf("relation conflict lost multiple affected handles: %+v", relationConflict)
	}
}

func TestTargetRevisionCASConflictDoesNotMasqueradeAsDocumentConflict(t *testing.T) {
	document := testDocument("en", true)
	staleTarget := Revision("target-rev-stale")
	request := applyRequest("en", document.DocumentRevision,
		SetFieldOperation("paragraph-a", "content", Text("new")),
	)
	request.ExpectedTargetRevision = &staleTarget

	result := ValidateOperations(document, request)
	if result.Conflict == nil || result.Conflict.Code != ConflictTargetRevision ||
		result.Conflict.CurrentDocumentRevision != document.DocumentRevision ||
		result.Conflict.CurrentTargetRevision == nil ||
		*result.Conflict.CurrentTargetRevision != *document.TargetRevision {
		t.Fatalf("target conflict did not preserve both current tokens: %+v", result)
	}
}

func TestTranslationLocaleCRUDAndImplicitFirstTargetWrite(t *testing.T) {
	missingTarget := testDocument("en", false)
	if result := ValidateOperations(missingTarget, missingTargetRequest("en", "rev-7", CreateTranslationOperation())); !result.Valid() {
		t.Fatalf("explicit create should be valid for an absent target: %+v", result)
	}
	if result := ValidateOperations(missingTarget, missingTargetRequest("en", "rev-7",
		SetFieldOperation("paragraph-a", "content", Text("first value")))); !result.Valid() {
		t.Fatalf("first target value write should implicitly create the locale resource: %+v", result)
	}
	if result := ValidateOperations(missingTarget, missingTargetRequest("en", "rev-7", DeleteTranslationOperation())); result.Valid() || result.Issues[0].Code != IssueTranslationMissing {
		t.Fatalf("deleting an absent translation should fail: %+v", result)
	}
	existingTarget := testDocument("en", true)
	if result := ValidateOperations(existingTarget, applyRequest("en", "rev-7", DeleteTranslationOperation())); !result.Valid() {
		t.Fatalf("delete should be valid for an existing target: %+v", result)
	}
	if result := ValidateOperations(existingTarget, applyRequest("en", "rev-7", CreateTranslationOperation())); result.Valid() || result.Issues[0].Code != IssueTranslationAlreadyExists {
		t.Fatalf("duplicate create should fail: %+v", result)
	}
	source := testDocument("ko", true)
	if result := ValidateOperations(source, applyRequest("ko", "rev-7", DeleteTranslationOperation())); result.Valid() || result.Issues[0].Code != IssueTranslationIsSource {
		t.Fatalf("source locale delete should fail: %+v", result)
	}
	if result := ValidateOperations(existingTarget, applyRequest("en", "rev-7",
		DeleteTranslationOperation(), SetFieldOperation("paragraph-a", "content", Text("x")))); result.Valid() || result.Issues[0].Code != IssueLocaleOperationNotExclusive {
		t.Fatalf("translation delete must not mix with graph/value mutation: %+v", result)
	}
}

func TestStableIDsRejectEmptyAndPositionalPaths(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Document)
		contains string
	}{
		{"empty block", func(document *Document) { document.Nodes[0].ID = "" }, "block ID"},
		{"numeric block index", func(document *Document) { document.Nodes[0].ID = "0" }, "positional index"},
		{"array path block", func(document *Document) { document.Nodes[0].ID = "rows[0]" }, "positional path"},
		{"dotted positional block", func(document *Document) { document.Nodes[0].ID = "table.rows.0.cells.1" }, "positional index"},
		{"numeric field index", func(document *Document) { document.Nodes[0].Localized[0].ID = "1" }, "positional index"},
		{"array path field", func(document *Document) { document.Nodes[0].Localized[0].ID = "cells[2]" }, "positional path"},
		{"numeric relation", func(document *Document) { document.Nodes[1].Relations[0].ID = "0" }, "positional index"},
		{"array path relation item", func(document *Document) { document.Nodes[1].Relations[0].Items[0].ID = "credits[0]" }, "positional path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := testDocument("en", true)
			test.mutate(&document)
			projection := Projection{
				Protocol: ProtocolVersion, Profile: document.Identity.Domain, Catalog: document.Catalog.Fingerprint,
				Document: document.Identity.Reference, DocumentRevision: document.DocumentRevision, TargetRevision: document.TargetRevision, SourceLocale: document.SourceLocale,
				Locale: document.Locale, LocaleRole: document.Role(), LocaleExists: document.LocaleExists,
				Mode: ReadBlocks, Nodes: document.Nodes,
			}
			_, err := EncodeProjection(projection)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("expected stable ID rejection containing %q, got %v", test.contains, err)
			}
		})
	}
}

func TestDecodeRejectsUnknownCanonicalSurface(t *testing.T) {
	document := testDocument("en", true)
	projection := Projection{
		Protocol: ProtocolVersion, Profile: document.Identity.Domain, Catalog: document.Catalog.Fingerprint,
		Document: document.Identity.Reference, DocumentRevision: document.DocumentRevision, TargetRevision: document.TargetRevision, SourceLocale: document.SourceLocale,
		Locale: document.Locale, LocaleRole: document.Role(), LocaleExists: document.LocaleExists,
		Mode: ReadOutline, Nodes: []Node{{ID: "root", Kind: "document"}},
	}
	wire, err := EncodeProjection(projection)
	if err != nil {
		t.Fatal(err)
	}
	wire = bytes.Replace(wire, []byte(`"next":null`), []byte(`"next":null,"tiptap":{}`), 1)
	if _, err := decodeProjectionForTest(wire); err == nil {
		t.Fatal("unknown editor-native surface was accepted")
	}
	if _, err := DecodeApplyRequest([]byte(`{"v":"dcdp/1","p":"post","d":"doc-handle","l":"en","er":"rev-7","o":[["json_patch","/nodes/0",{}]]}`)); err == nil {
		t.Fatal("generic JSON Patch operation was accepted")
	}
	if _, err := EncodeApplyRequest(applyRequest("ko", "rev-7",
		AttachFileOperation("root", "hero", "data:text/plain;base64,SGVsbG8="))); err == nil {
		t.Fatal("inline base64 file payload was accepted by the canonical input codec")
	}
}

func TestCompactDecodersResetReusedReceivers(t *testing.T) {
	var operation Operation
	if err := json.Unmarshal([]byte(`["rm","root","credits","credit-primary","root","credits","credit-secondary"]`), &operation); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`["ld"]`), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Kind != OperationDeleteTranslation || operation.DeleteTranslation == nil ||
		operation.MoveRelationItem != nil || operation.payloadCount() != 1 {
		t.Fatalf("reused operation retained its previous payload: %+v", operation)
	}

	var value Value
	if err := json.Unmarshal([]byte(`["i",[["a","https://example.com",[["t","link"]]]]]`), &value); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`["b",false]`), &value); err != nil {
		t.Fatal(err)
	}
	if value.Kind != ValueKindBoolean || value.Text != "" || value.Inline != nil || value.Boolean {
		t.Fatalf("reused value retained its previous scalar or inline payload: %+v", value)
	}

	var inline InlineItem
	if err := json.Unmarshal([]byte(`["a","https://example.com",[["t","link"]]]`), &inline); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`["br"]`), &inline); err != nil {
		t.Fatal(err)
	}
	if inline.Kind != InlineKindHardBreak || inline.Text != "" || inline.Target != "" || inline.Children != nil {
		t.Fatalf("reused inline item retained its previous payload: %+v", inline)
	}
}

func TestOpenReturnsDeterministicProtocolMetadata(t *testing.T) {
	port := &fakePort{document: testDocument("en", false)}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := service.Open(context.Background(), OpenRequest{Document: port.document.Identity, Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Protocol != ProtocolVersion || metadata.Profile != DomainPost || metadata.DocumentRevision != "rev-7" ||
		metadata.LocaleRole != LocaleRoleNonSource || metadata.LocaleExists {
		t.Fatalf("unexpected open metadata: %+v", metadata)
	}
	wire, err := EncodeOpenMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeOpenMetadataForTest(wire)
	if err != nil || decoded != metadata {
		t.Fatalf("open metadata did not round-trip: %+v %v", decoded, err)
	}
}

func TestRelationProjectionAndFieldReadsPreserveStableTopologyAndMissingState(t *testing.T) {
	document := testDocument("en", true)
	port := &fakePort{document: document}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	outline, err := service.Read(context.Background(), ReadRequest{Document: document.Identity, Locale: "en", Mode: ReadOutline})
	if err != nil {
		t.Fatal(err)
	}
	root := outline.Nodes[0]
	if root.ID != "root" || len(root.Relations) != 1 || len(root.Relations[0].Items) != 2 ||
		root.Relations[0].Items[0].ID != "credit-primary" || len(root.Relations[0].Items[0].Localized) != 0 {
		t.Fatalf("outline did not retain only stable relation topology: %+v", root)
	}

	explicit, err := service.Read(context.Background(), ReadRequest{Document: document.Identity, Locale: "en", Mode: ReadFields,
		Fields: []FieldSelection{{Block: "root", Relation: "credits", Item: "credit-primary", Field: "bio"}}})
	if err != nil {
		t.Fatal(err)
	}
	item := explicit.Nodes[0].Relations[0].Items[0]
	if len(item.Localized) != 1 || item.Localized[0].Value.Text != "" {
		t.Fatalf("relation item explicit empty was not preserved: %+v", item)
	}

	for nodeIndex := range document.Nodes {
		for relationIndex := range document.Nodes[nodeIndex].Relations {
			for itemIndex := range document.Nodes[nodeIndex].Relations[relationIndex].Items {
				document.Nodes[nodeIndex].Relations[relationIndex].Items[itemIndex].Localized = nil
			}
		}
	}
	sourceMissing := testDocument("ko", true)
	for nodeIndex := range sourceMissing.Nodes {
		for relationIndex := range sourceMissing.Nodes[nodeIndex].Relations {
			for itemIndex := range sourceMissing.Nodes[nodeIndex].Relations[relationIndex].Items {
				sourceMissing.Nodes[nodeIndex].Relations[relationIndex].Items[itemIndex].Localized = nil
			}
		}
	}
	for _, state := range []struct {
		name     string
		document Document
		locale   Locale
	}{
		{"source missing", sourceMissing, "ko"},
		{"existing target missing", document, "en"},
		{"absent target missing", testDocument("en", false), "en"},
	} {
		t.Run(state.name, func(t *testing.T) {
			port := &fakePort{document: state.document}
			service, _ := NewService(port)
			projection, err := service.Read(context.Background(), ReadRequest{Document: state.document.Identity, Locale: state.locale, Mode: ReadFields,
				Fields: []FieldSelection{{Block: "root", Relation: "credits", Item: "credit-primary", Field: "bio"}}})
			if err != nil {
				t.Fatal(err)
			}
			item := projection.Nodes[0].Relations[0].Items[0]
			if len(item.Localized) != 0 {
				t.Fatalf("known missing relation field was encoded as present: %+v", item)
			}
		})
	}
	for _, selection := range []FieldSelection{
		{Block: "root", Relation: "missing-relation", Item: "credit-primary", Field: "bio"},
		{Block: "root", Relation: "credits", Item: "missing-item", Field: "bio"},
		{Block: "root", Relation: "credits", Item: "credit-primary", Field: "missing-field"},
	} {
		if _, err := service.Read(context.Background(), ReadRequest{Document: document.Identity, Locale: "en", Mode: ReadFields, Fields: []FieldSelection{selection}}); err == nil {
			t.Fatalf("unknown relation selector was accepted: %+v", selection)
		}
	}
}

func TestRelationOperationValidationUsesStableItemsAndTypedCatalog(t *testing.T) {
	source := testDocument("ko", true)
	tests := []struct {
		name string
		op   Operation
		code IssueCode
	}{
		{"duplicate", InsertRelationItemOperation("root", "credits", "credit-primary", "credit", ""), IssueDuplicateRelationItem},
		{"unknown relation", InsertRelationItemOperation("root", "missing-relation", "credit-new", "credit", ""), IssueUnknownRelation},
		{"unknown delete item", DeleteRelationItemOperation("root", "credits", "credit-missing"), IssueUnknownRelationItem},
		{"move after unknown", MoveRelationItemOperation("root", "credits", "credit-primary", "root", "credits", "credit-missing"), IssueInvalidRelationItemMove},
		{"move to incompatible relation", MoveRelationItemOperation("root", "credits", "credit-primary", "paragraph-a", "links", ""), IssueInvalidRelationItemMove},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ValidateOperations(source, applyRequest("ko", "rev-7", test.op))
			if result.Valid() || len(result.Issues) != 1 || result.Issues[0].Code != test.code {
				t.Fatalf("unexpected relation validation result: %+v", result)
			}
		})
	}
	positional := InsertRelationItemOperation("root", "credits", "0", "credit", "")
	if result := ValidateOperations(source, applyRequest("ko", "rev-7", positional)); result.Valid() || result.Issues[0].Code != IssueInvalidOperation {
		t.Fatalf("positional relation item handle was accepted: %+v", result)
	}
}

type fakePort struct {
	document      Document
	loadCalls     int
	applied       []ValidatedApply
	result        ApplyResult
	applyErr      error
	issues        []OperationIssue
	validateCalls int
	validateErr   error
}

type exactMutationFakePort struct {
	*fakePort
	validateMutationCalls int
	executeMutationCalls  int
	validation            ValidationResult
	execution             ApplyResult
	validateMutationErr   error
	executeMutationErr    error
}

func (p *exactMutationFakePort) ValidateMutation(_ context.Context, _ ApplyRequest) (ValidationResult, error) {
	p.validateMutationCalls++
	return p.validation, p.validateMutationErr
}

func (p *exactMutationFakePort) ExecuteMutation(_ context.Context, request ApplyRequest) (ApplyResult, error) {
	p.executeMutationCalls++
	if p.executeMutationErr != nil {
		return ApplyResult{}, p.executeMutationErr
	}
	result := p.execution
	result.Normalized = append([]Operation(nil), request.Operations...)
	return result, nil
}

func (p *fakePort) Load(_ context.Context, identity DocumentIdentity, locale Locale) (Document, error) {
	p.loadCalls++
	document := p.document
	document.Identity = identity
	document.Locale = locale
	return document, nil
}

func (p *fakePort) ValidateMutation(_ context.Context, request ApplyRequest) (ValidationResult, error) {
	p.validateCalls++
	current := p.document
	current.Identity = request.Identity()
	current.Locale = request.Locale
	_, validation := ValidateLoadedApply(current, request)
	if !validation.Valid() {
		return validation, nil
	}
	if p.validateErr != nil {
		return ValidationResult{}, p.validateErr
	}
	validation.Issues = append(validation.Issues, p.issues...)
	return validation, nil

}

func (p *fakePort) ExecuteMutation(_ context.Context, request ApplyRequest) (ApplyResult, error) {
	current := p.document
	current.Identity = request.Identity()
	current.Locale = request.Locale
	command, validation := ValidateLoadedApply(current, request)
	if !validation.Valid() {
		if validation.Conflict != nil {
			return ApplyResult{}, &ConflictError{Conflict: *validation.Conflict}
		}
		return ApplyResult{}, &ValidationError{Result: validation}
	}
	p.applied = append(p.applied, command)
	if p.applyErr != nil {
		if validationError, ok := p.applyErr.(*ValidationError); ok && validationError.Result.Normalized == nil {
			completed := validationError.Result
			completed.Normalized = append([]Operation(nil), validation.Normalized...)
			return ApplyResult{}, &ValidationError{Result: completed}
		}
		return ApplyResult{}, p.applyErr
	}
	return AcceptValidatedApply(command, p.result)
}

func TestServiceValidateAndApplyInvokeEachExactDomainBoundaryOnce(t *testing.T) {
	request := applyRequest("en", "rev-7", SetFieldOperation("paragraph-a", "content", Text("translated")))

	validatePort := &fakePort{document: testDocument("en", true)}
	validateService, err := NewService(validatePort)
	if err != nil {
		t.Fatal(err)
	}
	validation, err := validateService.Validate(context.Background(), request)
	if err != nil || !validation.Valid() {
		t.Fatalf("validate failed: result=%+v err=%v", validation, err)
	}
	if validatePort.loadCalls != 0 || validatePort.validateCalls != 1 || len(validatePort.applied) != 0 {
		t.Fatalf("validate boundary calls = load:%d validate:%d apply:%d", validatePort.loadCalls, validatePort.validateCalls, len(validatePort.applied))
	}

	nextTargetRevision := Revision("target-rev-8")
	applyPort := &fakePort{document: testDocument("en", true), result: ApplyResult{
		DocumentRevision: "rev-7", TargetRevision: &nextTargetRevision, Changed: true,
		Changes: []Change{{Operation: 0, Kind: OperationSetField, AffectedHandles: []string{"field:paragraph-a/content"}}},
	}}
	applyService, err := NewService(applyPort)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyService.Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if applyPort.loadCalls != 0 || applyPort.validateCalls != 0 || len(applyPort.applied) != 1 {
		t.Fatalf("apply boundary calls = load:%d validate:%d apply:%d", applyPort.loadCalls, applyPort.validateCalls, len(applyPort.applied))
	}
}

func TestServiceMutationDelegatesOnlyToTheSelectedExactBoundary(t *testing.T) {
	request := applyRequest("en", "rev-7", SetFieldOperation("paragraph-a", "content", Text("translated")))
	nextTargetRevision := Revision("target-rev-8")
	port := &exactMutationFakePort{
		fakePort:   &fakePort{document: testDocument("en", true)},
		validation: ValidationResult{Normalized: append([]Operation(nil), request.Operations...)},
		execution: ApplyResult{
			DocumentRevision: "rev-7", TargetRevision: &nextTargetRevision, Changed: true,
			Changes: []Change{{Operation: 0, Kind: OperationSetField, AffectedHandles: []string{"field:paragraph-a/content"}}},
		},
	}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}

	validation, err := service.Validate(context.Background(), request)
	if err != nil || !validation.Valid() {
		t.Fatalf("Validate() = (%+v, %v)", validation, err)
	}
	if _, err := service.Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if port.validateMutationCalls != 1 || port.executeMutationCalls != 1 {
		t.Fatalf("exact mutation calls = validate:%d execute:%d", port.validateMutationCalls, port.executeMutationCalls)
	}
	if port.loadCalls != 0 || port.validateCalls != 0 || len(port.applied) != 0 {
		t.Fatalf(
			"unselected boundaries were entered: load:%d validate:%d apply:%d",
			port.loadCalls,
			port.validateCalls,
			len(port.applied),
		)
	}
}

func TestServiceProducesOutlineAndBoundedPartialReads(t *testing.T) {
	port := &fakePort{document: testDocument("en", true)}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	identity := port.document.Identity

	outline, err := service.Read(ctx, ReadRequest{Document: identity, Locale: "en", Mode: ReadOutline, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(outline.Nodes) != 1 || outline.Next == nil || len(outline.Nodes[0].Shared)+len(outline.Nodes[0].Localized)+len(outline.Nodes[0].Files) != 0 {
		t.Fatalf("outline is not compact and bounded: %+v", outline)
	}
	next, err := service.Read(ctx, ReadRequest{Document: identity, Locale: "en", Mode: ReadOutline, Limit: 1, Cursor: *outline.Next})
	if err != nil || len(next.Nodes) != 1 {
		t.Fatalf("outline cursor did not continue: %+v %v", next, err)
	}

	blocks, err := service.Read(ctx, ReadRequest{Document: identity, Locale: "en", Mode: ReadBlocks, Blocks: []BlockID{"paragraph-a"}})
	if err != nil || len(blocks.Nodes) != 1 || blocks.Nodes[0].ID != "paragraph-a" || len(blocks.Nodes[0].Localized) != 2 {
		t.Fatalf("block partial read failed: %+v %v", blocks, err)
	}
	if _, err := EncodeProjection(blocks); err != nil {
		t.Fatalf("partial projection whose parent is outside the chunk did not encode: %v", err)
	}
	fields, err := service.Read(ctx, ReadRequest{Document: identity, Locale: "en", Mode: ReadFields,
		Fields: []FieldSelection{{Block: "paragraph-b", Field: "content"}}})
	if err != nil || len(fields.Nodes) != 1 || len(fields.Nodes[0].Localized) != 1 || fields.Nodes[0].Localized[0].Value.Text != "" {
		t.Fatalf("field read did not preserve explicit empty: %+v %v", fields, err)
	}

	originalTargetRevision := *port.document.TargetRevision
	nextTargetRevision := Revision("target-rev-8")
	port.document.TargetRevision = &nextTargetRevision
	if _, err := service.Read(ctx, ReadRequest{Document: identity, Locale: "en", Mode: ReadOutline, Limit: 1, Cursor: *outline.Next}); err == nil {
		t.Fatal("cursor bound to stale target revision was accepted")
	} else {
		var cursorErr *CursorError
		if !errors.As(err, &cursorErr) || cursorErr.CurrentTargetRevision == nil ||
			*cursorErr.CurrentTargetRevision != nextTargetRevision {
			t.Fatalf("stale target cursor error is not structured: %v", err)
		}
	}
	port.document.TargetRevision = &originalTargetRevision
	port.document.DocumentRevision = "rev-8"
	if _, err := service.Read(ctx, ReadRequest{Document: identity, Locale: "en", Mode: ReadOutline, Limit: 1, Cursor: *outline.Next}); err == nil {
		t.Fatal("cursor bound to stale revision was accepted")
	} else {
		var cursorErr *CursorError
		if !errors.As(err, &cursorErr) || cursorErr.Code != "stale_cursor" || cursorErr.CurrentDocumentRevision != "rev-8" {
			t.Fatalf("stale cursor error is not structured: %v", err)
		}
	}
}

func TestReadFieldsDistinguishesKnownMissingValuesFromUnknownSelectors(t *testing.T) {
	removeLocalized := func(document Document, block BlockID, field FieldID) Document {
		for nodeIndex := range document.Nodes {
			if document.Nodes[nodeIndex].ID != block {
				continue
			}
			values := document.Nodes[nodeIndex].Localized[:0]
			for _, value := range document.Nodes[nodeIndex].Localized {
				if value.ID != field {
					values = append(values, value)
				}
			}
			document.Nodes[nodeIndex].Localized = values
		}
		return document
	}
	tests := []struct {
		name     string
		document Document
		locale   Locale
	}{
		{
			name:     "source locale missing value",
			document: removeLocalized(testDocument("ko", true), "paragraph-a", "content"),
			locale:   "ko",
		},
		{
			name:     "existing target missing value",
			document: removeLocalized(testDocument("en", true), "paragraph-a", "content"),
			locale:   "en",
		},
		{
			name:     "absent target has no values",
			document: testDocument("en", false),
			locale:   "en",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			port := &fakePort{document: test.document}
			service, err := NewService(port)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := service.Read(context.Background(), ReadRequest{
				Document: test.document.Identity,
				Locale:   test.locale,
				Mode:     ReadFields,
				Fields:   []FieldSelection{{Block: "paragraph-a", Field: "content"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(projection.Nodes) != 1 || projection.Nodes[0].ID != "paragraph-a" || len(projection.Nodes[0].Localized) != 0 {
				t.Fatalf("known missing value must return its node with the field absent: %+v", projection.Nodes)
			}
			wire, err := EncodeProjection(projection)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(wire, []byte(`"content"`)) {
				t.Fatalf("missing field was encoded as present: %s", wire)
			}
		})
	}

	port := &fakePort{document: testDocument("en", true)}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	base := ReadRequest{Document: port.document.Identity, Locale: "en", Mode: ReadFields}
	unknownBlock := base
	unknownBlock.Fields = []FieldSelection{{Block: "missing-block", Field: "content"}}
	if _, err := service.Read(context.Background(), unknownBlock); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("true unknown block did not fail distinctly: %v", err)
	}
	unknownField := base
	unknownField.Fields = []FieldSelection{{Block: "paragraph-a", Field: "missing-field"}}
	if _, err := service.Read(context.Background(), unknownField); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("true unknown field did not fail distinctly: %v", err)
	}
}

func TestAbsentTargetFieldWriteDelegatesAtomicImplicitTranslationCreate(t *testing.T) {
	nextTargetRevision := Revision("target-rev-1")
	port := &fakePort{document: testDocument("en", false), result: ApplyResult{
		DocumentRevision: "rev-7", TargetRevision: &nextTargetRevision, Changed: true,
		Changes: []Change{{Operation: 0, Kind: OperationSetField, AffectedHandles: []string{"field:paragraph-a/content"}}},
	}}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	request := missingTargetRequest("en", "rev-7", SetFieldOperation("paragraph-a", "content", Text("first translation")))
	if _, err := service.Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(port.applied) != 1 {
		t.Fatalf("expected one atomic owning-domain apply, got %d", len(port.applied))
	}
	command := port.applied[0]
	if command.LocaleRole != LocaleRoleNonSource || command.LocaleExists || len(command.Operations) != 1 ||
		command.Operations[0].Kind != OperationSetField {
		t.Fatalf("absent target create contract was not preserved: %+v", command)
	}
	// D-27: no core lifecycle operation or persisted state is synthesized. The
	// owning domain creates the locale resource and writes this value inside the
	// same document and target revision CAS transaction.
	if command.Operations[0].CreateTranslation != nil {
		t.Fatal("implicit first write was expanded into a separate locale lifecycle operation")
	}
}

func TestServiceAppliesOnlyValidatedBatchAndPreservesPortErrors(t *testing.T) {
	accepted := []Change{{Operation: 0, Kind: OperationSetField, AffectedHandles: []string{"field:paragraph-b/content", "translation:en"}}}
	nextTargetRevision := Revision("target-rev-8")
	port := &fakePort{document: testDocument("en", true), result: ApplyResult{DocumentRevision: "rev-7", TargetRevision: &nextTargetRevision, Changed: true, Changes: accepted}}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	request := applyRequest("en", "rev-7", SetFieldOperation("paragraph-b", "content", Text("")))
	result, err := service.Apply(context.Background(), request)
	if err != nil || result.DocumentRevision != "rev-7" || result.TargetRevision == nil || *result.TargetRevision != "target-rev-8" || !slices.Equal(result.Changes[0].AffectedHandles, accepted[0].AffectedHandles) || len(port.applied) != 1 {
		t.Fatalf("valid batch was not applied: %+v %v", result, err)
	}
	command := port.applied[0]
	if command.LocaleRole != LocaleRoleNonSource || command.ExpectedDocumentRevision != "rev-7" ||
		len(command.AffectedHandles) != 1 || command.AffectedHandles[0] != "field:paragraph-b/content" ||
		command.Operations[0].SetField.Value.Text != "" {
		t.Fatalf("validated command lost authority, CAS, handle, or explicit empty: %+v", command)
	}

	port.applied = nil
	if _, err := service.Apply(context.Background(), applyRequest("en", "rev-7", DeleteBlockOperation("paragraph-a"))); err == nil {
		t.Fatal("invalid target graph mutation reached the domain port")
	}
	if len(port.applied) != 0 {
		t.Fatal("invalid target graph mutation was applied")
	}

	port.applyErr = &ConflictError{Conflict: Conflict{Code: ConflictDocumentRevision, CurrentDocumentRevision: "rev-9", AffectedHandles: []string{"field:paragraph-b/content"}}}
	if _, err := service.Apply(context.Background(), request); err == nil {
		t.Fatal("port CAS conflict was swallowed")
	} else {
		var conflict *ConflictError
		if !errors.As(err, &conflict) || conflict.Conflict.CurrentDocumentRevision != "rev-9" {
			t.Fatalf("port CAS conflict was not preserved: %v", err)
		}
	}

	port.applyErr = &ValidationError{Result: ValidationResult{Issues: []OperationIssue{{
		Operation: 0, Code: IssueInvalidOperation, Message: "owning domain rejected operation",
	}}}}
	if _, err := service.Apply(context.Background(), request); err == nil {
		t.Fatal("port validation error was swallowed")
	} else {
		var validation *ValidationError
		if !errors.As(err, &validation) || len(validation.Result.Issues) != 1 || validation.Result.Issues[0].Message != "owning domain rejected operation" {
			t.Fatalf("port validation error was not preserved: %v", err)
		}
	}
}

func TestServiceApplyCompletesDomainValidationErrorWithoutOverwritingOrMutatingIt(t *testing.T) {
	request := applyRequest("en", "rev-7", SetFieldOperation("paragraph-b", "content", Text("translated")))
	domainError := &ValidationError{Result: ValidationResult{Issues: []OperationIssue{{
		Operation: 0, Code: IssueInvalidOperation, Message: "domain constraint",
	}}}}
	port := &fakePort{document: testDocument("en", true), applyErr: domainError}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Apply(context.Background(), request)
	var completed *ValidationError
	if !errors.As(err, &completed) {
		t.Fatalf("domain validation error type was not preserved: %v", err)
	}
	if len(completed.Result.Normalized) != 1 || completed.Result.Normalized[0].Kind != OperationSetField || completed.Result.Normalized[0].SetField.Value.Text != "translated" {
		t.Fatalf("core-normalized operations were not completed: %+v", completed.Result.Normalized)
	}
	if len(completed.Result.Issues) != 1 || completed.Result.Issues[0].Message != "domain constraint" || completed.Result.Conflict != nil {
		t.Fatalf("domain validation fields were not preserved: %+v", completed.Result)
	}
	if domainError.Result.Normalized != nil {
		t.Fatalf("domain-owned validation error was mutated: %+v", domainError.Result.Normalized)
	}

	domainNormalized := []Operation{UnsetFieldOperation("paragraph-b", "content")}
	domainConflict := &Conflict{Code: ConflictDocumentRevision, CurrentDocumentRevision: "rev-domain"}
	port.applyErr = &ValidationError{Result: ValidationResult{
		Normalized: domainNormalized,
		Issues:     []OperationIssue{{Operation: 0, Code: IssueInvalidOperation, Message: "preserve me"}},
		Conflict:   domainConflict,
	}}
	_, err = service.Apply(context.Background(), request)
	completed = nil
	if !errors.As(err, &completed) {
		t.Fatalf("pre-completed domain validation error type was not preserved: %v", err)
	}
	if len(completed.Result.Normalized) != 1 || completed.Result.Normalized[0].Kind != OperationUnsetField || completed.Result.Conflict != domainConflict || completed.Result.Issues[0].Message != "preserve me" {
		t.Fatalf("existing domain validation result was overwritten: %+v", completed.Result)
	}
	if port.validateCalls != 0 || len(port.applied) != 2 {
		t.Fatalf("apply error boundary calls = validate:%d apply:%d", port.validateCalls, len(port.applied))
	}
}

func TestServiceAcceptsSemanticNoopOnlyWithUnchangedRevisionAndNoChanges(t *testing.T) {
	request := applyRequest("en", "rev-7", SetFieldOperation("paragraph-b", "content", Text("")))
	targetRevision := Revision("target-rev-7")
	emptyTargetRevision := Revision("")
	port := &fakePort{document: testDocument("en", true), result: ApplyResult{DocumentRevision: "rev-7", TargetRevision: &targetRevision}}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), request)
	if err != nil || result.Changed || result.DocumentRevision != "rev-7" || len(result.Changes) != 0 {
		t.Fatalf("semantic no-op was not preserved: result=%+v err=%v", result, err)
	}

	tests := []ApplyResult{
		{DocumentRevision: "rev-8", TargetRevision: &targetRevision},
		{DocumentRevision: "rev-7", TargetRevision: &emptyTargetRevision},
		{DocumentRevision: "rev-7", Changes: []Change{{Operation: 0, Kind: OperationSetField}}},
		{DocumentRevision: "rev-7", TargetRevision: &targetRevision, Changed: true, Changes: []Change{{Operation: 0, Kind: OperationSetField}}},
		{DocumentRevision: "rev-8", Changed: true},
	}
	for _, invalid := range tests {
		port.result = invalid
		if _, err := service.Apply(context.Background(), request); err == nil {
			t.Fatalf("invalid domain no-op/change result was accepted: %+v", invalid)
		}
	}
}

func TestAcceptValidatedApplyPreservesAuthoritativeNormalizedBatch(t *testing.T) {
	operation := UnsetFieldOperation("paragraph-b", "content")
	command := ValidatedApply{
		ExpectedDocumentRevision: "rev-7",
		ExpectedTargetRevision:   revisionPointer("target-rev-7"),
		Operations:               []Operation{operation},
	}
	result, err := AcceptValidatedApply(command, ApplyResult{
		DocumentRevision: "rev-7",
		TargetRevision:   revisionPointer("target-rev-8"),
		Changed:          true,
		Changes:          []Change{{Operation: 0, Kind: OperationUnsetField}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Normalized) != 1 || result.Normalized[0].Kind != OperationUnsetField {
		t.Fatalf("accepted normalized batch = %+v", result.Normalized)
	}
	command.Operations[0] = SetFieldOperation("paragraph-b", "content", Text("mutated later"))
	if result.Normalized[0].Kind != OperationUnsetField {
		t.Fatal("accepted result reused the command slice backing array")
	}
}

func TestAcceptValidatedApplyRejectsInconsistentResultShape(t *testing.T) {
	command := ValidatedApply{
		ExpectedDocumentRevision: "rev-7",
		ExpectedTargetRevision:   revisionPointer("target-rev-7"),
		Operations: []Operation{
			SetFieldOperation("paragraph-b", "content", Text("translated")),
		},
	}
	tests := []ApplyResult{
		{
			DocumentRevision: "rev-7", TargetRevision: revisionPointer("target-rev-8"), Changed: true,
			Changes: []Change{{Operation: 1, Kind: OperationSetField}},
		},
		{
			DocumentRevision: "rev-7", TargetRevision: revisionPointer("target-rev-8"), Changed: true,
			Changes: []Change{{Operation: 0, Kind: OperationUnsetField}},
		},
		{
			DocumentRevision: "rev-7", TargetRevision: revisionPointer("target-rev-8"), Changed: true,
			Changes: []Change{{Operation: 0, Kind: OperationSetField}, {Operation: 0, Kind: OperationSetField}},
		},
	}
	for _, result := range tests {
		if _, err := AcceptValidatedApply(command, result); err == nil {
			t.Fatalf("inconsistent accepted result passed: %+v", result)
		}
	}
}

func TestSupportedDomainsReturnsOrderedCopy(t *testing.T) {
	first := SupportedDomains()
	second := SupportedDomains()
	if len(first) != 12 || len(second) != 12 || first[0] != DomainPost || first[len(first)-1] != DomainPostSeries {
		t.Fatalf("supported domains = %+v", first)
	}
	first[0] = Domain("mutated")
	if second[0] != DomainPost || SupportedDomains()[0] != DomainPost {
		t.Fatal("SupportedDomains exposed mutable package state")
	}
}

func revisionPointer(value Revision) *Revision { return &value }
