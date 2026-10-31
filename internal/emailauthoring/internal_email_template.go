package emailauthoring

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ApplyTemplateBlockBatchCommand applies a collaboration room's Block batch
// to an Email Template in the room's locale.
type ApplyTemplateBlockBatchCommand struct {
	TemplateID             string
	Locale                 string
	ExpectedTargetRevision *string
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
}

// templateCollaboration is one collaboration write of a template room after
// its document and source locale were read.
type templateCollaboration struct {
	templateID   string
	locale       string
	documentID   uuid.UUID
	sourceLocale string
	contributors []string
	now          time.Time
}

// readTemplateCollaboration reads the room's template document and source
// locale.
func (s *InternalService) readTemplateCollaboration(ctx context.Context, templateID, locale string, contributors []string) (templateCollaboration, error) {
	write := templateCollaboration{templateID: templateID, contributors: contributors, now: dbtime.Now()}
	if write.templateID == "" {
		return write, domainerr.Required("email_template_id")
	}
	var err error
	if write.locale, err = emailcontent.NormalizeLocale(locale); err != nil {
		return write, err
	}
	if write.documentID, err = loadEmailTemplateContentDocument(ctx, s.db, templateID, false); err != nil {
		return write, err
	}
	domain, err := loadEmailTemplateSourceContext(ctx, s.db, templateID, false)
	write.sourceLocale = domain.SourceLocale
	return write, err
}

// save runs one collaboration write after the room's single origin Member
// and the room's contributors are proven current.
func (s *InternalService) saveTemplateCollaboration(
	ctx context.Context,
	write templateCollaboration,
	change func(ctx context.Context, tx *gorm.DB, originMemberID string) (emailcontent.CollaborationChange, error),
) (emailcontent.CollaborationChange, error) {
	return mutation.Run(ctx, s.mutations, mutation.Spec[emailcontent.CollaborationChange]{
		Access: emailcontent.CollaborationAccess,
		Change: func(ctx context.Context, tx *gorm.DB) (emailcontent.CollaborationChange, error) {
			originMemberID, err := s.requireOriginMember(ctx, tx, write.contributors)
			if err != nil {
				return emailcontent.CollaborationChange{}, err
			}
			if err := s.checkpoints.RequireCurrentContributors(
				ctx, tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_TEMPLATE, write.templateID, write.contributors,
			); err != nil {
				return emailcontent.CollaborationChange{}, err
			}
			return change(ctx, tx, originMemberID)
		},
		Audit: func(change emailcontent.CollaborationChange) mutation.Audit { return change.Record },
	})
}

// ApplyTemplateBlockBatch applies the batch. A source edit refreshes the
// template's materialized locales and, when it changed the translation
// source, its custom variables.
func (s *InternalService) ApplyTemplateBlockBatch(ctx context.Context, command ApplyTemplateBlockBatchCommand) (emailcontent.CollaborationChange, error) {
	if command.Batch == nil {
		return emailcontent.CollaborationChange{}, domainerr.Required("batch")
	}
	write, err := s.readTemplateCollaboration(ctx, strings.TrimSpace(command.TemplateID), command.Locale, command.Batch.GetContributorMemberIds())
	if err != nil {
		return emailcontent.CollaborationChange{}, err
	}
	batch, err := emailTemplateContent.DecodeCollaborationBatch(write.documentID, write.locale, command.Batch, command.AffectedLocaleValues)
	if err != nil {
		return emailcontent.CollaborationChange{}, err
	}
	if write.locale != write.sourceLocale {
		return s.saveTemplateCollaboration(ctx, write, func(ctx context.Context, tx *gorm.DB, originMemberID string) (emailcontent.CollaborationChange, error) {
			return emailTemplateContent.SaveCollaborationTarget(ctx, tx, s.contentBlocks, emailcontent.TargetLocaleMutation{
				ID: write.templateID, DocumentID: write.documentID, Locale: write.locale,
				Batch: batch, ExpectedDocumentRevision: batch.ExpectedRevision,
				ExpectedTargetRevision: command.ExpectedTargetRevision, Now: write.now,
				Fence: emailTemplateContentFence(s.references, write.templateID),
			}, originMemberID)
		})
	}
	if command.ExpectedTargetRevision != nil {
		return emailcontent.CollaborationChange{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	return s.saveTemplateCollaboration(ctx, write, func(ctx context.Context, tx *gorm.DB, originMemberID string) (emailcontent.CollaborationChange, error) {
		result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, emailTemplateContentFence(s.references, write.templateID))
		if err != nil {
			return emailcontent.CollaborationChange{}, emailTemplateContent.NormalizeContentBlockError(err)
		}
		change := emailcontent.CollaborationChange{
			DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: result.TranslationSourceChanged, ChangedLocales: result.ChangedLocales, Locale: write.locale,
			Record: mutation.Unchanged(),
		}
		if !result.Changed {
			return change, nil
		}
		if err := s.projectTemplateSourceBatch(ctx, tx, write, result); err != nil {
			return emailcontent.CollaborationChange{}, err
		}
		change.Record = emailTemplateContent.LocaleContentChange(originMemberID, write.templateID, write.locale, emailcontent.LocaleContentOperation(true, false, false, true))
		return change, nil
	})
}

// projectTemplateSourceBatch refreshes the materialized locales a source
// batch changed. A source graph or value change also changes the source
// fallback of every existing target projection, so those rows are
// re-materialized from the current snapshot and delivery never serves a
// stale fallback.
func (s *InternalService) projectTemplateSourceBatch(ctx context.Context, tx *gorm.DB, write templateCollaboration, result contentblock.Result) error {
	domain, err := loadEmailTemplateSourceContext(ctx, tx, write.templateID, true)
	if err != nil {
		return err
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, write.documentID, domain.SourceLocale)
	if err != nil {
		return emailTemplateContent.NormalizeContentBlockError(err)
	}
	projectionLocales := result.ChangedLocales
	if result.TranslationSourceChanged {
		projectionLocales = nil
	}
	if err := projectEmailTemplateMaterializedContent(ctx, tx, write.templateID, snapshot, projectionLocales, write.now); err != nil {
		return err
	}
	if !result.TranslationSourceChanged {
		return nil
	}
	return syncSourceEmailTemplateVariables(ctx, tx, write.templateID, snapshot)
}

// UpdateTemplateLocaleMetadataCommand changes an Email Template's subject in
// one locale as the next document revision.
type UpdateTemplateLocaleMetadataCommand struct {
	TemplateID             string
	Locale                 string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	Subject                *string
	Contributors           []string
}

// UpdateTemplateLocaleMetadata stores the subject of a target locale, or
// of the source locale as a translation source change.
func (s *InternalService) UpdateTemplateLocaleMetadata(ctx context.Context, command UpdateTemplateLocaleMetadataCommand) (emailcontent.CollaborationChange, error) {
	templateID := strings.TrimSpace(command.TemplateID)
	if templateID == "" {
		return emailcontent.CollaborationChange{}, domainerr.Required("email_template_id")
	}
	locale, err := emailcontent.NormalizeLocale(command.Locale)
	if err != nil {
		return emailcontent.CollaborationChange{}, err
	}
	if command.Subject == nil {
		return emailcontent.CollaborationChange{}, domainerr.Required("subject")
	}
	expectedRevisionValue := strings.TrimSpace(command.ExpectedRevision)
	expectedRevision, err := uuid.Parse(expectedRevisionValue)
	if err != nil || expectedRevision == uuid.Nil || expectedRevision.String() != expectedRevisionValue {
		return emailcontent.CollaborationChange{}, domainerr.InvalidField("expected_revision", "must be a canonical UUID")
	}
	write, err := s.readTemplateCollaboration(ctx, templateID, locale, command.Contributors)
	if err != nil {
		return emailcontent.CollaborationChange{}, err
	}
	if write.locale != write.sourceLocale {
		return s.saveTemplateCollaboration(ctx, write, func(ctx context.Context, tx *gorm.DB, originMemberID string) (emailcontent.CollaborationChange, error) {
			return emailTemplateContent.SaveCollaborationTarget(ctx, tx, s.contentBlocks, emailcontent.TargetLocaleMutation{
				ID: write.templateID, DocumentID: write.documentID, Locale: write.locale,
				Batch:                    contentblock.Batch{DocumentID: write.documentID, ExpectedRevision: expectedRevision},
				ExpectedDocumentRevision: expectedRevision, ExpectedTargetRevision: command.ExpectedTargetRevision,
				SetSubject: true, Subject: command.Subject, Now: write.now,
				Fence: emailTemplateContentFence(s.references, write.templateID),
			}, originMemberID)
		})
	}
	if command.ExpectedTargetRevision != nil {
		return emailcontent.CollaborationChange{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	return s.saveTemplateCollaboration(ctx, write, func(ctx context.Context, tx *gorm.DB, originMemberID string) (emailcontent.CollaborationChange, error) {
		result, err := s.contentBlocks.AdvanceRevision(
			ctx, tx,
			contentblock.AdvanceInput{DocumentID: write.documentID, ExpectedRevision: expectedRevision},
			emailTemplateContentFence(s.references, write.templateID),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				domain, err := loadEmailTemplateSourceContext(ctx, tx, write.templateID, true)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				return updateEmailTemplateSourceSubject(ctx, tx, write.templateID, domain.SourceLocale, *command.Subject, write.now)
			},
		)
		if err != nil {
			return emailcontent.CollaborationChange{}, emailTemplateContent.NormalizeContentBlockError(err)
		}
		change := emailcontent.CollaborationChange{
			DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: result.TranslationSourceChanged, Locale: write.locale, Record: mutation.Unchanged(),
		}
		if result.Changed {
			change.ChangedLocales = []string{write.locale}
			change.Record = emailTemplateContent.LocaleContentChange(originMemberID, write.templateID, write.locale, emailcontent.LocaleContentOperation(true, false, false, true))
		}
		return change, nil
	})
}

// LoadTemplateDocumentQuery reads an Email Template's document in one locale
// for a collaboration principal.
type LoadTemplateDocumentQuery struct {
	TemplateID string
	Locale     string
	Principal  *intrav1.CollaborationPrincipal
}

// LoadTemplateDocument returns the template's document in the locale after
// authorizing the room's principal to edit it.
func (s *InternalService) LoadTemplateDocument(ctx context.Context, query LoadTemplateDocumentQuery) (emailcontent.CollaborationDocument, error) {
	templateID := strings.TrimSpace(query.TemplateID)
	if templateID == "" {
		return emailcontent.CollaborationDocument{}, domainerr.Required("email_template_id")
	}
	locale, err := emailcontent.NormalizeLocale(query.Locale)
	if err != nil {
		return emailcontent.CollaborationDocument{}, err
	}
	if err := requireEmailTemplateDocumentLoad(ctx, s.db, s.spiceDB, s.references, query.Principal, templateID); err != nil {
		return emailcontent.CollaborationDocument{}, err
	}
	return emailTemplateContent.LoadCollaborationDocument(ctx, s.db, s.contentBlocks, templateID, locale, func(ctx context.Context, tx *gorm.DB) (uuid.UUID, error) {
		return loadEmailTemplateContentDocument(ctx, tx, templateID, true)
	})
}
