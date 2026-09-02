package emailauthoring

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ApplyEmailLayoutProviderTranslationCandidate applies the request-time unit
// results that still exist in the current marker graph. A target locale that
// became source-owned advances the shared Content Document revision.
func ApplyEmailLayoutProviderTranslationCandidate(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	references CampaignDeliveryReferences,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if tx == nil || store == nil || references == nil || auditWriter == nil || job == nil ||
		job.EntityType != "email_layout" || candidate == nil || !candidate.HasProviderUnitPatch() {
		return errs.Internal(errors.New("email layout provider translation dependencies are required"))
	}
	memberID, err := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if err != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errs.InternalMsg("Email Layout provider translation requires canonical requester Member")
	}
	now := input.Now.UTC()
	if now.IsZero() {
		return errs.InternalMsg("Email Layout provider translation time is required")
	}
	if err := lockEmailLayoutAIDocumentRoot(ctx, tx, job.EntityID, "UPDATE"); err != nil {
		return err
	}
	if err := ensureEmailLayoutMutableForActiveDelivery(ctx, tx, references, job.EntityID); err != nil {
		return err
	}
	authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, job.EntityID, "UPDATE")
	if err != nil {
		return err
	}
	source, err := emailutil.LoadCanonicalLayoutTranslationDocument(ctx, tx, job.EntityID, authority.SourceLocale)
	if err != nil {
		return err
	}
	sourceHTML := derefString(source.ContentHTML)
	descriptors, err := emailutil.ExtractLayoutContentUnits(sourceHTML)
	if err != nil {
		return errs.FailedPrecondition("Email Layout source unit markers require repair before provider apply")
	}
	allowed := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		allowed[descriptor.Handle] = struct{}{}
	}
	entry, err := loadEmailLayoutInterchangeEntry(ctx, tx, job.EntityID, job.TargetLocale, "UPDATE")
	if err != nil {
		return err
	}
	currentValues := make(map[string]string)
	if job.TargetLocale == authority.SourceLocale {
		for _, descriptor := range descriptors {
			currentValues[descriptor.Handle] = descriptor.SourceValue
		}
	} else if entry != nil && entry.ContentHTML != nil {
		currentValues, err = emailutil.ExtractLayoutStoredLocaleValues(*entry.ContentHTML)
		if err != nil {
			return errs.FailedPrecondition("Email Layout target unit markers require repair before provider apply")
		}
	}
	nextValues := maps.Clone(currentValues)
	patch, _ := candidate.ProviderPatch()
	for _, unit := range patch.Units {
		result, ok := patch.Results[unit.UnitID]
		if !ok || unit.ContainerType != translation.ContainerTypeHTMLNode || unit.ContainerID != unit.UnitID {
			continue
		}
		if _, exists := allowed[unit.UnitID]; exists {
			nextValues[unit.UnitID] = result.TranslatedText
		}
	}
	if maps.Equal(currentValues, nextValues) && entry != nil {
		return nil
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if job.TargetLocale == authority.SourceLocale {
		contentHTML, contentText, err := emailutil.ApplyLayoutSourceValues(sourceHTML, nextValues)
		if err != nil {
			return errs.InvalidArgument("content", err.Error())
		}
		result, err := store.AdvanceRevision(
			ctx, tx,
			contentblock.AdvanceInput{DocumentID: authority.DocumentID, ExpectedRevision: authority.DocumentRevision},
			emailLayoutContentFence(references, job.EntityID),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				if err := emailutil.SaveLayoutSourceLocaleDocument(
					ctx, tx, job.EntityID, authority.SourceLocale,
					emailutil.LayoutTranslationDocument{ContentHTML: contentHTML, ContentText: contentText}, now,
				); err != nil {
					return contentblock.MetadataEffect{}, err
				}
				return contentblock.MetadataEffect{
					Changed: true, AffectsTranslationSource: true, SourceLocale: authority.SourceLocale,
					ChangedLocales: []string{authority.SourceLocale},
				}, nil
			},
		)
		if err != nil {
			return err
		}
		if !result.Changed || !result.TranslationSourceChanged {
			return errs.InternalMsg("provider source Email Layout translation did not advance source state")
		}
	} else {
		contentHTML, contentText, err := emailutil.ApplyLayoutLocaleValues(sourceHTML, nextValues)
		if err != nil {
			return errs.InvalidArgument("content", err.Error())
		}
		updatedAt := translation.NextTargetUpdatedAt(now, emailLayoutInterchangeUpdatedAt(entry))
		if err := emailutil.UpsertLayoutTranslationEntry(
			ctx, tx, job.EntityID, job.TargetLocale,
			translation.EntryWrite{ContentHTML: contentHTML, ContentText: contentText, Now: updatedAt},
		); err != nil {
			return err
		}
		if entry == nil {
			operation = sharedtelemetry.AuditItemOperationCreated
		}
	}
	return appendEmailLayoutLocaleContentAudit(
		ctx, tx, auditWriter, memberID.String(), job.EntityID, job.TargetLocale, operation,
	)
}
