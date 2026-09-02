package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
)

type fakeMenuAIDocumentDomain struct {
	snapshot       menudomain.AIDocumentSnapshot
	loadErr        error
	validation     []menudomain.AIDocumentIssue
	applyResult    menudomain.AIDocumentApplyResult
	applyErr       error
	appliedCommand menudomain.AIDocumentApply
	authorizeErr   error
	loadCalls      int
	executeCalls   int
	compilerCalls  int
	action         menudomain.AIDocumentMutationAction
}

func (f *fakeMenuAIDocumentDomain) LoadAIDocument(
	context.Context,
	string,
	string,
) (menudomain.AIDocumentSnapshot, error) {
	f.loadCalls++
	return f.snapshot, f.loadErr
}

func (f *fakeMenuAIDocumentDomain) ExecuteAIDocumentMutation(
	_ context.Context,
	menuID string,
	locale string,
	action menudomain.AIDocumentMutationAction,
	_ menudomain.AIDocumentExecutionMode,
	compiler menudomain.AIDocumentMutationCompiler,
) (menudomain.AIDocumentApplyResult, error) {
	f.executeCalls++
	f.action = action
	if f.authorizeErr != nil {
		return menudomain.AIDocumentApplyResult{}, f.authorizeErr
	}
	if menuID != f.snapshot.ID || locale != f.snapshot.Locale {
		return menudomain.AIDocumentApplyResult{}, errors.New("unexpected Menu identity or locale")
	}
	f.compilerCalls++
	command, err := compiler(f.snapshot)
	if err != nil {
		return menudomain.AIDocumentApplyResult{}, err
	}
	f.appliedCommand = command
	if len(f.validation) != 0 {
		return menudomain.AIDocumentApplyResult{}, &menudomain.AIDocumentValidationError{
			Issues: append([]menudomain.AIDocumentIssue(nil), f.validation...),
		}
	}
	return f.applyResult, f.applyErr
}

func TestNewMenuRegistrationRejectsNilAndProjectsStableSourceTree(t *testing.T) {
	_, err := NewMenuRegistration(nil)
	require.ErrorContains(t, err, "required")
	var typedNil *fakeMenuAIDocumentDomain
	_, err = NewMenuRegistration(typedNil)
	require.ErrorContains(t, err, "required")

	sourceSnapshot := menuAdapterSnapshot("ko", true)
	sourceSnapshot.Labels = map[string]string{"parent": "현재 원문", "child": ""}
	domain := &fakeMenuAIDocumentDomain{snapshot: sourceSnapshot}
	registration, err := NewMenuRegistration(domain)
	require.NoError(t, err)
	require.Equal(t, core.DomainMenu, registration.Domain)
	document, err := registration.Port.Load(t.Context(), core.DocumentIdentity{
		Domain: core.DomainMenu, Reference: "menu-1",
	}, "ko")
	require.NoError(t, err)
	require.Equal(t, core.BlockID("menu-1"), document.Nodes[0].ID)
	require.Equal(t, menuBlockKind, document.Nodes[0].Kind)
	require.Equal(t, core.BlockID("menu-1"), document.Nodes[1].Parent)
	require.Equal(t, core.BlockID("parent"), document.Nodes[2].Parent)
	require.Equal(t, core.Text("현재 원문"), document.Nodes[1].Localized[0].Value)
	require.Equal(t, core.Text(""), document.Nodes[2].Localized[0].Value)
	require.Equal(t, menuCatalog.Fingerprint, document.Catalog.Fingerprint)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	_, err = service.Open(t.Context(), core.OpenRequest{
		Document: core.DocumentIdentity{Domain: core.DomainMenu, Reference: "menu-1"}, Locale: "ko",
	})
	require.NoError(t, err, "projected Menu document and catalog must pass shared validation")
}

func TestMenuTargetProjectionIsValuesOnlyAndPreservesExplicitEmpty(t *testing.T) {
	snapshot := menuAdapterSnapshot("en", true)
	snapshot.Labels = map[string]string{"parent": "", "child": "Child"}
	domain := &fakeMenuAIDocumentDomain{snapshot: snapshot}
	registration, err := NewMenuRegistration(domain)
	require.NoError(t, err)
	document, err := registration.Port.Load(t.Context(), core.DocumentIdentity{
		Domain: core.DomainMenu, Reference: "menu-1",
	}, "en")
	require.NoError(t, err)
	require.Len(t, document.Nodes[1].Localized, 1)
	require.Equal(t, core.Text(""), document.Nodes[1].Localized[0].Value)
	require.Len(t, document.Nodes[2].Localized, 1)
	require.Equal(t, core.Text("Child"), document.Nodes[2].Localized[0].Value)
	require.Empty(t, document.Nodes[3].Localized, "fixed-ja label cannot be owned by the en target")

	snapshot.Labels = map[string]string{}
	domain.snapshot = snapshot
	document, err = registration.Port.Load(t.Context(), core.DocumentIdentity{
		Domain: core.DomainMenu, Reference: "menu-1",
	}, "en")
	require.NoError(t, err)
	require.Empty(t, document.Nodes[1].Localized, "missing target label must not be projected as source fallback")
}

func TestMenuExactValidationDryRunsMenuCompiler(t *testing.T) {
	snapshot := menuAdapterSnapshot("en", true)
	snapshot.Labels = map[string]string{"parent": "Parent"}
	domain := &fakeMenuAIDocumentDomain{snapshot: snapshot}
	registration, err := NewMenuRegistration(domain)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)

	validation, err := service.Validate(t.Context(), menuApplyRequest(
		core.SetFieldOperation("parent", menuFieldLabel, core.Text("")),
	))
	require.NoError(t, err)
	require.True(t, validation.Valid())
	require.Len(t, domain.appliedCommand.Operations, 1)
	require.Equal(t, menudomain.AIDocumentSetItemField, domain.appliedCommand.Operations[0].Kind)
	require.Equal(t, "", domain.appliedCommand.Operations[0].Value.Text)

	validation, err = service.Validate(t.Context(), menuApplyRequest(
		core.UnsetFieldOperation("parent", menuFieldLabel),
	))
	require.NoError(t, err)
	require.Len(t, validation.Issues, 1)
	require.Equal(t, core.IssueTargetFieldForbidden, validation.Issues[0].Code)

	domain.validation = []menudomain.AIDocumentIssue{{
		Operation: 1, Code: menudomain.AIDocumentIssueTargetForbidden,
		Handle: "item:fixed/label", Message: "fixed locale",
	}}
	validation, err = service.Validate(t.Context(), menuApplyRequest(
		core.SetFieldOperation("parent", menuFieldLabel, core.Text("Parent")),
		core.SetFieldOperation("fixed", menuFieldLabel, core.Text("Wrong")),
	))
	require.NoError(t, err)
	require.Len(t, validation.Issues, 1)
	require.Equal(t, 1, validation.Issues[0].Operation)
	require.Equal(t, core.IssueTargetFieldForbidden, validation.Issues[0].Code)

	validation, err = service.Validate(t.Context(), menuApplyRequest(
		core.DeleteBlockOperation("menu-1"),
	))
	require.NoError(t, err)
	require.Len(t, validation.Issues, 1)
	require.Equal(t, core.IssueSourceAuthorityRequired, validation.Issues[0].Code)
}

func TestMenuExactApplyForwardsOwningCommandAndMapsConflict(t *testing.T) {
	domain := &fakeMenuAIDocumentDomain{
		snapshot:    menuAdapterSnapshot("en", true),
		applyResult: menudomain.AIDocumentApplyResult{DocumentRevision: "revision-1", TargetRevision: adapterStringPointer("target-revision-2"), Changed: true},
	}
	registration, err := NewMenuRegistration(domain)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	request := menuApplyRequest(core.SetFieldOperation("parent", menuFieldLabel, core.Text("Parent")))
	result, err := service.Apply(t.Context(), request)
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, core.Revision("revision-1"), result.DocumentRevision)
	require.Equal(t, core.Revision("target-revision-2"), *result.TargetRevision)
	require.Equal(t, "menu-1", domain.appliedCommand.MenuID)
	require.Equal(t, "revision-1", domain.appliedCommand.ExpectedDocumentRevision)
	require.Equal(t, "target-revision-1", *domain.appliedCommand.ExpectedTargetRevision)
	require.Equal(t, "Parent", domain.appliedCommand.Operations[0].Value.Text)

	domain.applyErr = &menudomain.AIDocumentRevisionConflict{
		Target: true, CurrentDocumentRevision: "revision-1", CurrentTargetRevision: adapterStringPointer("target-revision-3"),
		AffectedHandles: []string{"field:parent/label"},
	}
	_, err = service.Apply(t.Context(), request)
	var conflict *core.ConflictError
	require.True(t, errors.As(err, &conflict))
	require.Equal(t, core.ConflictTargetRevision, conflict.Conflict.Code)
	require.Equal(t, core.Revision("target-revision-3"), *conflict.Conflict.CurrentTargetRevision)
}

func TestMenuExactMutationPathDoesNotEnterPublicLoad(t *testing.T) {
	domain := &fakeMenuAIDocumentDomain{
		snapshot:    menuAdapterSnapshot("en", true),
		applyResult: menudomain.AIDocumentApplyResult{DocumentRevision: "revision-1", TargetRevision: adapterStringPointer("target-revision-2"), Changed: true},
	}
	registration, err := NewMenuRegistration(domain)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainMenu,
		Document: "menu-1", Locale: "en", ExpectedDocumentRevision: "revision-1", ExpectedTargetRevision: coreRevisionPointer("target-revision-1"),
		Operations: []core.Operation{core.SetFieldOperation("parent", menuFieldLabel, core.Text("Parent"))},
	}

	validation, err := service.Validate(t.Context(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	result, err := service.Apply(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, core.Revision("target-revision-2"), *result.TargetRevision)
	require.Equal(t, 2, domain.executeCalls)
	require.Equal(t, 2, domain.compilerCalls)
	require.Zero(t, domain.loadCalls)
	require.Equal(t, menudomain.AIDocumentMutationEdit, domain.action)
}

func TestMenuExactMutationSelectsManageBeforeCompilerAndHidesUnauthorizedShape(t *testing.T) {
	denied := errors.New("menu not found")
	domain := &fakeMenuAIDocumentDomain{
		snapshot:     menuAdapterSnapshot("ko", true),
		authorizeErr: denied,
	}
	registration, err := NewMenuRegistration(domain)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainMenu,
		Document: "menu-1", Locale: "ko", ExpectedDocumentRevision: "revision-1",
		Operations: []core.Operation{core.DeleteBlockOperation("menu-1")},
	}

	_, err = service.Validate(t.Context(), request)
	require.ErrorIs(t, err, denied)
	require.Equal(t, menudomain.AIDocumentMutationManage, domain.action)
	require.Zero(t, domain.compilerCalls, "unauthorized request reached the adapter compiler")
	require.Zero(t, domain.loadCalls, "unauthorized request entered the public read path")
}

func menuAdapterSnapshot(locale string, exists bool) menudomain.AIDocumentSnapshot {
	fixedMode := "fixed_locale"
	japanese := "ja"
	snapshot := menudomain.AIDocumentSnapshot{
		ID: "menu-1", Name: "Main", SourceLocale: "ko", Locale: locale,
		LocaleExists: exists, DocumentRevision: "revision-1", Labels: map[string]string{},
		Items: []menudomain.AIDocumentItem{
			{ID: "parent", Label: "상위", LinkType: "custom", URL: adapterStringPointer("/parent"), VisibilityMode: adapterStringPointer("roles"), VisibilityRoles: []string{"admin", "author"}, Children: []menudomain.AIDocumentItem{{
				ID: "child", Label: "하위", LinkType: "custom", URL: adapterStringPointer("/child"),
			}}},
			{ID: "fixed", Label: "고정", LinkType: "custom", URL: adapterStringPointer("/fixed"), LocalizationMode: &fixedMode, FixedLocale: &japanese},
		},
	}
	if locale == "ko" {
		snapshot.SourceValuesStored = true
		snapshot.Labels = map[string]string{"parent": "상위", "child": "하위"}
	} else if exists {
		snapshot.TargetRevision = adapterStringPointer("target-revision-1")
	}
	return snapshot
}

func adapterStringPointer(value string) *string { return &value }

func menuApplyRequest(operations ...core.Operation) core.ApplyRequest {
	return core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainMenu,
		Document: "menu-1", Locale: "en", ExpectedDocumentRevision: "revision-1", ExpectedTargetRevision: coreRevisionPointer("target-revision-1"),
		Operations: operations,
	}
}

func coreRevisionPointer(value string) *core.Revision {
	revision := core.Revision(value)
	return &revision
}
