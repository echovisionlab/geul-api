package aidocumentadapter

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"connectrpc.com/connect"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type fakeApplication struct {
	openRequest  core.OpenRequest
	applyRequest core.ApplyRequest
	openResult   core.OpenMetadata
	applyResult  core.ApplyResult
	openErr      error
	applyErr     error
}

func (a *fakeApplication) Open(_ context.Context, request core.OpenRequest) (core.OpenMetadata, error) {
	a.openRequest = request
	return a.openResult, a.openErr
}

func (a *fakeApplication) Apply(_ context.Context, request core.ApplyRequest) (core.ApplyResult, error) {
	a.applyRequest = request
	return a.applyResult, a.applyErr
}

func protoDocument() *managev1.AIDocumentReference {
	return &managev1.AIDocumentReference{Domain: managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST, Reference: "post-stable"}
}

func protoLocale(code string) *managev1.AIDocumentLocale {
	return &managev1.AIDocumentLocale{Code: code}
}

func TestOpenMapsMetadata(t *testing.T) {
	targetRevision := core.Revision("target-revision-7")
	application := &fakeApplication{
		openResult: core.OpenMetadata{
			Protocol: core.ProtocolVersion, Profile: core.DomainPost, Catalog: "catalog-1", Document: "post-stable",
			DocumentRevision: "revision-7", TargetRevision: &targetRevision,
			SourceLocale: "ko", Locale: "en", LocaleRole: core.LocaleRoleNonSource, LocaleExists: true,
		},
	}
	service, err := NewService(application)
	if err != nil {
		t.Fatal(err)
	}
	open, err := service.OpenAIDocument(context.Background(), connect.NewRequest(&managev1.OpenAIDocumentRequest{Document: protoDocument(), Locale: protoLocale("en")}))
	if err != nil {
		t.Fatal(err)
	}
	if application.openRequest.Document.Reference != "post-stable" ||
		open.Msg.Metadata.GetLocaleRole() != managev1.AIDocumentLocaleRole_AI_DOCUMENT_LOCALE_ROLE_NON_SOURCE ||
		!open.Msg.Metadata.LocaleExists || open.Msg.Metadata.GetTargetRevision() != string(targetRevision) {
		t.Fatalf("open conversion lost metadata: request=%+v response=%+v", application.openRequest, open.Msg)
	}
}

func TestEveryTypedOperationAndValueRoundTrips(t *testing.T) {
	relationTarget := core.FieldTarget{Block: "root", Relation: "credits", Item: "credit-primary", Field: "bio"}
	operations := []core.Operation{
		core.SetFieldOperation("root", "title", core.Text("")),
		{Kind: core.OperationSetField, SetField: &core.SetField{Target: relationTarget, Value: core.RichText(
			core.InlineText("text"), core.Bold(core.InlineText("bold")), core.Italic(core.InlineText("italic")),
			core.Underline(core.InlineText("underline")), core.Strike(core.InlineText("strike")),
			core.InlineCode(core.InlineText("code")), core.TextColor("#aabbcc", core.InlineText("foreground")),
			core.BackgroundColor("yellow", core.InlineText("background")),
			core.Link("https://example.com", core.InlineText("link")), core.HardBreak(), core.InlineMath("x^2"), core.Placeholder("member-name"),
		)}},
		core.UnsetFieldOperation("root", "slug"),
		core.InsertBlockOperation("block-new", "paragraph", "root", "block-before"),
		core.DeleteBlockOperation("block-new"),
		core.MoveBlockOperation("block-new", "", "root"),
		core.ReplaceBlockKindOperation("block-new", "heading"),
		{Kind: core.OperationAttachFile, AttachFile: &core.AttachFile{Target: relationTarget, File: "file-stable"}},
		{Kind: core.OperationDetachFile, DetachFile: &core.DetachFile{Target: relationTarget}},
		core.CreateTranslationOperation(),
		core.DeleteTranslationOperation(),
		core.InsertRelationItemOperation("root", "credits", "credit-new", "credit", "credit-primary"),
		core.DeleteRelationItemOperation("root", "credits", "credit-new"),
		core.MoveRelationItemOperation("root", "credits", "credit-new", "other-root", "credits", "credit-other"),
	}
	for _, operation := range operations {
		if operationKindToProto(operation.Kind) == managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_UNSPECIFIED {
			t.Fatalf("operation kind %q has no public enum", operation.Kind)
		}
		converted, err := operationFromProto(operationToProto(operation))
		if err != nil {
			t.Fatalf("%s did not decode: %v", operation.Kind, err)
		}
		if !reflect.DeepEqual(converted, operation) {
			t.Fatalf("%s was not lossless:\nwant=%#v\ngot=%#v", operation.Kind, operation, converted)
		}
	}

	for _, protoDomain := range []managev1.AIDocumentDomain{
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PAGE,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_WORK,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PROGRAM_EVENT,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_MENU,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_TEMPLATE,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_EMAIL_LAYOUT,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_CAMPAIGN,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_FORM,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_PRIVACY,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_TERMS,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST_SERIES,
	} {
		domain, err := domainFromProto(protoDomain)
		if err != nil || domainToProto(domain) != protoDomain {
			t.Fatalf("domain %d did not round-trip: %q %v", protoDomain, domain, err)
		}
	}
	for _, protoDomain := range []managev1.AIDocumentDomain{
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_RELEASE,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_ARTIST,
		managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_LABEL,
	} {
		if _, err := domainFromProto(protoDomain); err == nil {
			t.Fatalf("removed domain %d was accepted", protoDomain)
		}
	}
	if _, err := domainFromProto(managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_UNSPECIFIED); err == nil {
		t.Fatal("unspecified domain was accepted")
	}
	issueCodes := []core.IssueCode{
		core.IssueInvalidOperation, core.IssueUnknownBlock, core.IssueDuplicateBlock, core.IssueUnknownBlockKind,
		core.IssueUnknownField, core.IssueValueKindMismatch, core.IssueSourceAuthorityRequired, core.IssueTargetFieldForbidden,
		core.IssueInvalidBlockRelation, core.IssueBlockCycle, core.IssueInvalidFileReference, core.IssueTranslationIsSource,
		core.IssueTranslationAlreadyExists, core.IssueTranslationMissing, core.IssueLocaleOperationNotExclusive,
		core.IssueUnknownRelation, core.IssueUnknownRelationItem, core.IssueDuplicateRelationItem, core.IssueInvalidRelationItemMove,
	}
	for _, code := range issueCodes {
		if issueCodeToProto(code) == managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNSPECIFIED {
			t.Fatalf("issue code %q has no public enum", code)
		}
	}
}

func TestInlineMarkConversionRejectsUnknownAndMalformedColorMarks(t *testing.T) {
	for _, mark := range []string{"unknown", "text-color:", "background-color:RGB", "other:#aabbcc"} {
		_, err := inlineItemsFromProto([]*managev1.AIDocumentInlineItem{{
			Item: &managev1.AIDocumentInlineItem_Mark{Mark: &managev1.AIDocumentInlineMark{
				Mark: mark,
				Children: []*managev1.AIDocumentInlineItem{{
					Item: &managev1.AIDocumentInlineItem_Text{Text: "value"},
				}},
			}},
		}})
		if err == nil {
			t.Fatalf("inline mark %q was accepted", mark)
		}
	}
}

func TestApplyPreservesTypedIssuesConflictsAndAcceptedHandles(t *testing.T) {
	normalized := core.SetRelationFieldOperation("root", "credits", "credit-primary", "bio", core.Text(""))
	application := &fakeApplication{}
	service, _ := NewService(application)
	mutation := mutationWithOperations(operationToProto(normalized))

	nextTargetRevision := core.Revision("target-revision-10")
	application.applyResult = core.ApplyResult{DocumentRevision: "revision-7", TargetRevision: &nextTargetRevision, Changed: true, Changes: []core.Change{{
		Operation: 0, Kind: core.OperationSetField,
		AffectedHandles: []string{"field:root/credits/credit-primary/bio", "translation:en"},
	}}}
	applied, err := service.ApplyAIDocumentOperations(context.Background(), connect.NewRequest(&managev1.ApplyAIDocumentOperationsRequest{Mutation: mutation}))
	if err != nil {
		t.Fatal(err)
	}
	accepted := applied.Msg.GetAccepted()
	if accepted.DocumentRevision != "revision-7" || accepted.GetTargetRevision() != "target-revision-10" || accepted.Changes[0].Kind != managev1.AIDocumentOperationKind_AI_DOCUMENT_OPERATION_KIND_SET_FIELD || len(accepted.Changes[0].AffectedHandles) != 2 {
		t.Fatalf("accepted mutation was not lossless: %+v", accepted)
	}

	application.applyErr = &core.ConflictError{Conflict: core.Conflict{Code: core.ConflictDocumentRevision, CurrentDocumentRevision: "revision-11", AffectedHandles: []string{"relation:root/credits", "relation-item:root/credits/credit-primary"}}}
	rejected, err := service.ApplyAIDocumentOperations(context.Background(), connect.NewRequest(&managev1.ApplyAIDocumentOperationsRequest{Mutation: mutation}))
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Msg.GetRejected().Conflict.CurrentDocumentRevision != "revision-11" || len(rejected.Msg.GetRejected().Conflict.AffectedHandles) != 2 {
		t.Fatalf("apply conflict did not use typed rejection: %+v", rejected.Msg)
	}

	application.applyErr = &core.ValidationError{Result: core.ValidationResult{Issues: []core.OperationIssue{{
		Operation: 0, Code: core.IssueTargetFieldForbidden, Handle: "field:root/slug",
	}}}}
	rejected, err = service.ApplyAIDocumentOperations(context.Background(), connect.NewRequest(&managev1.ApplyAIDocumentOperationsRequest{Mutation: mutation}))
	if err != nil {
		t.Fatal(err)
	}
	issues := rejected.Msg.GetRejected().Issues
	if len(issues) != 1 || issues[0].Code != managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_TARGET_FIELD_FORBIDDEN || issues[0].GetHandle() != "field:root/slug" {
		t.Fatalf("apply validation issue did not use typed rejection: %+v", rejected.Msg)
	}
}

func TestInvalidTransportAndApplicationAuthorizationErrorBoundaries(t *testing.T) {
	application := &fakeApplication{}
	service, _ := NewService(application)
	_, err := service.OpenAIDocument(context.Background(), connect.NewRequest(&managev1.OpenAIDocumentRequest{}))
	var connectError *connect.Error
	if !errors.As(err, &connectError) || connectError.Code() != connect.CodeInvalidArgument {
		t.Fatalf("malformed transport request did not become invalid_argument: %v", err)
	}

	authorizationError := connect.NewError(connect.CodePermissionDenied, errors.New("domain denied"))
	application.openErr = authorizationError
	_, err = service.OpenAIDocument(context.Background(), connect.NewRequest(&managev1.OpenAIDocumentRequest{Document: protoDocument(), Locale: protoLocale("en")}))
	if err != authorizationError {
		t.Fatalf("injected application authorization error was altered: %v", err)
	}
}

func mutationWithOperations(operations ...*managev1.AIDocumentOperation) *managev1.AIDocumentMutation {
	return &managev1.AIDocumentMutation{
		ProtocolVersion: core.ProtocolVersion, Document: protoDocument(), Locale: protoLocale("en"),
		ExpectedDocumentRevision: "revision-7", ExpectedTargetRevision: stringPointer("target-revision-7"), Operations: operations,
	}
}

func stringPointer(value string) *string { return &value }
