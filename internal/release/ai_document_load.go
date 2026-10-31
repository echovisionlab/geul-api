package release

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
)

// AIDocumentStateQuery names the Release locale an AI document reads.
type AIDocumentStateQuery struct {
	ReleaseID string
	Locale    string
}

// LoadAIDocumentState reads the Release locale an AI document edits, for an
// onboarded Member that can view the Release.
func (s *Service) LoadAIDocumentState(ctx context.Context, query AIDocumentStateQuery) (AIDocumentState, error) {
	id, err := uuidFromCanonicalString(query.ReleaseID)
	if err != nil {
		return AIDocumentState{}, domainerr.InvalidField("release_id", "must be a canonical UUID")
	}
	locale, err := normalizeReleaseDocumentLocale(query.Locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.IdentityID == "" || principal.MemberID == "" {
		return AIDocumentState{}, authenticationRequired()
	}
	var state AIDocumentState
	err = mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		root, err := loadReleaseAIDocumentRoot(ctx, tx, id.String(), "KEY SHARE")
		if err != nil {
			return err
		}
		if err := requireReleaseAction(ctx, s.spiceDB, id.String(), releaseActionView); err != nil {
			return err
		}
		state, err = s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, locale, principal.MemberID.String(),
		)
		return err
	})
	return state, err
}

type releaseAIDocumentRoot struct {
	ID           string              `gorm:"column:id"`
	Status       model.ReleaseStatus `gorm:"column:status"`
	SourceLocale string              `gorm:"column:source_locale"`
	DocumentID   *uuid.UUID          `gorm:"column:content_document_id"`
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
		return releaseAIDocumentRoot{}, missingAs(err, releaseNotFound(releaseID))
	}
	if root.DocumentID == nil || *root.DocumentID == uuid.Nil {
		return releaseAIDocumentRoot{}, domainerr.Precondition("Release content document is not initialized")
	}
	if !knownReleaseLifecycle(root.Status) {
		return releaseAIDocumentRoot{}, errors.New("Release has an unsupported lifecycle status")
	}
	return root, nil
}

func (s *Service) loadAIDocumentStateAfterAuthorization(
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
		return AIDocumentState{}, errors.New("Release AI document requires the compact content profile")
	}
	exists := target.TargetMetadata != nil
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(target.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizeReleaseContentBlockError(err)
	}
	if !exists && targetlocale.SnapshotHasLocale(target.Snapshot, locale) {
		return AIDocumentState{}, errors.New("Release locale overlay exists without translation metadata")
	}
	creditIDs, err := loadReleaseCreditIDs(ctx, tx, root.ID)
	if err != nil {
		return AIDocumentState{}, err
	}
	state := AIDocumentState{
		ReleaseID: root.ID, Status: string(root.Status), DocumentID: *root.DocumentID,
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

// knownReleaseLifecycle reports whether status is a lifecycle state an AI
// document may read and edit; any other stored value is corrupt.
func knownReleaseLifecycle(status model.ReleaseStatus) bool {
	return status == model.ReleaseStatusDraft || status == model.ReleaseStatusPublished
}
