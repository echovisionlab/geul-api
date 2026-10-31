package release

import (
	"context"
	"database/sql"
	"errors"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *InternalReleaseService) LoadAIDocumentState(ctx context.Context, releaseID, locale string) (AIDocumentState, error) {
	if s == nil || s.db == nil || s.spiceDB == nil || s.contentBlocks == nil {
		return AIDocumentState{}, errs.Internal(errors.New("release AI document dependencies are not configured"))
	}
	id, err := uuidFromCanonicalString(releaseID)
	if err != nil {
		return AIDocumentState{}, errs.InvalidArgument("release_id", "must be a canonical UUID")
	}
	locale, err = normalizeReleaseDocumentLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.IdentityID == "" || principal.MemberID == "" {
		return AIDocumentState{}, errs.AuthenticationRequired()
	}
	var state AIDocumentState
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadReleaseAIDocumentRoot(ctx, tx, id.String(), "KEY SHARE")
		if err != nil {
			return err
		}
		_, valid := releaseAIDocumentLifecycle(root.Status)
		if !valid {
			return errs.InternalMsg("Release has an unsupported lifecycle status")
		}
		if err := requireReleaseAction(ctx, s.spiceDB, id.String(), releaseActionView); err != nil {
			return err
		}
		state, err = s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, locale, principal.MemberID.String(),
		)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return state, err
}

type releaseAIDocumentRoot struct {
	ID           string     `gorm:"column:id"`
	Status       string     `gorm:"column:status"`
	SourceLocale string     `gorm:"column:source_locale"`
	DocumentID   *uuid.UUID `gorm:"column:content_document_id"`
}

func loadReleaseAIDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	releaseID string,
	lock string,
) (releaseAIDocumentRoot, error) {
	query := tx.WithContext(ctx).
		Table("release").
		Select("id::text AS id", "status", "source_locale", "content_document_id").
		Where("id = ?::uuid", releaseID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	var root releaseAIDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return releaseAIDocumentRoot{}, errs.NotFound("release", releaseID)
		}
		return releaseAIDocumentRoot{}, errs.Internal(err)
	}
	if root.DocumentID == nil || *root.DocumentID == uuid.Nil {
		return releaseAIDocumentRoot{}, errs.FailedPrecondition("Release content document is not initialized")
	}
	return root, nil
}

func (s *InternalReleaseService) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root releaseAIDocumentRoot,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	target, err := loadReleaseTargetLocaleState(
		ctx, tx, s.contentBlocks, root.ID, *root.DocumentID, locale, false,
	)
	if err != nil {
		return AIDocumentState{}, err
	}
	if target.Snapshot.Document.Profile != releaseContentProfile {
		return AIDocumentState{}, errs.InternalMsg("Release AI document requires the compact content profile")
	}
	exists := target.TargetMetadata != nil
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(target.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizeReleaseContentBlockError(err)
	}
	if !exists && releaseSnapshotContainsLocale(target.Snapshot, locale) {
		return AIDocumentState{}, errs.InternalMsg("Release locale overlay exists without translation metadata")
	}
	creditIDs, err := loadReleaseCreditIDs(ctx, tx, root.ID)
	if err != nil {
		return AIDocumentState{}, errs.Internal(err)
	}
	state := AIDocumentState{
		ReleaseID: root.ID, Status: root.Status, DocumentID: *root.DocumentID,
		DocumentRevision: target.Snapshot.Document.Revision.String(), SourceLocale: target.SourceLocale,
		Locale: locale, LocaleExists: exists, Document: document,
		CreditIDs: creditIDs,
		SourceMetadata: AIDocumentLocaleMetadata{
			Title: target.SourceMetadata.Title, CreditNotes: target.SourceNotes,
		},
		ViewerMemberID: viewerMemberID,
	}
	if exists {
		state.RequestedMetadata = &AIDocumentLocaleMetadata{
			Title: target.TargetMetadata.Title, CreditNotes: target.TargetNotes,
		}
		if locale != target.SourceLocale {
			revision := target.TargetRevision
			state.TargetRevision = &revision
		}
	}
	return state, nil
}

func releaseAIDocumentLifecycle(status string) (published, valid bool) {
	switch status {
	case managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String():
		return false, true
	case managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String():
		return true, true
	default:
		return false, false
	}
}
