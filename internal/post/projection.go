package post

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PostService implements the PostService Connect handler
func (s *PostService) ListMyPosts(
	ctx context.Context,
	req *connect.Request[managev1.ListMyPostsRequest],
) (*connect.Response[managev1.ListMyPostsResponse], error) {
	var posts []model.Post
	var total int64

	query, err := s.authorizedPostListQuery(ctx)
	if err != nil {
		return nil, err
	}

	// 1. Handle join filters directly (category_id, tag_id require subqueries)
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range req.Msg.Filters {
		if f == nil {
			continue
		}
		switch f.GetField() {
		case "category_id":
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				query = query.Where("id IN (SELECT post_id FROM post_category WHERE category_id = ?)", f.GetValue())
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				query = query.Where("id IN (SELECT post_id FROM post_category WHERE category_id IN ?)", f.GetValues())
			}
		case "tag_id":
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				query = query.Where("id IN (SELECT post_id FROM post_tag WHERE tag_id = ?)", f.GetValue())
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				query = query.Where("id IN (SELECT post_id FROM post_tag WHERE tag_id IN ?)", f.GetValues())
			}
		default:
			remainingFilters = append(remainingFilters, f)
		}
	}

	// 2. Apply remaining filters using FilterConfig (status, series_id, published_at)
	query, err = PostAdminFilterConfig.ApplyFilters(query, remainingFilters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	// Apply sorting
	query, err = postSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// Load posts with relations
	if err := query.
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&posts).Error; err != nil {
		return nil, errs.Internal(err)
	}

	if err := s.overlayPostSourceLocaleDocuments(ctx, posts); err != nil {
		return nil, err
	}
	if err := s.hydratePostContentProjections(ctx, posts); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadyPostOgAssets(ctx, posts)
	if err != nil {
		return nil, err
	}
	authorMembers, err := s.loadPostAuthorMembers(ctx, collectManagePostIDs(posts))
	if err != nil {
		return nil, err
	}

	featuredDeliveries, err := s.loadPostPublicFeaturedImageDeliveries(ctx, posts)
	if err != nil {
		return nil, err
	}
	authorities, err := s.loadCurrentPostAuthorities(ctx, collectManagePostIDs(posts))
	if err != nil {
		return nil, err
	}

	protoPosts := make([]*managev1.Post, len(posts))
	for i := range posts {
		protoPosts[i] = s.toProtoPostWithProjection(&posts[i], manageOgAssetFromReadyMap(
			readyOgAssets,
			posts[i].SourceLocaleOgAssetID,
			posts[i].OgAssetID,
		), authorMembers[posts[i].ID], featuredDeliveries[posts[i].ID], authorities[posts[i].ID])
	}

	return connect.NewResponse(&managev1.ListMyPostsResponse{
		Posts: protoPosts,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// Helper methods

func (s *PostService) overlayPostSourceLocaleDocument(
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

func (s *PostService) overlayPostSourceLocaleDocuments(
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
			return errs.NotFound("post_translation", posts[i].ID)
		}
		overlayPostSourceLocaleDocument(&posts[i], state)
	}
	return nil
}

func (s *PostService) loadReadyPostOgAssets(
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

func (s *PostService) postResponseWithReadyOg(
	ctx context.Context,
	post *model.Post,
) (*connect.Response[managev1.Post], error) {
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
	return connect.NewResponse(s.toProtoPostWithProjection(
		post,
		ogAsset,
		authors[post.ID],
		featuredDelivery,
		authorities[post.ID],
	)), nil
}

func (s *PostService) hydratePostContentProjection(ctx context.Context, post *model.Post) error {
	if post == nil {
		return errs.InternalMsg("Post is required")
	}
	if s.contentBlocks == nil {
		return errs.InternalMsg("Post content Block store is not configured")
	}
	if strings.TrimSpace(post.SourceLocale) == "" {
		return errs.FailedPrecondition("Post source locale is not initialized")
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, post.ID)
	if err != nil {
		return err
	}
	snapshot, err := s.contentBlocks.LoadSnapshot(ctx, s.db, documentID, post.SourceLocale)
	if err != nil {
		return normalizePostContentBlockError(err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return normalizePostContentBlockError(err)
	}
	post.ContentDocument = document
	post.ContentRevision = snapshot.Document.Revision.String()
	post.BlockMedia, err = s.mediaLoader.LoadContentBlockMediaReferences(ctx, s.db, documentID)
	if err != nil {
		return errs.Internal(fmt.Errorf("load Post content media references: %w", err))
	}
	return nil
}

func (s *PostService) hydratePostContentProjections(ctx context.Context, posts []model.Post) error {
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
		return nil, "", errs.InternalMsg("Post content projection dependencies are required")
	}
	var sourceState struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).
		Table("post").Select("source_locale").Where("id = ?::uuid", postID).
		Take(&sourceState).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "", errs.NotFound("post", postID)
		}
		return nil, "", errs.Internal(err)
	}
	if strings.TrimSpace(sourceState.SourceLocale) == "" {
		return nil, "", errs.FailedPrecondition("Post source locale is not initialized")
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

func (s *PostService) toProtoPostWithProjection(
	p *model.Post,
	ogAsset *commonv1.AssetRef,
	authors []*commonv1.MemberSummary,
	featuredImage *commonv1.MediaDelivery,
	authority PostAuthority,
) *managev1.Post {
	post := s.toProtoPost(p, ogAsset)
	post.AuthorMembers = authors
	post.FeaturedImageDelivery = featuredImage
	post.AllowedActions = postAllowedActions(p.ID, p.Status, authority)
	return post
}

func (s *PostService) loadPostAuthorMembers(ctx context.Context, postIDs []string) (map[string][]*commonv1.MemberSummary, error) {
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
		return nil, errs.Internal(fmt.Errorf("load post authors: %w", err))
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memberIDs = append(memberIDs, row.MemberID)
	}
	summaries, err := s.members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	for _, row := range rows {
		if summary := summaries[row.MemberID]; summary != nil {
			result[row.PostID] = append(result[row.PostID], summary)
		}
	}
	return result, nil
}

func (s *PostService) loadCurrentPostAuthorities(ctx context.Context, postIDs []string) (map[string]PostAuthority, error) {
	result := make(map[string]PostAuthority, len(postIDs))
	principal := auth.GetUser(ctx)
	if principal == nil || len(postIDs) == 0 {
		return result, nil
	}
	subject, err := auth.NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return nil, errs.AuthenticationRequired()
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, errs.AuthenticationRequired()
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
			return nil, errs.DependencyUnavailable("SpiceDB")
		}
		for _, id := range ids {
			if _, ok := wanted[id]; !ok {
				continue
			}
			authority := result[id]
			can, err := item.action(id)
			if err != nil {
				return nil, errs.Internal(err)
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
			return nil, errs.DependencyUnavailable("SpiceDB")
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

func (s *PostService) loadPostCommentCounts(ctx context.Context, postIDs []string) (map[string]int32, error) {
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
		return nil, errs.Internal(err)
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
func (s *PostService) toProtoPost(p *model.Post, ogAsset *commonv1.AssetRef) *managev1.Post {
	post := &managev1.Post{
		Id:              p.ID,
		Title:           p.Title,
		Document:        p.ContentDocument,
		Revision:        p.ContentRevision,
		BlockMedia:      p.BlockMedia,
		Status:          managev1.PostStatus(managev1.PostStatus_value[string(p.Status)]),
		CommentsEnabled: p.CommentsEnabled,
		DocumentLayout:  p.DocumentLayout.Proto(),
		CreatedAt:       timestamppb.New(p.CreatedAt),
		OgAsset:         ogAsset,
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
				Id:   cat.ID,
				Name: cat.Name,
				Slug: &cat.Slug,
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
				Id:   tag.ID,
				Name: tag.Name,
				Slug: &tag.Slug,
			}
		}
	}

	// Convert series
	if p.Series != nil {
		post.Series = &managev1.Series{
			Id:    p.Series.ID,
			Title: p.Series.Title,
			Slug:  p.Series.Slug,
		}
		if p.Series.Description != nil {
			post.Series.Description = p.Series.Description
		}
	}

	return post
}

func (s *PostService) normalizeMapPlaceID(ctx context.Context, raw string) (*string, error) {
	mapPlaceID := strings.TrimSpace(raw)
	if mapPlaceID == "" {
		return nil, nil
	}

	var count int64
	if err := s.db.WithContext(ctx).
		Table("map_place").
		Where("id = ?", mapPlaceID).
		Count(&count).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if count == 0 {
		return nil, errs.InvalidArgument("map_place_id", "map place does not exist")
	}

	return &mapPlaceID, nil
}

// ============================================================================
// Version History
// ============================================================================

// ListPostVersions returns a paginated list of version snapshots for a post.
