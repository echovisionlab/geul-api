package artist

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LoadAIDocumentState preserves published visibility while keeping non-public
// reads behind Artist's current SpiceDB authority fence.
func (s *Service) LoadAIDocumentState(ctx context.Context, artistID, locale string) (AIDocumentState, error) {
	id, err := canonicalArtistUUID(artistID)
	if err != nil {
		return AIDocumentState{}, domainerr.InvalidField("artist_id", "must be a canonical UUID")
	}
	locale, err = normalizeArtistDocumentLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.IdentityID == "" || principal.MemberID == "" {
		return AIDocumentState{}, errAuthenticationRequired
	}

	var state AIDocumentState
	err = mutation.LockingRead(ctx, s.db, func(tx *gorm.DB) error {
		root, err := loadArtistAIDocumentRoot(ctx, tx, id.String(), "KEY SHARE")
		if err != nil {
			return err
		}
		published, valid := artistAIDocumentLifecycle(root.Status)
		if !valid {
			return errors.New("Artist has an unsupported lifecycle status")
		}
		if !published {
			if err := artistEntity.RequirePermission(ctx, s.spiceDB, id.String(), policyv1.Artist.View); err != nil {
				return err
			}
		}
		state, err = s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, locale, principal.MemberID.String(),
		)
		return err
	})
	return state, err
}

type artistAIDocumentRoot struct {
	ID         string     `gorm:"column:id"`
	Status     string     `gorm:"column:status"`
	DocumentID *uuid.UUID `gorm:"column:content_document_id"`
}

func loadArtistAIDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	artistID string,
	lock string,
) (artistAIDocumentRoot, error) {
	query := tx.WithContext(ctx).
		Table("artist").
		Select("id::text AS id", "status", "content_document_id").
		Where("id = ?::uuid", artistID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	var root artistAIDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return artistAIDocumentRoot{}, domainerr.ResourceNotFound("artist", artistID)
		}
		return artistAIDocumentRoot{}, err
	}
	if root.DocumentID == nil || *root.DocumentID == uuid.Nil {
		return artistAIDocumentRoot{}, domainerr.Precondition("Artist content document is not initialized")
	}
	return root, nil
}

func (s *Service) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root artistAIDocumentRoot,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	locale, err := normalizeArtistDocumentLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	localeState, err := loadArtistTargetLocaleState(
		ctx, tx, s.contentBlocks, root.ID, *root.DocumentID, locale, false,
	)
	if err != nil {
		return AIDocumentState{}, err
	}
	document, err := artistSparseLocalizedDocument(localeState, locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	var targetRevision *string
	if locale != localeState.SourceLocale && localeState.TargetMetadata != nil {
		targetRevision = &localeState.TargetRevision
	}
	return AIDocumentState{
		ArtistID: root.ID, Status: root.Status, DocumentID: *root.DocumentID,
		Revision: localeState.Snapshot.Document.Revision.String(), TargetRevision: targetRevision,
		SourceLocale: localeState.SourceLocale,
		Locale:       locale, LocaleExists: localeState.TargetMetadata != nil, Document: document,
		ViewerMemberID: viewerMemberID,
	}, nil
}

func artistAIDocumentLifecycle(status string) (published, valid bool) {
	switch status {
	case managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String():
		return false, true
	case managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String():
		return true, true
	default:
		return false, false
	}
}
