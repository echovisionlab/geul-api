package aidocumentadapter

import (
	"context"
	"errors"
	"reflect"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

type stubEmailRichTextDomain struct {
	state         emailRichTextState
	validateInput emailRichTextMutation
	applyInput    emailRichTextMutation
	validateErr   error
	applyResult   emailRichTextMutationResult
	applyErr      error
	authorizeErr  error
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (s *stubEmailRichTextDomain) Load(context.Context, string, string) (emailRichTextState, error) {
	s.loadCalls++
	return s.state, nil
}

func (s *stubEmailRichTextDomain) Execute(
	_ context.Context,
	reference string,
	locale string,
	mode emailRichTextExecutionMode,
	compiler emailRichTextMutationCompiler,
) (emailRichTextMutationResult, error) {
	s.executeCalls++
	if s.authorizeErr != nil {
		return emailRichTextMutationResult{}, s.authorizeErr
	}
	if reference != s.state.Reference || locale != s.state.Locale {
		return emailRichTextMutationResult{}, errors.New("unexpected Email profile identity or locale")
	}
	s.compilerCalls++
	mutation, err := compiler(s.state)
	if err != nil {
		return emailRichTextMutationResult{}, err
	}
	if mode == emailRichTextExecutionValidate {
		s.validateInput = mutation
		return s.applyResult, s.validateErr
	}
	s.applyInput = mutation
	return s.applyResult, s.applyErr
}

func TestEmailRichTextProjectionUsesGeneratedCatalogAndFixedMetadataRoot(t *testing.T) {
	t.Parallel()
	port, domain, identity := newEmailRichTextPortForTest(t, core.DomainEmailTemplate, "ko", false)
	document, err := port.Load(context.Background(), identity, "ko")
	if err != nil {
		t.Fatal(err)
	}
	if document.LocaleExists || len(document.Nodes) != 2 || document.Nodes[0].ID != emailMetadataBlockID {
		t.Fatalf("unexpected absent target projection: %+v", document)
	}
	if len(document.Nodes[0].Localized) != 0 || document.Nodes[1].Parent != emailMetadataBlockID {
		t.Fatalf("metadata/body topology = %+v", document.Nodes)
	}
	if document.Catalog.Fingerprint == port.codec.Catalog().Fingerprint {
		t.Fatal("Email subject catalog extension did not change the generated catalog fingerprint")
	}

	empty := ""
	domain.state.LocaleExists = true
	domain.state.Subject = &empty
	present, err := port.Load(context.Background(), identity, "ko")
	if err != nil {
		t.Fatal(err)
	}
	if !present.LocaleExists || len(present.Nodes[0].Localized) != 1 ||
		!reflect.DeepEqual(present.Nodes[0].Localized[0].Value, core.Text("")) {
		t.Fatalf("explicit empty subject was not preserved: %+v", present.Nodes[0])
	}
}

func TestEmailRichTextCompilePreservesExplicitEmptyAndTranslationLifecycle(t *testing.T) {
	t.Parallel()
	port, domain, identity := newEmailRichTextPortForTest(t, core.DomainCampaign, "ko", false)
	document, err := port.Load(context.Background(), identity, "ko")
	if err != nil {
		t.Fatal(err)
	}
	contributor := uuid.New()

	mutation, issues, err := port.compile(domain.state, document, contributor, []core.Operation{
		core.SetFieldOperation(emailMetadataBlockID, emailSubjectField, core.Text("")),
	})
	if err != nil || len(issues) != 0 {
		t.Fatalf("compile explicit empty = issues=%+v err=%v", issues, err)
	}
	if !mutation.SetSubject || mutation.Subject != "" || mutation.Batch == nil {
		t.Fatalf("explicit empty mutation = %+v", mutation)
	}

	_, issues, err = port.compile(domain.state, document, contributor, []core.Operation{
		core.UnsetFieldOperation(emailMetadataBlockID, emailSubjectField),
	})
	if err != nil || len(issues) != 1 || issues[0].Code != core.IssueInvalidOperation {
		t.Fatalf("unset subject = issues=%+v err=%v", issues, err)
	}

	created, issues, err := port.compile(domain.state, document, contributor, []core.Operation{
		core.CreateTranslationOperation(),
	})
	if err != nil || len(issues) != 0 || !created.CreateTranslation || created.Batch != nil {
		t.Fatalf("create translation = mutation=%+v issues=%+v err=%v", created, issues, err)
	}
	domain.state.LocaleExists = true
	document.LocaleExists = true
	deleted, issues, err := port.compile(domain.state, document, contributor, []core.Operation{
		core.DeleteTranslationOperation(),
	})
	if err != nil || len(issues) != 0 || !deleted.DeleteTranslation || deleted.Batch != nil {
		t.Fatalf("delete translation = mutation=%+v issues=%+v err=%v", deleted, issues, err)
	}
}

func TestEmailRichTextCompileUnwrapsOnlyTheFixedDocumentRoot(t *testing.T) {
	t.Parallel()
	port, domain, identity := newEmailRichTextPortForTest(t, core.DomainEmailTemplate, "en", true)
	document, err := port.Load(context.Background(), identity, "en")
	if err != nil {
		t.Fatal(err)
	}
	inserted := uuid.New()
	mutation, issues, err := port.compile(domain.state, document, uuid.New(), []core.Operation{
		core.InsertBlockOperation(core.BlockID(inserted.String()), "paragraph", emailMetadataBlockID, ""),
	})
	if err != nil || len(issues) != 0 || mutation.Batch == nil {
		t.Fatalf("compile root child = issues=%+v err=%v", issues, err)
	}
	found := false
	for _, upsert := range mutation.Batch.Upserts {
		if upsert.ID == inserted {
			found = true
			if upsert.ParentID != nil {
				t.Fatalf("adapter-only metadata parent leaked into storage: %+v", upsert)
			}
		}
	}
	if !found {
		t.Fatalf("inserted Block missing from generated batch: %+v", mutation.Batch.Upserts)
	}

	_, issues, err = port.compile(domain.state, document, uuid.New(), []core.Operation{
		core.InsertBlockOperation(core.BlockID(uuid.NewString()), "paragraph", "", ""),
	})
	if err != nil || len(issues) != 1 {
		t.Fatalf("root escape = issues=%+v err=%v", issues, err)
	}
}

func TestEmailRichTextValidateAndApplyCompileTheSameMutation(t *testing.T) {
	t.Parallel()
	port, domain, identity := newEmailRichTextPortForTest(t, core.DomainCampaign, "ko", true)
	loaded, err := port.Load(context.Background(), identity, "ko")
	if err != nil {
		t.Fatal(err)
	}
	operations := []core.Operation{
		core.SetFieldOperation(emailMetadataBlockID, emailSubjectField, core.Text("translated")),
	}
	service, err := core.NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainCampaign,
		Document: identity.Reference, Locale: "ko",
		ExpectedDocumentRevision: loaded.DocumentRevision,
		ExpectedTargetRevision:   loaded.TargetRevision,
		Operations:               operations,
	}
	validation, err := service.Validate(t.Context(), request)
	if err != nil || !validation.Valid() {
		t.Fatalf("Validate = result=%+v err=%v", validation, err)
	}
	domain.applyResult = emailRichTextMutationResult{DocumentRevision: uuid.NewString(), Changed: true}
	result, err := service.Apply(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(domain.validateInput, domain.applyInput) {
		t.Fatalf("Validate and Apply compiled different mutations:\nvalidate=%+v\napply=%+v", domain.validateInput, domain.applyInput)
	}
	if !result.Changed || len(result.Changes) != 1 || result.Changes[0].Operation != 0 {
		t.Fatalf("Apply result = %+v", result)
	}
	if domain.executeCalls != 2 || domain.compilerCalls != 2 || domain.loadCalls != 1 {
		t.Fatalf("exact boundary calls = execute:%d compiler:%d load:%d", domain.executeCalls, domain.compilerCalls, domain.loadCalls)
	}
}

func TestEmailRichTextValidationMapsDomainCASConflict(t *testing.T) {
	t.Parallel()
	port, domain, identity := newEmailRichTextPortForTest(t, core.DomainEmailTemplate, "en", true)
	loaded, err := port.Load(context.Background(), identity, "en")
	if err != nil {
		t.Fatal(err)
	}
	domain.validateErr = &emailRichTextRevisionConflict{
		kind: core.ConflictDocumentRevision, currentDocumentRevision: uuid.NewString(),
	}
	service, err := core.NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Validate(t.Context(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainEmailTemplate,
		Document: identity.Reference, Locale: "en", ExpectedDocumentRevision: loaded.DocumentRevision,
		Operations: []core.Operation{
			core.SetFieldOperation(emailMetadataBlockID, emailSubjectField, core.Text("source")),
		},
	})
	var conflict *core.ConflictError
	if !errors.As(err, &conflict) || conflict.Conflict.CurrentDocumentRevision == "" {
		t.Fatalf("domain conflict was not mapped: %v", err)
	}
}

func TestEmailRichTextExactMutationDeniesBeforeAdapterCompiler(t *testing.T) {
	port, domain, identity := newEmailRichTextPortForTest(t, core.DomainCampaign, "en", true)
	denied := errors.New("campaign not found")
	domain.authorizeErr = denied
	service, err := core.NewService(port)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Validate(t.Context(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainCampaign,
		Document: identity.Reference, Locale: "en", ExpectedDocumentRevision: core.Revision(domain.state.DocumentRevision),
		Operations: []core.Operation{core.DeleteBlockOperation("unknown-block")},
	})
	if !errors.Is(err, denied) {
		t.Fatalf("Validate error = %v, want denial", err)
	}
	if domain.executeCalls != 1 || domain.compilerCalls != 0 || domain.loadCalls != 0 {
		t.Fatalf(
			"unauthorized boundary calls = execute:%d compiler:%d load:%d",
			domain.executeCalls,
			domain.compilerCalls,
			domain.loadCalls,
		)
	}
}

func newEmailRichTextPortForTest(
	t *testing.T,
	domainName core.Domain,
	locale string,
	exists bool,
) (*emailRichTextPort, *stubEmailRichTextDomain, core.DocumentIdentity) {
	t.Helper()
	reference, documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	document := localizedParagraphDocument(blockID, "source")
	document.Profile = contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL
	document.Locale = locale
	document.LocaleOverlay.Locale = locale
	if !exists {
		document.LocaleOverlay.Blocks = nil
	}
	subject := "subject"
	state := emailRichTextState{
		Reference: reference.String(), DocumentID: documentID, DocumentRevision: revision.String(),
		SourceLocale: "en", Locale: locale, LocaleExists: exists, ViewerMemberID: uuid.NewString(),
		Subject: &subject, Document: document,
	}
	if !exists {
		state.Subject = nil
	} else if locale != state.SourceLocale {
		targetRevision := uuid.NewString()
		state.TargetRevision = &targetRevision
	}
	service := &stubEmailRichTextDomain{state: state}
	port, err := newEmailRichTextPort(domainName, string(domainName), service)
	if err != nil {
		t.Fatal(err)
	}
	identity := core.DocumentIdentity{Domain: domainName, Reference: core.DocumentReference(reference.String())}
	return port, service, identity
}
