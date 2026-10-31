package post

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// preparePostCreate validates command as written; the checks against stored
// state run in the creating transaction.
func (s *Service) preparePostCreate(command CreatePostCommand) (*model.Post, string, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return nil, "", domainerr.Required("title")
	}
	normalizedSlug, _ := normalizeOptionalNullableString(command.Slug)
	if normalizedSlug != nil {
		if err := pathslug.Validate(*normalizedSlug); err != nil {
			return nil, "", err
		}
	}
	return &model.Post{
		Slug:            normalizedSlug,
		DocumentLayout:  model.DefaultDocumentLayout(),
		CommentsEnabled: command.CommentsEnabled,
		Status:          model.PostStatusDraft,
		CreatedAt:       time.Now(),
	}, title, nil
}

func validateReferenceIDsExist(
	ctx context.Context,
	db *gorm.DB,
	table string,
	ids []string,
) error {
	if len(ids) == 0 {
		return nil
	}
	var count int64
	if err := db.WithContext(ctx).Table(table).Where("id IN ?", ids).Count(&count).Error; err != nil {
		return err
	}
	if int(count) != len(ids) {
		return domainerr.New(domainerr.Invalid, "", "one or more "+table+" IDs do not exist")
	}
	return nil
}

// checkNewPostReferences checks the slug route, Categories, Tags and Map
// Place of a new Post in its creating transaction.
func (s *Service) checkNewPostReferences(ctx context.Context, tx *gorm.DB, post *model.Post, command CreatePostCommand) error {
	if post.Slug != nil {
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "post", "posts", *post.Slug); err != nil {
			return err
		}
	}
	if err := validateReferenceIDsExist(ctx, tx, "category", command.CategoryIDs); err != nil {
		return err
	}
	if err := validateReferenceIDsExist(ctx, tx, "tag", command.TagIDs); err != nil {
		return err
	}
	if command.MapPlaceID == nil {
		return nil
	}
	mapPlaceID, err := normalizeMapPlaceIDWithDB(ctx, tx, *command.MapPlaceID)
	post.MapPlaceID = mapPlaceID
	return err
}

func (s *Service) persistNewPost(
	ctx context.Context,
	tx *gorm.DB,
	effects *postEffects,
	post *model.Post,
	command CreatePostCommand,
	title string,
	memberID string,
) error {
	if err := s.checkNewPostReferences(ctx, tx, post, command); err != nil {
		return err
	}
	sourceLocale, err := resolveCreateSourceLocale(ctx, tx, command.SourceLocale, command.AcceptLanguage)
	if err != nil {
		return err
	}
	post.SourceLocale = sourceLocale
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile:      postContentDocumentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return normalizePostContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	post.ContentDocumentID = &contentDocumentID
	if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(post).Error; err != nil {
		return err
	}
	if err := insertPostAuthor(tx, post.ID, memberID); err != nil {
		return err
	}
	if err := insertPostReferences(tx, "post_category", "category_id", post.ID, command.CategoryIDs); err != nil {
		return err
	}
	if err := insertPostReferences(tx, "post_tag", "tag_id", post.ID, command.TagIDs); err != nil {
		return err
	}
	if _, err := s.initializePostContentDocument(ctx, tx, post.ID, sourceLocale, document, command.Document); err != nil {
		return err
	}
	if err := savePostSourceLocaleDocumentState(ctx, tx, post.ID, sourceLocale, translationLocaleDocumentSaveInput{
		Title:   &title,
		Summary: command.Summary,
		Now:     time.Now().UTC(),
	}); err != nil {
		return err
	}
	effects.requestOG(post.ID, sourceLocale, false, "post_created")
	postID := post.ID
	effects.audit(sharedtelemetry.AuditPostCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostCreatedAuditRecord(metadata, postID)
	})
	return nil
}

// postCreationRelationships returns the new Post's policy and its creator's
// author relationship, with their exact inverse.
func postCreationRelationships(postID string, creator policyv1.Actor) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	policyTouch, err := policyv1.Post.TouchPolicy(postID)
	if err != nil {
		return nil, nil, err
	}
	authorTouch, err := policyv1.Post.TouchAuthor(postID, creator)
	if err != nil {
		return nil, nil, err
	}
	policyDelete, err := policyv1.Post.DeletePolicy(postID)
	if err != nil {
		return nil, nil, err
	}
	authorDelete, err := policyv1.Post.DeleteAuthor(postID, creator)
	if err != nil {
		return nil, nil, err
	}
	return []policyv1.RelationshipMutation{policyTouch, authorTouch}, []policyv1.RelationshipMutation{policyDelete, authorDelete}, nil
}

func (s *Service) initializePostContentDocument(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	sourceLocale string,
	created contentblock.Snapshot,
	document *contentv1.RichTextDocument,
) (contentblock.Result, error) {
	if document == nil {
		return contentblock.Result{}, nil
	}
	if document.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST {
		return contentblock.Result{}, domainerr.InvalidField("document.profile", "must be post")
	}
	if document.GetSourceLocale() != sourceLocale {
		return contentblock.Result{}, domainerr.InvalidField(
			"document.source_locale",
			"must match the server-selected source locale",
		)
	}
	replace, err := contentblock.ReplaceFromRichTextProto(
		created.Document.ID,
		created.Document.Revision,
		document,
	)
	if err != nil {
		return contentblock.Result{}, normalizePostContentBlockError(err)
	}
	result, err := s.contentBlocks.ReplaceSnapshot(
		ctx,
		tx,
		replace,
		postCreationDocumentFence(postID, sourceLocale),
	)
	if err != nil {
		return contentblock.Result{}, normalizePostContentBlockError(err)
	}
	return result, nil
}

func insertPostAuthor(tx *gorm.DB, postID, memberID string) error {
	return tx.Exec(
		`INSERT INTO post_author (post_id, member_id, created_at) VALUES (?::uuid, ?::uuid, NOW())`,
		postID,
		memberID,
	).Error
}

func insertPostReferences(tx *gorm.DB, table, referenceColumn, postID string, ids []string) error {
	for _, id := range ids {
		query := "INSERT INTO " + table + " (post_id, " + referenceColumn + ", created_at) VALUES (?, ?, NOW())"
		if err := tx.Exec(query, postID, id).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) loadCreatedPost(ctx context.Context, postID string) (*model.Post, error) {
	var post model.Post
	if err := s.db.WithContext(ctx).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		First(&post, "id = ?", postID).Error; err != nil {
		return nil, err
	}
	if err := s.overlayPostSourceLocaleDocument(ctx, &post); err != nil {
		return nil, err
	}
	return &post, nil
}
