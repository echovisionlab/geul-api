package post

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *PostService) preparePostCreate(
	ctx context.Context,
	request *managev1.CreatePostRequest,
) (*model.Post, string, error) {
	title := strings.TrimSpace(request.Title)
	if title == "" {
		return nil, "", errs.Required("title")
	}
	normalizedSlug, _ := normalizeOptionalNullableString(request.Slug)
	if err := s.validatePostCreateSlug(ctx, normalizedSlug); err != nil {
		return nil, "", err
	}
	if err := validateReferenceIDsExist(ctx, s.db, "category", request.CategoryIds); err != nil {
		return nil, "", err
	}
	if err := validateReferenceIDsExist(ctx, s.db, "tag", request.TagIds); err != nil {
		return nil, "", err
	}

	post := &model.Post{
		Slug:            normalizedSlug,
		DocumentLayout:  model.DefaultDocumentLayout(),
		CommentsEnabled: request.CommentsEnabled,
		Status:          model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()),
		CreatedAt:       time.Now(),
	}
	if request.MapPlaceId == nil {
		return post, title, nil
	}
	mapPlaceID, err := s.normalizeMapPlaceID(ctx, *request.MapPlaceId)
	if err != nil {
		return nil, "", err
	}
	post.MapPlaceID = mapPlaceID
	return post, title, nil
}

func (s *PostService) validatePostCreateSlug(ctx context.Context, slug *string) error {
	if slug == nil {
		return nil
	}
	if err := validateSlugWithoutSlash(*slug); err != nil {
		return err
	}
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "post", "posts", *slug)
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
		return errs.Internal(err)
	}
	if int(count) != len(ids) {
		return errs.InvalidArgumentMsg("one or more " + table + " IDs do not exist")
	}
	return nil
}

func (s *PostService) persistNewPost(
	ctx context.Context,
	post *model.Post,
	request *managev1.CreatePostRequest,
	title string,
	memberID string,
	identityID auth.IdentityID,
	acceptLanguage string,
) (auth.ZedToken, error) {
	if s.contentBlocks == nil {
		return auth.ZedToken{}, errs.InternalMsg("Post content Block store is not configured")
	}
	return authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := requireLockedPostCreator(ctx, tx, s.spiceDB); err != nil {
			return err
		}
		if err := ensureNewPostRouteAvailable(ctx, tx, post.Slug); err != nil {
			return err
		}
		sourceLocale := resolveInitialSourceLocale(ctx, tx, s.kratosClient, acceptLanguage)
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
		if err := insertPostReferences(tx, "post_category", "category_id", post.ID, request.CategoryIds); err != nil {
			return err
		}
		if err := insertPostReferences(tx, "post_tag", "tag_id", post.ID, request.TagIds); err != nil {
			return err
		}
		_, err = s.initializePostContentDocument(
			ctx,
			tx,
			post.ID,
			sourceLocale,
			document,
			request.Document,
		)
		if err != nil {
			return err
		}
		if err := s.persistInitialPostDocument(
			ctx,
			tx,
			post,
			request,
			title,
			sourceLocale,
		); err != nil {
			return err
		}
		if s.auditWriter != nil {
			if err := domainaudit.AppendRequest(
				ctx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditPostCreated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewPostCreatedAuditRecord(metadata, post.ID)
				},
			); err != nil {
				return err
			}
		}
		creator, err := policyv1.NewAccountIdentityActor(identityID.String())
		if err != nil {
			return err
		}
		policyTouch, err := policyv1.Post.TouchPolicy(post.ID)
		if err != nil {
			return err
		}
		authorTouch, err := policyv1.Post.TouchAuthor(post.ID, creator)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.Post.DeletePolicy(post.ID)
		if err != nil {
			return err
		}
		authorDelete, err := policyv1.Post.DeleteAuthor(post.ID, creator)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{policyTouch, authorTouch},
			[]policyv1.RelationshipMutation{policyDelete, authorDelete},
		)
	})
}

func (s *PostService) initializePostContentDocument(
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
		return contentblock.Result{}, errs.InvalidArgument("document.profile", "must be post")
	}
	if document.GetSourceLocale() != sourceLocale {
		return contentblock.Result{}, errs.InvalidArgument(
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

func ensureNewPostRouteAvailable(ctx context.Context, tx *gorm.DB, slug *string) error {
	if slug == nil {
		return nil
	}
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "post", "posts", *slug)
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

func (s *PostService) persistInitialPostDocument(
	ctx context.Context,
	tx *gorm.DB,
	post *model.Post,
	request *managev1.CreatePostRequest,
	title string,
	sourceLocale string,
) error {
	now := time.Now().UTC()
	if err := savePostSourceLocaleDocumentState(ctx, tx, post.ID, sourceLocale, translationLocaleDocumentSaveInput{
		Title:   &title,
		Summary: request.Summary,
		Now:     now,
	}); err != nil {
		return err
	}
	_, err := s.ogRefresher.RequestCurrentWithDB(
		ctx,
		tx,
		managev1.OgEntityType_OG_ENTITY_TYPE_POST,
		post.ID,
		sourceLocale,
		false,
		"post_created",
	)
	return err
}

func mapCreatePostError(err error, requestedSlug *string) error {
	if strings.Contains(err.Error(), "duplicate key") {
		return errs.SlugAlreadyExists("post", ptrStringValue(requestedSlug))
	}
	if connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	return errs.Internal(err)
}

func (s *PostService) loadCreatedPost(ctx context.Context, postID string) (*model.Post, error) {
	var post model.Post
	if err := s.db.WithContext(ctx).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		First(&post, "id = ?", postID).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayPostSourceLocaleDocument(ctx, &post); err != nil {
		return nil, err
	}
	return &post, nil
}
