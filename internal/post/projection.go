package post

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListMyPosts returns the Posts the caller may edit.
func (s *Service) ListMyPosts(ctx context.Context, query ListPostsQuery) (*managev1.ListMyPostsResponse, error) {
	stored, err := s.authorizedPostListQuery(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.listPostPage(ctx, stored, query, false, "Categories", "Tags", "Series")
	if err != nil {
		return nil, err
	}
	if err := s.hydratePostContentProjections(ctx, page.posts); err != nil {
		return nil, err
	}
	if err := s.loadPostListProjections(ctx, &page); err != nil {
		return nil, err
	}
	protoPosts := make([]*managev1.Post, len(page.posts))
	for i := range page.posts {
		if protoPosts[i], err = s.projectListedPost(page, i); err != nil {
			return nil, err
		}
	}
	return &managev1.ListMyPostsResponse{Posts: protoPosts, Pagination: page.pagination()}, nil
}

// Helper methods

func (s *Service) overlayPostSourceLocaleDocument(
	ctx context.Context,
	post *model.Post,
) error {
	state, err := loadRequiredPostSourceLocaleDocumentState(ctx, s.db, post.ID)
	if err != nil {
		return err
	}
	overlayPostSourceLocaleDocument(post, state)
	return nil
}

func (s *Service) overlayPostSourceLocaleDocuments(
	ctx context.Context,
	posts []model.Post,
) error {
	sourceStates, err := loadPostSourceLocaleDocumentStates(ctx, s.db, collectManagePostIDs(posts))
	if err != nil {
		return err
	}
	for i := range posts {
		state := sourceStates[posts[i].ID]
		if state == nil {
			return domainerr.ResourceNotFound("post_translation", posts[i].ID)
		}
		overlayPostSourceLocaleDocument(&posts[i], state)
	}
	return nil
}

func (s *Service) loadReadyPostOgAssets(
	ctx context.Context,
	posts []model.Post,
) (map[string]*commonv1.AssetRef, error) {
	candidates := make([]*string, 0, len(posts)*2)
	for i := range posts {
		candidates = append(
			candidates,
			posts[i].SourceLocaleOgAssetID,
			posts[i].OgAssetID,
		)
	}
	return loadReadyManageOgAssetRefs(ctx, s.db, s.cdnDomain, candidates...)
}

func (s *Service) postResponseWithReadyOg(
	ctx context.Context,
	post *model.Post,
) (*managev1.Post, error) {
	if err := s.hydratePostContentProjection(ctx, post); err != nil {
		return nil, err
	}
	ogAsset, err := readyManageOgAssetRef(
		ctx,
		s.db,
		s.cdnDomain,
		post.SourceLocaleOgAssetID,
		post.OgAssetID,
	)
	if err != nil {
		return nil, err
	}
	authors, err := s.loadPostAuthorMembers(ctx, []string{post.ID})
	if err != nil {
		return nil, err
	}
	featuredDelivery, err := s.resolveAuthorizedPostFeaturedImage(ctx, post)
	if err != nil {
		return nil, err
	}
	authorities, err := s.loadCurrentPostAuthorities(ctx, []string{post.ID})
	if err != nil {
		return nil, err
	}
	protoPost, err := s.toProtoPostWithProjection(
		post,
		ogAsset,
		authors[post.ID],
		featuredDelivery,
		authorities[post.ID],
	)
	if err != nil {
		return nil, err
	}
	return protoPost, nil
}

func (s *Service) hydratePostContentProjection(ctx context.Context, post *model.Post) error {
	if strings.TrimSpace(post.SourceLocale) == "" {
		return domainerr.Precondition("Post source locale is not initialized")
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, post.ID)
	if err != nil {
		return err
	}
	return mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		root, err := lockPostContentDocumentRoot(ctx, tx, post.ID, documentID, true)
		if err != nil {
			return err
		}
		if _, err := requirePostViewForStatus(ctx, s.spiceDB, post.ID, root.Status); err != nil {
			return err
		}
		if root.SourceLocale != post.SourceLocale {
			return domainerr.Precondition("Post source locale changed; reload before reading")
		}
		snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, post.SourceLocale)
		if err != nil {
			return normalizePostContentBlockError(err)
		}
		document, err := contentblock.SnapshotToRichTextDocument(snapshot)
		if err != nil {
			return normalizePostContentBlockError(err)
		}
		mediaItems, err := loadPostBlockMedia(ctx, tx, post.ID, documentID, auth.GetUser(ctx), s.mediaLoader, s.mediaHydrator)
		if err != nil {
			return err
		}
		post.ContentDocument = document
		post.ContentRevision = snapshot.Document.Revision.String()
		post.BlockMedia = mediaItems
		return nil
	})
}

func (s *Service) hydratePostContentProjections(ctx context.Context, posts []model.Post) error {
	for i := range posts {
		if err := s.hydratePostContentProjection(ctx, &posts[i]); err != nil {
			return err
		}
	}
	return nil
}

// LoadLocalizedPostContentProjectionForPublic materializes a selected locale
// only after the public caller has completed Post view authorization.
func LoadLocalizedPostContentProjectionForPublic(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	postID string,
	locale string,
) (*contentv1.LocalizedRichTextDocument, string, error) {
	if db == nil || store == nil {
		return nil, "", errors.New("Post content projection dependencies are required")
	}
	var sourceState struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).
		Table("post").Select("source_locale").Where("id = ?::uuid", postID).
		Take(&sourceState).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", domainerr.ResourceNotFound("post", postID)
		}
		return nil, "", err
	}
	if strings.TrimSpace(sourceState.SourceLocale) == "" {
		return nil, "", domainerr.Precondition("Post source locale is not initialized")
	}
	if strings.TrimSpace(locale) == "" {
		locale = sourceState.SourceLocale
	}
	documentID, err := loadPostContentDocumentID(ctx, db, postID)
	if err != nil {
		return nil, "", err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, sourceState.SourceLocale)
	if err != nil {
		return nil, "", normalizePostContentBlockError(err)
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return nil, "", normalizePostContentBlockError(err)
	}
	return document, snapshot.Document.Revision.String(), nil
}

func (s *Service) toProtoPostWithProjection(
	p *model.Post,
	ogAsset *commonv1.AssetRef,
	authors []*commonv1.MemberSummary,
	featuredImage *commonv1.MediaDelivery,
	authority PostAuthority,
) (*managev1.Post, error) {
	post, err := s.toProtoPost(p, ogAsset)
	if err != nil {
		return nil, err
	}
	post.AuthorMembers = authors
	post.FeaturedImageDelivery = featuredImage
	post.AllowedActions = postAllowedActions(p.ID, p.Status, authority)
	return post, nil
}

func (s *Service) loadPostAuthorMembers(ctx context.Context, postIDs []string) (map[string][]*commonv1.MemberSummary, error) {
	result := make(map[string][]*commonv1.MemberSummary, len(postIDs))
	if len(postIDs) == 0 {
		return result, nil
	}
	type row struct {
		PostID   string `gorm:"column:post_id"`
		MemberID string `gorm:"column:member_id"`
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Table("post_author").
		Select("post_id, member_id").
		Where("post_id IN ?", postIDs).
		Order("post_id ASC, created_at ASC, member_id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load post authors: %w", err)
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	summaries, err := s.members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if summary := summaries[row.MemberID]; summary != nil {
			result[row.PostID] = append(result[row.PostID], summary)
		}
	}
	return result, nil
}

func (s *Service) loadCurrentPostAuthorities(ctx context.Context, postIDs []string) (map[string]PostAuthority, error) {
	result := make(map[string]PostAuthority, len(postIDs))
	principal := auth.GetUser(ctx)
	if principal == nil || len(postIDs) == 0 {
		return result, nil
	}
	subject, err := auth.NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	wanted := make(map[string]struct{}, len(postIDs))
	for _, id := range postIDs {
		wanted[id] = struct{}{}
	}
	type authorityLookup struct {
		lookup policyv1.ResourceLookup
		action postAction
	}
	lookups := []authorityLookup{
		{policyv1.Post.LookupView(), policyv1.Post.View},
		{policyv1.Post.LookupEdit(), policyv1.Post.Edit},
		{policyv1.Post.LookupDelete(), policyv1.Post.Delete},
		{policyv1.Post.LookupPublish(), policyv1.Post.Publish},
		{policyv1.Post.LookupManage(), policyv1.Post.Manage},
		{policyv1.Post.LookupManageParticipants(), policyv1.Post.ManageParticipants},
		{policyv1.Post.LookupManageShareLinks(), policyv1.Post.ManageShareLinks},
		{policyv1.Post.LookupPlatformAdmin(), policyv1.Post.RemoveAuthor},
		{policyv1.Post.LookupViewArchived(), policyv1.Post.ViewArchived},
		{policyv1.Post.LookupEditArchived(), policyv1.Post.EditArchived},
	}
	for _, item := range lookups {
		ids, err := s.spiceDB.LookupResources(ctx, item.lookup, actor)
		if err != nil {
			return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		for _, id := range ids {
			if _, ok := wanted[id]; !ok {
				continue
			}
			authority := result[id]
			can, err := item.action(id)
			if err != nil {
				return nil, err
			}
			authority.add(can)
			result[id] = authority
		}
	}
	return result, nil
}

func lookupPostResources(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	actor policyv1.Actor,
	lookups ...policyv1.ResourceLookup,
) ([]string, error) {
	seen := make(map[string]struct{})
	for _, lookup := range lookups {
		ids, err := spiceDB.LookupResources(ctx, lookup, actor)
		if err != nil {
			return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	return result, nil
}

func (s *Service) loadPostCommentCounts(ctx context.Context, postIDs []string) (map[string]int32, error) {
	result := make(map[string]int32, len(postIDs))
	if len(postIDs) == 0 {
		return result, nil
	}
	type row struct {
		PostID string `gorm:"column:post_id"`
		Count  int64  `gorm:"column:comment_count"`
	}
	var rows []row
	if err := s.db.WithContext(ctx).Table("comment").
		Select("post_id, COUNT(*) AS comment_count").
		Where("post_id IN ?", postIDs).
		Group("post_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.PostID] = int32(row.Count)
	}
	return result, nil
}

// postSortConfig defines allowed sort fields for posts
var postSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
		"title":        PostSourceTitleSQL,
		"status":       "status",
	},
	DefaultSort: "published_at DESC NULLS LAST, created_at DESC",
}

// toProtoPost converts a model.Post to protobuf Post (Domain Object -> DTO)
// Note: Author is not populated here - use toProtoPostWithAuthor for that
func (s *Service) toProtoPost(p *model.Post, ogAsset *commonv1.AssetRef) (*managev1.Post, error) {
	documentLayout, err := p.DocumentLayout.Proto()
	if err != nil {
		return nil, err
	}
	post := &managev1.Post{
		Id:                    p.ID,
		Title:                 p.Title,
		SourceLocale:          p.SourceLocale,
		Document:              p.ContentDocument,
		Revision:              p.ContentRevision,
		ConfigurationRevision: p.ConfigurationRevision,
		BlockMedia:            p.BlockMedia,
		Status:                postStatusToProto(p.Status),
		CommentsEnabled:       p.CommentsEnabled,
		DocumentLayout:        documentLayout,
		CreatedAt:             timestamppb.New(p.CreatedAt),
		OgAsset:               ogAsset,
	}

	if p.Slug != nil {
		post.Slug = p.Slug
	}
	if p.Summary != nil {
		post.Summary = p.Summary
	}
	if p.PublishedAt != nil {
		post.PublishedAt = timestamppb.New(*p.PublishedAt)
	}
	if p.ScheduledAt != nil {
		post.ScheduledAt = timestamppb.New(*p.ScheduledAt)
	}
	if p.ScheduledTimeZone != nil {
		post.ScheduledTimeZone = p.ScheduledTimeZone
	}
	if !p.UpdatedAt.IsZero() {
		post.UpdatedAt = timestamppb.New(p.UpdatedAt)
	}
	if p.SeriesOrder != nil {
		post.SeriesOrder = p.SeriesOrder
	}
	if p.MapPlaceID != nil {
		post.MapPlaceId = p.MapPlaceID
	}
	// Convert categories
	if len(p.Categories) > 0 {
		post.Categories = make([]*managev1.Category, len(p.Categories))
		for i, cat := range p.Categories {
			post.Categories[i] = &managev1.Category{
				Id:        cat.ID,
				Name:      cat.Name,
				CreatedAt: timestamppb.New(cat.CreatedAt),
				Slug:      &cat.Slug,
			}
			if cat.Description != nil {
				post.Categories[i].Description = cat.Description
			}
		}
	}

	// Convert tags
	if len(p.Tags) > 0 {
		post.Tags = make([]*managev1.Tag, len(p.Tags))
		for i, tag := range p.Tags {
			post.Tags[i] = &managev1.Tag{
				Id:        tag.ID,
				Name:      tag.Name,
				CreatedAt: timestamppb.New(tag.CreatedAt),
				Slug:      &tag.Slug,
			}
		}
	}

	// Convert series
	if p.Series != nil {
		post.Series = &managev1.Series{
			Id:           p.Series.ID,
			SourceLocale: p.Series.SourceLocale,
			Title:        p.Series.Title,
			Status:       string(p.Series.Status),
			CreatedAt:    timestamppb.New(p.Series.CreatedAt),
			Slug:         p.Series.Slug,
		}
		if p.Series.Description != nil {
			post.Series.Description = p.Series.Description
		}
	}

	return post, nil
}

func (s *Service) normalizeMapPlaceID(ctx context.Context, raw string) (*string, error) {
	return normalizeMapPlaceIDWithDB(ctx, s.db, raw)
}

func normalizeMapPlaceIDWithDB(ctx context.Context, db *gorm.DB, raw string) (*string, error) {
	mapPlaceID := strings.TrimSpace(raw)
	if mapPlaceID == "" {
		return nil, nil
	}

	var count int64
	if err := db.WithContext(ctx).
		Table("map_place").
		Where("id = ?", mapPlaceID).
		Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, domainerr.InvalidField("map_place_id", "map place does not exist")
	}

	return &mapPlaceID, nil
}

// ============================================================================
// Version History
// ============================================================================

// ListPostVersions returns a paginated list of version snapshots for a post.
