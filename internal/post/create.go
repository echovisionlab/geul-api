package post

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// preparePostCreate validates command as written; the checks against stored
// state run in the creating transaction.
func preparePostCreate(command CreatePostCommand) (*Post, string, error) {
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
	return &Post{
		Slug:            normalizedSlug,
		DocumentLayout:  DefaultDocumentLayout(),
		CommentsEnabled: command.CommentsEnabled,
		Status:          StatusDraft,
		CreatedAt:       time.Now(),
	}, title, nil
}

func validateReferenceIDsExist(
	ctx context.Context,
	store Store,
	taxonomy Taxonomy,
	ids []string,
) error {
	if len(ids) == 0 {
		return nil
	}
	count, err := store.CountTaxonomy(ctx, taxonomy, ids)
	if err != nil {
		return err
	}
	if int(count) != len(ids) {
		return domainerr.New(domainerr.Invalid, "", "one or more "+string(taxonomy)+" IDs do not exist")
	}
	return nil
}

// checkNewPostReferences checks the slug route, Categories, Tags and Map
// Place of a new Post in its creating transaction.
func checkNewPostReferences(ctx context.Context, store Store, created *Post, command CreatePostCommand) error {
	if created.Slug != nil {
		if err := store.LockRoute(ctx, *created.Slug); err != nil {
			return err
		}
	}
	if err := validateReferenceIDsExist(ctx, store, Categories, command.CategoryIDs); err != nil {
		return err
	}
	if err := validateReferenceIDsExist(ctx, store, Tags, command.TagIDs); err != nil {
		return err
	}
	if command.MapPlaceID == nil {
		return nil
	}
	mapPlaceID, err := normalizeMapPlaceIDWithStore(ctx, store, *command.MapPlaceID)
	created.MapPlaceID = mapPlaceID
	return err
}

func persistNewPost(
	ctx context.Context,
	store Store,
	effects *postEffects,
	created *Post,
	command CreatePostCommand,
	title string,
	memberID string,
) error {
	if err := checkNewPostReferences(ctx, store, created, command); err != nil {
		return err
	}
	sourceLocale, err := resolveCreateSourceLocale(ctx, store, command.SourceLocale, command.AcceptLanguage)
	if err != nil {
		return err
	}
	created.SourceLocale = sourceLocale
	document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
		Profile:      postContentDocumentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return normalizePostContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	created.ContentDocumentID = &contentDocumentID
	if err := store.InsertPost(ctx, created); err != nil {
		return err
	}
	if err := store.InsertCreatorAuthor(ctx, created.ID, memberID); err != nil {
		return err
	}
	if err := store.InsertTaxonomyLinks(ctx, Categories, created.ID, command.CategoryIDs); err != nil {
		return err
	}
	if err := store.InsertTaxonomyLinks(ctx, Tags, created.ID, command.TagIDs); err != nil {
		return err
	}
	if _, err := initializePostContentDocument(ctx, store, created.ID, sourceLocale, document, command.Document); err != nil {
		return err
	}
	if err := savePostSourceLocaleDocumentState(ctx, store, created.ID, sourceLocale, &title, command.Summary, time.Now().UTC()); err != nil {
		return err
	}
	effects.requestOG(created.ID, sourceLocale, false, "post_created")
	postID := created.ID
	effects.audit(sharedtelemetry.AuditPostCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostCreatedAuditRecord(metadata, postID)
	})
	return nil
}

// savePostSourceLocaleDocumentState locks the Post root, as every
// transaction that may touch both the post and post_translation rows does
// first, and stores the source locale's title and summary.
func savePostSourceLocaleDocumentState(ctx context.Context, store Store, postID, locale string, title, summary *string, now time.Time) error {
	if err := lockPostRootForLocaleWrite(ctx, store, postID); err != nil {
		return err
	}
	return store.UpsertSourceLocale(ctx, postID, locale, title, summary, now)
}

func lockPostRootForLocaleWrite(ctx context.Context, store Store, postID string) error {
	err := store.LockPostRoot(ctx, postID)
	if errors.Is(err, ErrPostNotFound) {
		return domainerr.ResourceNotFound("post", postID)
	}
	return err
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

func initializePostContentDocument(
	ctx context.Context,
	store Store,
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
	result, err := store.ReplaceContentSnapshot(
		ctx,
		replace,
		postCreationDocumentFence(postID, sourceLocale),
	)
	if err != nil {
		return contentblock.Result{}, normalizePostContentBlockError(err)
	}
	return result, nil
}

func (s *Service) loadCreatedPost(ctx context.Context, postID string) (Post, error) {
	created, err := readStore(ctx, s, func(store Store) (Post, error) { return store.FindPost(ctx, postID) })
	if err != nil {
		return Post{}, err
	}
	if err := s.overlayPostSourceLocaleDocument(ctx, &created); err != nil {
		return Post{}, err
	}
	return created, nil
}
