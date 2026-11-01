package aidocumentadapter

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/legal"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	programeventdomain "github.com/echovisionlab/geul-api/internal/programevent"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

type goldenProgramEventDocumentAPI struct {
	*exactProgramEventDocumentAPI
	err error
}

func (a *goldenProgramEventDocumentAPI) ExecuteAIDocument(
	ctx context.Context,
	execution programeventdomain.ExecuteAIDocumentCommand,
) (programeventdomain.AIDocumentResult, error) {
	result, err := a.exactProgramEventDocumentAPI.ExecuteAIDocument(ctx, execution)
	if err == nil && a.err != nil {
		return programeventdomain.AIDocumentResult{}, a.err
	}
	return result, err
}

func TestPageExactMutationGolden(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	api := &exactPageDocumentAPI{}
	service, err := core.NewService(&pagePort{application: api, codec: codec, catalog: pageCatalog(codec)})
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "page", domain: core.DomainPage, reference: goldenEntityID,
		sourceLocale: "en", targetLocale: "ko", service: service,
		handles: pageOperationHandles,
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			state := pageStateForCodecTest(goldenBlockID)
			revision := uuid.MustParse(goldenDocumentRevision)
			state.Page.ID, state.ViewerMemberID = goldenEntityID, goldenMemberID
			state.Revision = goldenDocumentRevision
			state.Snapshot.Document.ID, state.Snapshot.Document.Revision = uuid.MustParse(goldenDocumentID), revision
			if target {
				title := "ko title"
				state.Locale, state.Title, state.Summary = "ko", &title, nil
				state.Document.Locale, state.Document.LocaleOverlay.Locale = "ko", "ko"
				state.Document.LocaleOverlay.Sections = nil
				state.TargetRevision = goldenStringPointer(goldenTargetRevision)
			}
			api.state = state
			api.result = pagedomain.AIDocumentMutationResult{
				DocumentRevision: result.DocumentRevision, TargetRevision: result.TargetRevision, Changed: result.Changed,
			}
			api.executeErr = err
		},
		conflict: func(target bool) error {
			if target {
				return &core.RevisionConflict{
					Kind:                    core.ConflictTargetRevision,
					CurrentDocumentRevision: goldenDocumentRevision,
					CurrentTargetRevision:   goldenStringPointer(goldenCurrentTargetRevision),
					Message:                 "Page AI document revision changed: current revision is " + "0a000000-0000-4000-8000-000000000001",
				}
			}
			return &core.RevisionConflict{
				Kind:                    core.ConflictDocumentRevision,
				CurrentDocumentRevision: goldenCurrentDocumentRevision,
				Message:                 "Page AI document revision changed: current revision is " + "0a000000-0000-4000-8000-000000000003",
			}
		},
		sourceOps:    []core.Operation{core.SetFieldOperation(pageMetadataBlockID, pageTitleField, core.Text("changed title"))},
		targetOps:    []core.Operation{core.SetFieldOperation(pageMetadataBlockID, pageTitleField, core.Text("changed target title"))},
		structureOps: []core.Operation{core.DeleteBlockOperation(goldenBlockID)},
	})
}

func TestLegalExactMutationGolden(t *testing.T) {
	for _, legalDomain := range []struct {
		name       string
		domain     core.Domain
		entityType string
		register   func(*legal.AIDocumentService) (DomainRegistration, error)
	}{
		{name: "privacy", domain: core.DomainPrivacy, entityType: "privacy", register: NewPrivacyRegistration},
		{name: "terms", domain: core.DomainTerms, entityType: "terms", register: NewTermsRegistration},
	} {
		t.Run(legalDomain.name, func(t *testing.T) {
			registration, err := legalDomain.register(&legal.AIDocumentService{})
			require.NoError(t, err)
			port := registration.Port.(*legalAIDocumentPort)
			api := &exactLegalAIDocumentAPI{}
			port.application = api
			service, err := core.NewService(port)
			require.NoError(t, err)
			runMutationGolden(t, mutationGoldenSubject{
				name: "legal_" + legalDomain.name, domain: legalDomain.domain, reference: goldenEntityID,
				sourceLocale: "en", targetLocale: "ko", service: service,
				handles: legalOperationHandles,
				arrange: func(target bool, result mutationGoldenDomainResult, err error) {
					locale := goldenLocale(target)
					title := locale + " title"
					api.state = legal.AIDocument{
						EntityType: legalDomain.entityType, EntityID: goldenEntityID, DocumentID: uuid.MustParse(goldenDocumentID),
						Revision: goldenDocumentRevision, TargetRevision: goldenTargetRevisionFor(target),
						SourceLocale: "en", Locale: locale, LocaleExists: true, Title: &title,
						Rows: legalExactTestRows(t, goldenBlockID), ViewerMemberID: goldenMemberID,
					}
					api.result = legal.AIDocumentMutationResult{
						Revision: result.DocumentRevision, TargetRevision: result.TargetRevision, Changed: result.Changed,
					}
					api.resultErr = err
				},
				conflict: func(target bool) error {
					if target {
						return &core.RevisionConflict{
							Kind:                    core.ConflictTargetRevision,
							CurrentDocumentRevision: goldenDocumentRevision,
							CurrentTargetRevision:   goldenStringPointer(goldenCurrentTargetRevision),
							Message:                 "Legal AI document revision changed; reload before saving",
						}
					}
					return &core.RevisionConflict{
						Kind:                    core.ConflictDocumentRevision,
						CurrentDocumentRevision: goldenCurrentDocumentRevision,
						Message:                 "Legal AI document revision changed; reload before saving",
					}
				},
				sourceOps: []core.Operation{
					core.SetFieldOperation(legalMetadataBlockID, legalTitleField, core.Text("changed title")),
					goldenContentEdit("changed source"),
				},
				targetOps: []core.Operation{
					core.SetFieldOperation(legalMetadataBlockID, legalTitleField, core.Text("changed target title")),
				},
				structureOps: goldenRichTextStructure(),
			})
		})
	}
}

func TestProgramEventExactMutationGolden(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT)
	require.NoError(t, err)
	api := &goldenProgramEventDocumentAPI{exactProgramEventDocumentAPI: &exactProgramEventDocumentAPI{}}
	service, err := core.NewService(&programEventPort{service: api, codec: codec})
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "program_event", domain: core.DomainProgramEvent, reference: goldenEntityID,
		sourceLocale: "en", targetLocale: "ko", service: service,
		handles: programEventOperationHandles,
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			locale := goldenLocale(target)
			api.state = programeventdomain.AIDocumentState{
				EventID: goldenEntityID, ContentDocumentID: uuid.MustParse(goldenDocumentID),
				DocumentRevision: goldenDocumentRevision, TargetRevision: goldenTargetRevisionFor(target),
				SourceLocale: "en", RequestedLocale: locale, LocaleExists: true, Title: locale + " title",
				LocalizedDocument: goldenFileDocument(t, contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT, locale),
				ViewerMemberID:    goldenMemberID,
			}
			api.result = programeventdomain.AIDocumentResult{
				DocumentRevision: result.DocumentRevision, TargetRevision: result.TargetRevision, Changed: result.Changed,
			}
			api.err = err
		},
		conflict: func(target bool) error {
			if target {
				return &core.RevisionConflict{
					Kind:                    core.ConflictTargetRevision,
					CurrentDocumentRevision: goldenDocumentRevision,
					CurrentTargetRevision:   goldenStringPointer(goldenCurrentTargetRevision),
					Message:                 fmt.Sprintf("Program Event AI document revision changed: current revision is %s", "0a000000-0000-4000-8000-000000000001"),
				}
			}
			return &core.RevisionConflict{
				Kind:                    core.ConflictDocumentRevision,
				CurrentDocumentRevision: goldenCurrentDocumentRevision,
				Message:                 fmt.Sprintf("Program Event AI document revision changed: current revision is %s", "0a000000-0000-4000-8000-000000000003"),
			}
		},
		sourceOps:    []core.Operation{goldenContentEdit("changed source")},
		targetOps:    []core.Operation{goldenContentEdit("changed target")},
		structureOps: goldenRichTextStructure(),
		extra:        []mutationGoldenScenarioInput{goldenAttachFileRejection()},
	})
}

func TestMenuExactMutationGolden(t *testing.T) {
	domain := &fakeMenuAIDocumentDomain{}
	registration, err := NewMenuRegistration(domain)
	require.NoError(t, err)
	service, err := core.NewService(registration.Port)
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "menu", domain: core.DomainMenu, reference: goldenEntityID,
		sourceLocale: "ko", targetLocale: "en", service: service,
		handles: menuOperationHandles,
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			snapshot := menuAdapterSnapshot("ko", true)
			if target {
				snapshot = menuAdapterSnapshot("en", true)
				snapshot.TargetRevision = goldenStringPointer(goldenTargetRevision)
			}
			snapshot.ID, snapshot.DocumentRevision = goldenEntityID, goldenDocumentRevision
			domain.snapshot = snapshot
			domain.applyResult = menudomain.AIDocumentApplyResult{
				DocumentRevision: result.DocumentRevision, TargetRevision: result.TargetRevision, Changed: result.Changed,
			}
			domain.applyErr = err
		},
		conflict: func(target bool) error {
			if target {
				return &core.RevisionConflict{
					Kind:                    core.ConflictTargetRevision,
					CurrentDocumentRevision: goldenDocumentRevision,
					CurrentTargetRevision:   goldenStringPointer(goldenCurrentTargetRevision),
					AffectedHandles:         []string{"menu-domain-handle"},
					Message:                 "Menu AI document revision conflict",
				}
			}
			return &core.RevisionConflict{
				CurrentDocumentRevision: goldenCurrentDocumentRevision,
				AffectedHandles:         []string{"menu-domain-handle"},
				Kind:                    core.ConflictDocumentRevision,
				Message:                 "Menu AI document revision conflict",
			}
		},
		sourceOps:    []core.Operation{core.SetFieldOperation("parent", menuFieldLabel, core.Text("changed parent"))},
		targetOps:    []core.Operation{core.SetFieldOperation("parent", menuFieldLabel, core.Text("changed target parent"))},
		structureOps: []core.Operation{core.DeleteBlockOperation("child")},
		extra: []mutationGoldenScenarioInput{{
			name:       "domain validation issues",
			operations: []core.Operation{core.SetFieldOperation("parent", menuFieldLabel, core.Text("changed parent"))},
			err: &menudomain.AIDocumentValidationError{Issues: []menudomain.AIDocumentIssue{
				{Operation: 0, Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "parent", Message: "golden invalid"},
				{Operation: 0, Code: menudomain.AIDocumentIssueTargetForbidden, Handle: "parent", Message: "golden forbidden"},
			}},
		}},
	})
}

func TestEmailRichTextExactMutationGolden(t *testing.T) {
	for _, emailDomain := range []core.Domain{core.DomainCampaign, core.DomainEmailTemplate} {
		t.Run(string(emailDomain), func(t *testing.T) {
			stub := &stubEmailRichTextDomain{}
			port, err := newEmailRichTextPort(emailDomain, string(emailDomain), stub)
			require.NoError(t, err)
			service, err := core.NewService(port)
			require.NoError(t, err)
			runMutationGolden(t, mutationGoldenSubject{
				name: "email_richtext_" + string(emailDomain), domain: emailDomain, reference: goldenEntityID,
				sourceLocale: "en", targetLocale: "ko", service: service,
				handles: emailRichTextOperationHandles,
				arrange: func(target bool, result mutationGoldenDomainResult, err error) {
					locale := goldenLocale(target)
					subject := locale + " subject"
					stub.state = emailRichTextState{
						Reference: goldenEntityID, DocumentID: uuid.MustParse(goldenDocumentID),
						DocumentRevision: goldenDocumentRevision, TargetRevision: goldenTargetRevisionFor(target),
						SourceLocale: "en", Locale: locale, LocaleExists: true, ViewerMemberID: goldenMemberID,
						Subject:  &subject,
						Document: goldenParagraphDocument(contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL, locale),
					}
					stub.applyResult = emailRichTextMutationResult(result)
					stub.validateErr, stub.applyErr = err, err
				},
				conflict: func(target bool) error {
					if target {
						return &emailRichTextRevisionConflict{
							kind: core.ConflictTargetRevision, currentDocumentRevision: goldenDocumentRevision,
							currentTargetRevision: goldenStringPointer(goldenCurrentTargetRevision),
						}
					}
					return &emailRichTextRevisionConflict{
						kind: core.ConflictDocumentRevision, currentDocumentRevision: goldenCurrentDocumentRevision,
					}
				},
				sourceOps: []core.Operation{
					core.SetFieldOperation(emailMetadataBlockID, emailSubjectField, core.Text("changed subject")),
					goldenContentEdit("changed source"),
				},
				targetOps: []core.Operation{
					core.SetFieldOperation(emailMetadataBlockID, emailSubjectField, core.Text("changed target subject")),
					goldenContentEdit("changed target"),
				},
				// Email profile Blocks are children of the fixed metadata root.
				structureOps: []core.Operation{
					core.InsertBlockOperation(goldenSecondBlockID, "paragraph", emailMetadataBlockID, goldenBlockID),
					core.MoveBlockOperation(goldenBlockID, emailMetadataBlockID, goldenSecondBlockID),
					core.DeleteBlockOperation(goldenSecondBlockID),
				},
			})
		})
	}
}

const (
	goldenEmailLayoutTextUnit      = "unit:0b000000-0000-4000-8000-000000000001:text"
	goldenEmailLayoutAttributeUnit = "unit:0b000000-0000-4000-8000-000000000002:attr:alt"
)

func TestEmailLayoutExactMutationGolden(t *testing.T) {
	domain := &stubEmailLayoutDomain{}
	service, err := core.NewService(&emailLayoutPort{service: domain})
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "email_layout", domain: core.DomainEmailLayout, reference: goldenEntityID,
		sourceLocale: "en", targetLocale: "ko", service: service,
		handles: func(operation core.Operation) []string {
			return compactOperationHandles(operation, "ko", "email_layout")
		},
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			locale := goldenLocale(target)
			text, attribute := locale+" text", locale+" alt"
			domain.state = emailauthoring.EmailLayoutAIDocumentState{
				LayoutID: goldenEntityID, DocumentRevision: goldenDocumentRevision,
				TargetRevision: goldenTargetRevisionFor(target), SourceLocale: "en", Locale: locale,
				LocaleExists: true, ViewerMemberID: goldenMemberID,
				Units: []emailauthoring.EmailLayoutAIDocumentUnit{
					{Handle: goldenEmailLayoutTextUnit, Kind: "text", Order: 0, SourceValue: "en text", LocaleValue: &text},
					{Handle: goldenEmailLayoutAttributeUnit, Kind: "attribute", Element: "img", Attribute: "alt", Order: 1, SourceValue: "en alt", LocaleValue: &attribute},
				},
			}
			domain.applyResult = emailauthoring.EmailLayoutAIDocumentMutationResult{
				DocumentRevision: result.DocumentRevision, TargetRevision: result.TargetRevision, Changed: result.Changed,
			}
			domain.validateErr, domain.applyErr = err, err
		},
		conflict: func(target bool) error {
			if target {
				return &core.RevisionConflict{
					Kind:                    core.ConflictTargetRevision,
					CurrentDocumentRevision: goldenDocumentRevision,
					CurrentTargetRevision:   goldenStringPointer(goldenCurrentTargetRevision),
					Message:                 fmt.Sprintf("Email Layout AI document revision conflict: current document revision is %q", "0a000000-0000-4000-8000-000000000001"),
				}
			}
			return &core.RevisionConflict{
				Kind:                    core.ConflictDocumentRevision,
				CurrentDocumentRevision: goldenCurrentDocumentRevision,
				Message:                 fmt.Sprintf("Email Layout AI document revision conflict: current document revision is %q", "0a000000-0000-4000-8000-000000000003"),
			}
		},
		sourceOps: []core.Operation{
			core.SetFieldOperation(goldenEmailLayoutTextUnit, emailLayoutContentField, core.Text("changed source")),
			core.SetFieldOperation(goldenEmailLayoutAttributeUnit, emailLayoutContentField, core.Text("changed alt")),
		},
		targetOps: []core.Operation{
			core.SetFieldOperation(goldenEmailLayoutTextUnit, emailLayoutContentField, core.Text("changed target")),
		},
	})
}

func TestFormExactMutationGolden(t *testing.T) {
	api := &exactFormDocumentAPI{}
	service, err := core.NewService(&formPort{service: api, catalog: formCatalog()})
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "form", domain: core.DomainForm, reference: goldenEntityID,
		sourceLocale: "en", targetLocale: "ko", service: service,
		handles: func(operation core.Operation) []string { return compactOperationHandles(operation, "ko", "form") },
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			locale := goldenLocale(target)
			sourceTitle, title := "Contact", locale+" title"
			api.state = formdomain.AIDocumentState{
				FormID: goldenEntityID, DocumentRevision: goldenDocumentRevision,
				TargetRevision: goldenTargetRevisionFor(target), SourceLocale: "en", Locale: locale, LocaleExists: true,
				SourceTitle: &sourceTitle, LocaleTitle: &title,
				SourceSchema: []byte(formAuthoringSchemaForTest), LocaleSchema: []byte(formAuthoringSchemaForTest),
				ViewerMemberID: goldenMemberID,
			}
			api.result = formdomain.AIDocumentMutationResult{
				DocumentRevision: result.DocumentRevision, TargetRevision: result.TargetRevision, Changed: result.Changed,
			}
			api.executeErr = err
		},
		conflict: func(target bool) error {
			if target {
				return &core.RevisionConflict{
					Kind:                    core.ConflictTargetRevision,
					CurrentDocumentRevision: goldenDocumentRevision,
					CurrentTargetRevision:   goldenStringPointer(goldenCurrentTargetRevision),
					Message:                 fmt.Sprintf("Form AI document %s revision conflict", "target"),
				}
			}
			return &core.RevisionConflict{
				Kind:                    core.ConflictDocumentRevision,
				CurrentDocumentRevision: goldenCurrentDocumentRevision,
				Message:                 fmt.Sprintf("Form AI document %s revision conflict", "document"),
			}
		},
		sourceOps: []core.Operation{core.SetFieldOperation(formRootID, formRootTitleField, core.Text("changed title"))},
		targetOps: []core.Operation{core.SetFieldOperation(formRootID, formRootTitleField, core.Text("changed target title"))},
	})
}
