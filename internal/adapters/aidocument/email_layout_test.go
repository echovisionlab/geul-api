package aidocumentadapter

import (
	"context"
	"errors"
	"reflect"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/google/uuid"
)

type stubEmailLayoutDomain struct {
	state         emailauthoring.EmailLayoutAIDocumentState
	validateInput emailauthoring.EmailLayoutAIDocumentMutation
	applyInput    emailauthoring.EmailLayoutAIDocumentMutation
	validateErr   error
	applyResult   emailauthoring.EmailLayoutAIDocumentMutationResult
	applyErr      error
	authorizeErr  error
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (s *stubEmailLayoutDomain) Load(context.Context, string, string) (emailauthoring.EmailLayoutAIDocumentState, error) {
	s.loadCalls++
	return s.state, nil
}

func (s *stubEmailLayoutDomain) ExecuteAIDocumentMutation(
	_ context.Context,
	layoutID string,
	locale string,
	mode emailauthoring.EmailLayoutAIDocumentExecutionMode,
	compiler emailauthoring.EmailLayoutAIDocumentMutationCompiler,
) (emailauthoring.EmailLayoutAIDocumentMutationResult, error) {
	s.executeCalls++
	if s.authorizeErr != nil {
		return emailauthoring.EmailLayoutAIDocumentMutationResult{}, s.authorizeErr
	}
	if layoutID != s.state.LayoutID || locale != s.state.Locale {
		return emailauthoring.EmailLayoutAIDocumentMutationResult{}, errors.New(
			"unexpected Email Layout identity or locale",
		)
	}
	s.compilerCalls++
	mutation, err := compiler(s.state)
	if err != nil {
		return emailauthoring.EmailLayoutAIDocumentMutationResult{}, err
	}
	if mode == emailauthoring.EmailLayoutAIDocumentExecutionValidate {
		s.validateInput = mutation
		return s.applyResult, s.validateErr
	}
	s.applyInput = mutation
	return s.applyResult, s.applyErr
}

func TestEmailLayoutProjectionPreservesStableUnitsAbsentAndExplicitEmpty(t *testing.T) {
	t.Parallel()

	port, domain, identity := newEmailLayoutPortForTest("ko", false)
	document, err := port.Load(context.Background(), identity, "ko")
	if err != nil {
		t.Fatal(err)
	}
	if document.LocaleExists || len(document.Nodes) != 3 || len(document.Nodes[1].Localized) != 0 {
		t.Fatalf("absent target projection = %+v", document)
	}
	if document.Nodes[1].ID != core.BlockID(domain.state.Units[0].Handle) || document.Nodes[2].Kind != emailLayoutAttributeKind {
		t.Fatalf("stable unit topology = %+v", document.Nodes)
	}

	empty := ""
	domain.state.LocaleExists = true
	targetRevision := "tr1_target"
	domain.state.TargetRevision = &targetRevision
	domain.state.Units[0].LocaleValue = &empty
	present, err := port.Load(context.Background(), identity, "ko")
	if err != nil {
		t.Fatal(err)
	}
	if !present.LocaleExists || len(present.Nodes[1].Localized) != 1 || present.Nodes[1].Localized[0].Value.Text != "" {
		t.Fatalf("explicit empty target projection = %+v", present.Nodes[1])
	}
}

func TestEmailLayoutCompileAllowsOnlyLocaleValueSetAndLifecycle(t *testing.T) {
	t.Parallel()

	port, domain, identity := newEmailLayoutPortForTest("ko", false)
	document, err := port.Load(context.Background(), identity, "ko")
	if err != nil {
		t.Fatal(err)
	}
	member := uuid.New()
	unit := core.BlockID(domain.state.Units[0].Handle)
	mutation, issues, err := compileEmailLayoutMutation(domain.state, document, member, []core.Operation{
		core.SetFieldOperation(unit, emailLayoutContentField, core.Text("")),
	})
	if err != nil || len(issues) != 0 || !mutation.ReplaceValues || mutation.Values[string(unit)] != "" {
		t.Fatalf("explicit empty compile = mutation=%+v issues=%+v err=%v", mutation, issues, err)
	}

	for _, operation := range []core.Operation{
		core.UnsetFieldOperation(unit, emailLayoutContentField),
		core.DeleteBlockOperation(unit),
		core.SetFieldOperation(unit, emailLayoutElementField, core.Text("section")),
	} {
		_, issues, err := compileEmailLayoutMutation(domain.state, document, member, []core.Operation{operation})
		if err != nil || len(issues) != 1 || issues[0].Code != core.IssueInvalidOperation {
			t.Fatalf("forbidden operation %+v = issues=%+v err=%v", operation, issues, err)
		}
	}

	created, issues, err := compileEmailLayoutMutation(domain.state, document, member, []core.Operation{core.CreateTranslationOperation()})
	if err != nil || len(issues) != 0 || !created.CreateTranslation {
		t.Fatalf("create translation = %+v issues=%+v err=%v", created, issues, err)
	}
	domain.state.LocaleExists = true
	targetRevision := core.Revision("tr1_target")
	domain.state.TargetRevision = emailLayoutStringRevision(&targetRevision)
	document.LocaleExists = true
	document.TargetRevision = &targetRevision
	deleted, issues, err := compileEmailLayoutMutation(domain.state, document, member, []core.Operation{core.DeleteTranslationOperation()})
	if err != nil || len(issues) != 0 || !deleted.DeleteTranslation ||
		deleted.ExpectedTargetRevision == nil || *deleted.ExpectedTargetRevision != string(targetRevision) {
		t.Fatalf("delete translation = %+v issues=%+v err=%v", deleted, issues, err)
	}
}

func TestEmailLayoutValidateAndApplyCompileIdenticalMutation(t *testing.T) {
	t.Parallel()

	port, domain, identity := newEmailLayoutPortForTest("ko", true)
	loaded, err := port.Load(context.Background(), identity, "ko")
	if err != nil {
		t.Fatal(err)
	}
	operation := core.SetFieldOperation(core.BlockID(domain.state.Units[0].Handle), emailLayoutContentField, core.Text("번역"))
	service, err := core.NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainEmailLayout,
		Document: identity.Reference, Locale: "ko",
		ExpectedDocumentRevision: loaded.DocumentRevision,
		ExpectedTargetRevision:   loaded.TargetRevision,
		Operations:               []core.Operation{operation},
	}
	validation, err := service.Validate(t.Context(), request)
	if err != nil || !validation.Valid() {
		t.Fatalf("Validate = result=%+v err=%v", validation, err)
	}
	nextTargetRevision := "tr1_next"
	domain.applyResult = emailauthoring.EmailLayoutAIDocumentMutationResult{
		DocumentRevision: string(loaded.DocumentRevision),
		TargetRevision:   &nextTargetRevision,
		Changed:          true,
	}
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
	if result.DocumentRevision != loaded.DocumentRevision || result.TargetRevision == nil ||
		*result.TargetRevision != core.Revision(nextTargetRevision) {
		t.Fatalf("Apply revisions = %+v", result)
	}
	if domain.executeCalls != 2 || domain.compilerCalls != 2 || domain.loadCalls != 1 {
		t.Fatalf("exact boundary calls = execute:%d compiler:%d load:%d", domain.executeCalls, domain.compilerCalls, domain.loadCalls)
	}
}

func TestEmailLayoutMapsDomainRevisionConflict(t *testing.T) {
	t.Parallel()

	port, domain, identity := newEmailLayoutPortForTest("en", true)
	loaded, err := port.Load(context.Background(), identity, "en")
	if err != nil {
		t.Fatal(err)
	}
	domain.validateErr = &emailauthoring.EmailLayoutAIDocumentRevisionConflictError{
		Kind:                    emailauthoring.EmailLayoutAIDocumentDocumentRevisionConflict,
		CurrentDocumentRevision: "eld1_current",
	}
	service, err := core.NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Validate(t.Context(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainEmailLayout,
		Document: identity.Reference, Locale: "en",
		ExpectedDocumentRevision: loaded.DocumentRevision,
		Operations: []core.Operation{
			core.SetFieldOperation(core.BlockID(domain.state.Units[0].Handle), emailLayoutContentField, core.Text("changed")),
		},
	})
	var conflict *core.ConflictError
	if !errors.As(err, &conflict) || conflict.Conflict.Code != core.ConflictDocumentRevision ||
		conflict.Conflict.CurrentDocumentRevision != "eld1_current" ||
		conflict.Conflict.CurrentTargetRevision != nil {
		t.Fatalf("domain conflict was not mapped: %v", err)
	}
}

func TestEmailLayoutMapsDomainTargetRevisionConflict(t *testing.T) {
	t.Parallel()

	port, domain, identity := newEmailLayoutPortForTest("ko", true)
	loaded, err := port.Load(context.Background(), identity, "ko")
	if err != nil {
		t.Fatal(err)
	}
	currentTarget := "tr1_current"
	domain.validateErr = &emailauthoring.EmailLayoutAIDocumentRevisionConflictError{
		Kind:                    emailauthoring.EmailLayoutAIDocumentTargetRevisionConflict,
		CurrentDocumentRevision: string(loaded.DocumentRevision),
		CurrentTargetRevision:   &currentTarget,
	}
	service, err := core.NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Validate(t.Context(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainEmailLayout,
		Document: identity.Reference, Locale: "ko",
		ExpectedDocumentRevision: loaded.DocumentRevision,
		ExpectedTargetRevision:   loaded.TargetRevision,
		Operations: []core.Operation{
			core.SetFieldOperation(core.BlockID(domain.state.Units[0].Handle), emailLayoutContentField, core.Text("changed")),
		},
	})
	var conflict *core.ConflictError
	if !errors.As(err, &conflict) || conflict.Conflict.Code != core.ConflictTargetRevision ||
		conflict.Conflict.CurrentDocumentRevision != loaded.DocumentRevision ||
		conflict.Conflict.CurrentTargetRevision == nil ||
		*conflict.Conflict.CurrentTargetRevision != core.Revision(currentTarget) {
		t.Fatalf("domain target conflict was not mapped: %v", err)
	}
}

func TestEmailLayoutExactMutationDeniesBeforeAdapterCompiler(t *testing.T) {
	port, domain, identity := newEmailLayoutPortForTest("en", true)
	denied := errors.New("email layout not found")
	domain.authorizeErr = denied
	service, err := core.NewService(port)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Validate(t.Context(), core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainEmailLayout,
		Document: identity.Reference, Locale: "en",
		ExpectedDocumentRevision: core.Revision(domain.state.DocumentRevision),
		Operations:               []core.Operation{core.DeleteBlockOperation("unknown-block")},
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

func newEmailLayoutPortForTest(locale string, exists bool) (*emailLayoutPort, *stubEmailLayoutDomain, core.DocumentIdentity) {
	reference := uuid.NewString()
	textID := "unit:" + uuid.NewString() + ":text"
	attributeID := "unit:" + uuid.NewString() + ":attr:alt"
	text := "Source"
	attribute := "Source alt"
	state := emailauthoring.EmailLayoutAIDocumentState{
		LayoutID: reference, DocumentRevision: "eld1_revision", SourceLocale: "en", Locale: locale,
		LocaleExists: exists, ViewerMemberID: uuid.NewString(),
		Units: []emailauthoring.EmailLayoutAIDocumentUnit{
			{Handle: textID, Kind: "text", Order: 0, SourceValue: text},
			{Handle: attributeID, Kind: "attribute", Element: "img", Attribute: "alt", Order: 1, SourceValue: attribute},
		},
	}
	if locale == "en" {
		state.LocaleExists = true
		state.Units[0].LocaleValue = &text
		state.Units[1].LocaleValue = &attribute
	} else if exists {
		targetRevision := "tr1_target"
		state.TargetRevision = &targetRevision
		translated := "Target"
		state.Units[0].LocaleValue = &translated
	}
	domain := &stubEmailLayoutDomain{state: state}
	return &emailLayoutPort{service: domain}, domain, core.DocumentIdentity{
		Domain: core.DomainEmailLayout, Reference: core.DocumentReference(reference),
	}
}
