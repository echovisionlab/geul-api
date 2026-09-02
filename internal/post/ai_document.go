package post

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// AIDocumentLocaleMetadata preserves missing and explicit-empty locale values.
// A nil Metadata on AIDocumentState means the target translation resource does
// not exist; nil fields inside a present Metadata remain explicit missing.
type AIDocumentLocaleMetadata struct {
	Title   *string
	Summary *string
}

// AIDocumentState is the Post-owned authorized read model consumed by the
// schema adapter. Editor JSON and transport messages do not cross this domain
// boundary.
type AIDocumentState struct {
	PostID            string
	ContentDocumentID string
	DocumentRevision  string
	TargetRevision    *string
	SourceLocale      string
	RequestedLocale   string
	LocaleExists      bool
	SourceMetadata    AIDocumentLocaleMetadata
	RequestedMetadata *AIDocumentLocaleMetadata
	CategoryIDs       []string
	TagIDs            []string
	LocalizedDocument *contentv1.LocalizedRichTextDocument
	ViewerMemberID    string
}

type aiDocumentPostRoot struct {
	ID                string           `gorm:"column:id"`
	Status            model.PostStatus `gorm:"column:status"`
	ContentDocumentID *string          `gorm:"column:content_document_id"`
}

type aiDocumentMetadataRow struct {
	Title   *string `gorm:"column:title"`
	Summary *string `gorm:"column:summary"`
}

// LoadAIDocumentState applies current Member/Identity authority and one
// database snapshot before projecting the exact requested locale. Archived
// Posts remain readable to their Author and Site Admin, but not to a former
// collaborator-only editor.
func (s *PostService) LoadAIDocumentState(
	ctx context.Context,
	postID string,
	locale string,
) (AIDocumentState, error) {
	if s == nil || s.db == nil || s.spiceDB == nil || s.contentBlocks == nil {
		return AIDocumentState{}, errs.DependencyUnavailable("Post AI document")
	}
	var err error
	locale, err = validatePostAIDocumentIdentity(postID, locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.MemberID == "" {
		return AIDocumentState{}, errs.AuthenticationRequired()
	}

	var state AIDocumentState
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := loadPostAIDocumentRoot(ctx, tx, postID, "KEY SHARE")
		if err != nil {
			return err
		}
		if _, err := requirePostViewForStatus(ctx, s.spiceDB, postID, root.Status); err != nil {
			return err
		}
		state, err = s.loadAIDocumentStateAfterAuthorization(
			ctx,
			tx,
			root,
			locale,
			principal.MemberID.String(),
		)
		return err
	})
	if err != nil {
		return AIDocumentState{}, err
	}
	return state, nil
}

func validatePostAIDocumentIdentity(postID, locale string) (string, error) {
	if _, err := uuidutil.ParseCanonical(postID, "post_id"); err != nil {
		return "", errs.InvalidArgument("post_id", "must be a canonical UUID")
	}
	normalizedLocale := localization.NormalizeExactSupportedLocale(locale)
	if normalizedLocale == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *normalizedLocale, nil
}

func loadPostAIDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	lock string,
) (aiDocumentPostRoot, error) {
	query := tx.WithContext(ctx).
		Table("post").
		Select("id::text AS id", "status", "content_document_id::text AS content_document_id").
		Where("id = ?::uuid", postID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	var root aiDocumentPostRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return aiDocumentPostRoot{}, errs.NotFound("post", postID)
		}
		return aiDocumentPostRoot{}, errs.Internal(fmt.Errorf("load Post AI document root: %w", err))
	}
	if root.ContentDocumentID == nil || strings.TrimSpace(*root.ContentDocumentID) == "" {
		return aiDocumentPostRoot{}, errs.FailedPrecondition("Post content document has not been populated")
	}
	return root, nil
}

func (s *PostService) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root aiDocumentPostRoot,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	documentID, err := uuidutil.ParseCanonical(*root.ContentDocumentID, "content_document_id")
	if err != nil {
		return AIDocumentState{}, errs.FailedPrecondition("Post content document identity is invalid")
	}
	domain, err := loadPostContentDomainContext(ctx, tx, root.ID)
	if err != nil {
		return AIDocumentState{}, err
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return AIDocumentState{}, normalizePostContentBlockError(err)
	}
	if snapshot.Document.Profile != postContentDocumentProfile {
		return AIDocumentState{}, errs.FailedPrecondition("Post AI document requires the Post content profile")
	}

	state := AIDocumentState{
		PostID:            root.ID,
		ContentDocumentID: documentID.String(),
		DocumentRevision:  snapshot.Document.Revision.String(),
		SourceLocale:      domain.SourceLocale,
		RequestedLocale:   locale,
		ViewerMemberID:    viewerMemberID,
	}
	sourceMetadata, found, err := loadAIDocumentMetadata(ctx, tx, root.ID, domain.SourceLocale)
	if err != nil {
		return AIDocumentState{}, err
	}
	if !found {
		return AIDocumentState{}, errs.FailedPrecondition("Post source locale metadata is missing")
	}
	state.SourceMetadata = sourceMetadata
	if locale == domain.SourceLocale {
		state.LocaleExists = true
		copy := sourceMetadata
		state.RequestedMetadata = &copy
	} else {
		requested, exists, err := loadOptionalPostLocaleMetadataRow(ctx, tx, root.ID, locale, false)
		if err != nil {
			return AIDocumentState{}, err
		}
		if !exists && snapshotContainsLocale(snapshot, locale) {
			return AIDocumentState{}, errs.FailedPrecondition("Post locale Blocks exist without owning translation metadata")
		}
		state.LocaleExists = exists
		if exists {
			state.RequestedMetadata = &AIDocumentLocaleMetadata{Title: requested.Title, Summary: requested.Summary}
			targetRevision, revisionErr := derivePostTargetRevision(snapshot.Document.Revision.String(), requested)
			if revisionErr != nil {
				return AIDocumentState{}, revisionErr
			}
			state.TargetRevision = &targetRevision
		}
	}
	state.CategoryIDs, state.TagIDs, err = loadPostTaxonomyIDs(ctx, tx, root.ID)
	if err != nil {
		return AIDocumentState{}, errs.Internal(fmt.Errorf("load Post AI document taxonomy: %w", err))
	}
	state.LocalizedDocument, err = contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizePostContentBlockError(err)
	}
	return state, nil
}

func loadAIDocumentMetadata(
	ctx context.Context,
	db *gorm.DB,
	postID string,
	locale string,
) (AIDocumentLocaleMetadata, bool, error) {
	var row aiDocumentMetadataRow
	err := db.WithContext(ctx).
		Table("post_translation").
		Select("title", "summary").
		Where("entity_id = ?::uuid AND locale = ?", postID, locale).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AIDocumentLocaleMetadata{}, false, nil
	}
	if err != nil {
		return AIDocumentLocaleMetadata{}, false, errs.Internal(fmt.Errorf("load Post AI document locale metadata: %w", err))
	}
	return AIDocumentLocaleMetadata(row), true, nil
}

func snapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale {
			return true
		}
	}
	return false
}
