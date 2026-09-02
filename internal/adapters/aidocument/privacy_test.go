package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/legal"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

type exactLegalAIDocumentAPI struct {
	state         legal.AIDocument
	result        legal.AIDocumentMutationResult
	authorizeErr  error
	resultErr     error
	mutation      legal.AIDocumentMutation
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (a *exactLegalAIDocumentAPI) LoadAIDocument(
	context.Context,
	string,
	string,
	string,
) (legal.AIDocument, error) {
	a.loadCalls++
	return a.state, nil
}

func (a *exactLegalAIDocumentAPI) ExecuteAIDocumentMutation(
	_ context.Context,
	entityType string,
	entityID string,
	locale string,
	_ legal.AIDocumentExecutionMode,
	compiler legal.AIDocumentMutationCompiler,
) (legal.AIDocumentMutationResult, error) {
	a.executeCalls++
	if a.authorizeErr != nil {
		return legal.AIDocumentMutationResult{}, a.authorizeErr
	}
	if entityType != a.state.EntityType || entityID != a.state.EntityID || locale != a.state.Locale {
		return legal.AIDocumentMutationResult{}, errors.New("unexpected Legal identity or locale")
	}
	a.compilerCalls++
	mutation, err := compiler(a.state)
	if err != nil {
		return legal.AIDocumentMutationResult{}, err
	}
	a.mutation = mutation
	if a.resultErr != nil {
		return legal.AIDocumentMutationResult{}, a.resultErr
	}
	return a.result, nil
}

func TestPrivacyRegistrationProjectsOfficialPolicyAndExactTargetPresence(t *testing.T) {
	_, err := NewPrivacyRegistration(nil)
	require.Error(t, err)
	registration, err := NewPrivacyRegistration(&legal.AIDocumentService{})
	require.NoError(t, err)
	require.Equal(t, core.DomainPrivacy, registration.Domain)
	port := registration.Port.(*legalAIDocumentPort)

	blockID := uuid.New().String()
	document := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{
			{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{{
						Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "source"}},
					}},
				}},
			}}},
			{Locale: "ko"},
		},
	}
	rows, err := contentv1.FlattenRichTextDocumentStorage(document, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE)
	require.NoError(t, err)
	empty := ""
	targetRevision := "legal-target-revision-1"
	projected, err := port.project(
		core.DocumentIdentity{Domain: core.DomainPrivacy, Reference: "privacy-a"}, "ko",
		legal.AIDocument{
			EntityType: "privacy", EntityID: "privacy-a", DocumentID: uuid.New(), Revision: uuid.NewString(),
			SourceLocale: "en", Locale: "ko", LocaleExists: true, TargetRevision: &targetRevision,
			Title: &empty, Rows: rows,
		},
	)
	require.NoError(t, err)
	require.True(t, projected.LocaleExists)
	require.Equal(t, core.LocaleRoleNonSource, projected.Role())
	require.Equal(t, core.Revision(targetRevision), *projected.TargetRevision)
	require.Len(t, projected.Nodes, 2)
	require.Equal(t, legalMetadataBlockID, projected.Nodes[0].ID)
	require.Equal(t, "", projected.Nodes[0].Localized[0].Value.Text, "explicit empty target title must remain present")
	require.Empty(t, projected.Nodes[1].Localized, "missing target block must not source-fallback into locale-owned values")
	require.NotEqual(t, contentv1.ContentBlockCatalogFingerprint, projected.Catalog.Fingerprint, "Legal metadata must participate in the catalog fingerprint")
}

func TestLegalMetadataCompilerKeepsSourceRequiredAndTargetExplicitEmpty(t *testing.T) {
	source := core.Document{SourceLocale: "en", Locale: "en"}
	target := core.Document{SourceLocale: "en", Locale: "ko"}

	compiled := compiledLegalMutation{}
	issue := validateLegalMetadataOperation(2, source, core.SetFieldOperation(legalMetadataBlockID, legalTitleField, core.Text("  ")), &compiled)
	require.NotNil(t, issue)
	require.Equal(t, 2, issue.Operation)

	compiled = compiledLegalMutation{}
	issue = validateLegalMetadataOperation(0, target, core.SetFieldOperation(legalMetadataBlockID, legalTitleField, core.Text("")), &compiled)
	require.Nil(t, issue)
	require.True(t, compiled.setTitle)
	require.NotNil(t, compiled.title)
	require.Equal(t, "", *compiled.title)

	compiled = compiledLegalMutation{}
	issue = validateLegalMetadataOperation(0, target, core.UnsetFieldOperation(legalMetadataBlockID, legalTitleField), &compiled)
	require.NotNil(t, issue)
	require.Contains(t, issue.Message, "explicit empty")
}

func TestPrivacyAndTermsExactMutationPathsDoNotEnterPublicLoad(t *testing.T) {
	for _, test := range []struct {
		name       string
		domain     core.Domain
		entityType string
		register   func(*legal.AIDocumentService) (DomainRegistration, error)
	}{
		{name: "privacy", domain: core.DomainPrivacy, entityType: "privacy", register: NewPrivacyRegistration},
		{name: "terms", domain: core.DomainTerms, entityType: "terms", register: NewTermsRegistration},
	} {
		t.Run(test.name, func(t *testing.T) {
			registration, err := test.register(&legal.AIDocumentService{})
			require.NoError(t, err)
			port := registration.Port.(*legalAIDocumentPort)
			entityID, documentID, revision, nextRevision, contributor :=
				uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			title := "source"
			api := &exactLegalAIDocumentAPI{
				state: legal.AIDocument{
					EntityType: test.entityType, EntityID: entityID.String(), DocumentID: documentID,
					Revision: revision.String(), SourceLocale: "en", Locale: "en", LocaleExists: true,
					Title: &title, Rows: legalExactTestRows(t, uuid.NewString()), ViewerMemberID: contributor.String(),
				},
				result: legal.AIDocumentMutationResult{Revision: nextRevision.String(), Changed: true},
			}
			port.application = api
			service, err := core.NewService(port)
			require.NoError(t, err)
			request := core.ApplyRequest{
				Protocol: core.ProtocolVersion, Profile: test.domain,
				Document: core.DocumentReference(entityID.String()), Locale: "en",
				ExpectedDocumentRevision: core.Revision(revision.String()),
				Operations: []core.Operation{
					core.SetFieldOperation(legalMetadataBlockID, legalTitleField, core.Text("changed")),
				},
			}

			validation, err := service.Validate(context.Background(), request)
			require.NoError(t, err)
			require.True(t, validation.Valid())
			result, err := service.Apply(context.Background(), request)
			require.NoError(t, err)
			require.Equal(t, core.Revision(nextRevision.String()), result.DocumentRevision)
			require.Equal(t, 2, api.executeCalls)
			require.Equal(t, 2, api.compilerCalls)
			require.Zero(t, api.loadCalls)
		})
	}
}

func TestLegalExactMutationAuthorizesBeforeCompiler(t *testing.T) {
	registration, err := NewPrivacyRegistration(&legal.AIDocumentService{})
	require.NoError(t, err)
	port := registration.Port.(*legalAIDocumentPort)
	entityID := uuid.New()
	denied := errors.New("privacy not found")
	api := &exactLegalAIDocumentAPI{
		state: legal.AIDocument{
			EntityType: "privacy", EntityID: entityID.String(), Locale: "en",
		},
		authorizeErr: denied,
	}
	port.application = api
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPrivacy,
		Document: core.DocumentReference(entityID.String()), Locale: "en",
		ExpectedDocumentRevision: core.Revision(uuid.NewString()),
		Operations:               []core.Operation{core.DeleteBlockOperation("unknown-block")},
	}

	_, err = service.Validate(context.Background(), request)
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, api.executeCalls)
	require.Zero(t, api.compilerCalls)
	require.Zero(t, api.loadCalls)
}

func TestPrivacyExactTargetMutationPassesTargetFenceAndMapsTargetConflict(t *testing.T) {
	registration, err := NewPrivacyRegistration(&legal.AIDocumentService{})
	require.NoError(t, err)
	port := registration.Port.(*legalAIDocumentPort)
	entityID, documentID, documentRevision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	targetRevision := "legal-target-revision-1"
	nextTargetRevision := "legal-target-revision-2"
	title := "target"
	api := &exactLegalAIDocumentAPI{
		state: legal.AIDocument{
			EntityType: "privacy", EntityID: entityID.String(), DocumentID: documentID,
			Revision: documentRevision.String(), TargetRevision: &targetRevision,
			SourceLocale: "en", Locale: "ko", LocaleExists: true,
			Title: &title, Rows: legalExactTestRows(t, uuid.NewString()), ViewerMemberID: contributor.String(),
		},
		result: legal.AIDocumentMutationResult{
			Revision: documentRevision.String(), TargetRevision: &nextTargetRevision, Changed: true,
		},
	}
	port.application = api
	service, err := core.NewService(port)
	require.NoError(t, err)
	expectedTarget := core.Revision(targetRevision)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPrivacy,
		Document: core.DocumentReference(entityID.String()), Locale: "ko",
		ExpectedDocumentRevision: core.Revision(documentRevision.String()),
		ExpectedTargetRevision:   &expectedTarget,
		Operations: []core.Operation{
			core.SetFieldOperation(legalMetadataBlockID, legalTitleField, core.Text("changed target")),
		},
	}
	result, err := service.Apply(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, core.Revision(documentRevision.String()), result.DocumentRevision)
	require.Equal(t, core.Revision(nextTargetRevision), *result.TargetRevision)
	require.Equal(t, targetRevision, *api.mutation.ExpectedTargetRevision)

	currentTarget := "legal-target-revision-3"
	api.resultErr = &legal.AIDocumentRevisionConflict{
		Kind: legal.AIDocumentTargetRevisionConflict, CurrentRevision: documentRevision.String(),
		CurrentTargetRevision: &currentTarget,
	}
	_, err = service.Apply(t.Context(), request)
	var conflict *core.ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, core.ConflictTargetRevision, conflict.Conflict.Code)
	require.Equal(t, core.Revision(documentRevision.String()), conflict.Conflict.CurrentDocumentRevision)
	require.Equal(t, core.Revision(currentTarget), *conflict.Conflict.CurrentTargetRevision)
}

func legalExactTestRows(t *testing.T, blockID string) []contentv1.ContentStorageRow {
	t.Helper()
	document := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
			Locale: "en",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID,
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{
						Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "source"}},
					}},
				}},
			}},
		}},
	}
	rows, err := contentv1.FlattenRichTextDocumentStorage(
		document,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	require.NoError(t, err)
	return rows
}
