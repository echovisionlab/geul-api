package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type testTranslationDomains struct {
	DomainRegistry
}

func translationJobStringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}

func (testTranslationDomains) LockRoot(ctx context.Context, tx *gorm.DB, entityType, entityID string) error {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok {
		return errs.InvalidArgument("target.entity_type", "unsupported translation entity type")
	}
	var row struct{ ID string }
	return tx.WithContext(ctx).Table(definition.RootTable).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("id = ?", entityID).Take(&row).Error
}

func (testTranslationDomains) RequireEditable(context.Context, *gorm.DB, string, string) error {
	return nil
}

func (testTranslationDomains) RequireTranslationInterchangeView(
	context.Context,
	*gorm.DB,
	*auth.SpiceDBClient,
	string,
	string,
) error {
	return nil
}

func (testTranslationDomains) RequireTranslationInterchangeEdit(
	context.Context,
	*gorm.DB,
	*auth.SpiceDBClient,
	string,
	string,
) error {
	return nil
}

func (testTranslationDomains) RequireSourceLocaleEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string, string) error {
	return nil
}

func (testTranslationDomains) RequireLegalEditable(context.Context, *gorm.DB, *auth.SpiceDBClient, string, string) error {
	return nil
}

func (testTranslationDomains) RequireTranslationSourceMutable(context.Context, *gorm.DB, string, string) error {
	return nil
}

func (testTranslationDomains) AppendSourceLocaleAudit(context.Context, *gorm.DB, string, string, string, string) error {
	return nil
}

func (testTranslationDomains) RequestLocaleOG(context.Context, *gorm.DB, *og.Planner, *og.Refresher, string, string, string, string) (bool, error) {
	return false, nil
}

func (testTranslationDomains) RequireJobRead(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	entityType string,
	entityID string,
) error {
	if entityType == "series" {
		return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return series.RequireViewAndLockWithDB(ctx, tx, spiceDB, entityID)
		})
	}
	factories := map[string]func(string) (policyv1.Can, error){
		"post":           policyv1.Post.Edit,
		"page":           policyv1.Page.Edit,
		"work":           policyv1.Work.Edit,
		"artist":         policyv1.Artist.Edit,
		"release":        policyv1.Release.Edit,
		"label":          policyv1.Label.Edit,
		"form":           policyv1.Form.Edit,
		"campaign":       policyv1.Campaign.Edit,
		"email_template": policyv1.EmailTemplate.Edit,
		"email_layout":   policyv1.EmailLayout.Edit,
		"program_event":  policyv1.ProgramEvent.Edit,
		"menu":           policyv1.Menu.Edit,
		"terms":          policyv1.TermsHistory.Edit,
		"privacy":        policyv1.PrivacyHistory.Edit,
	}
	factory, ok := factories[entityType]
	if !ok {
		return errs.InvalidArgument("filters.entity_type", "unsupported translation job reader")
	}
	can, err := factory(entityID)
	if err != nil {
		return errs.InvalidArgument("filters.entity_id", err.Error())
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.Internal(err)
	}
	if !allowed {
		return errs.NoPermission("view translation jobs for", entityType)
	}
	return nil
}

func (testTranslationDomains) BuildExtractionPlan(
	job *model.TranslationJob,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	switch job.EntityType {
	case "post":
		return translation.BuildRichTextExtractionPlan(
			job,
			source,
			translation.RichTextDocumentFields{Title: true, Summary: true},
		)
	case "form":
		return form.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
	case "series":
		return series.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
	case "email_layout":
		return email.BuildLayoutTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
	default:
		return nil, fmt.Errorf("unsupported test translation entity type %q", job.EntityType)
	}
}

func (testTranslationDomains) BuildCandidate(
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
	results map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	switch plan.EntityType {
	case "post":
		return translation.BuildRichTextCandidate(plan, source, results)
	case "form":
		return form.ApplyTranslationCandidate(source, results)
	case "series", "email_layout":
		return BuildGenericCandidate(plan, source, results)
	default:
		return nil, fmt.Errorf("unsupported test translation entity type %q", plan.EntityType)
	}
}

type noopAsyncPublisher struct{}

func (noopAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (noopAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (noopAsyncPublisher) EnqueueProtobufWithExecutor(
	context.Context,
	eventpkg.DBTX,
	string,
	string,
	proto.Message,
) error {
	return nil
}

func testPostVersionLocalizedDocument() *contentv1.LocalizedRichTextDocument {
	blockID := "10000000-0000-4000-8000-000000000001"
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: "test-fingerprint",
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		Locale:                  "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{
				Id: blockID,
				Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
					Props: &contentv1.ParagraphProps{},
				}},
			},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "en",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID,
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{
						Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Hello"}},
					}},
				}},
			}},
		},
	}
}
