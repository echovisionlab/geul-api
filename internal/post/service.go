package post

import (
	"context"
	"errors"
	"reflect"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// PostService implements the PostService Connect handler
type PostService struct {
	managev1connect.UnimplementedPostServiceHandler
	db             *gorm.DB
	cdnDomain      string
	spiceDB        *auth.SpiceDBClient
	kratosClient   auth.IdentityManager
	authz          *authz.SpiceDBResourceChecker
	fileService    FileService
	asyncPublisher AsyncPublisher
	shareLinks     ShareLinkValidator
	mediaLoader    ContentBlockMediaLoader
	members        MemberSummaryLoader
	versionRestore VersionRestoreSupport
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
	ogRefresher    *og.Refresher
}

type PostServiceOption func(*PostService)

func WithPostContentBlockStore(store *contentblock.Store) PostServiceOption {
	return func(service *PostService) {
		service.contentBlocks = store
	}
}

// NewPostService creates a new PostService
func NewPostService(
	db *gorm.DB,
	cdnDomain string,
	ogRefresher *og.Refresher,
	spiceDB *auth.SpiceDBClient,
	kratosClient auth.IdentityManager,
	fileService FileService,
	asyncPublisher AsyncPublisher,
	shareLinks ShareLinkValidator,
	mediaLoader ContentBlockMediaLoader,
	members MemberSummaryLoader,
	versionRestore VersionRestoreSupport,
	options ...PostServiceOption,
) *PostService {
	dependencycheck.New("PostService").
		RequireNotNil(db, "db").
		RequireNotNil(ogRefresher, "ogRefresher").
		RequireNotNil(spiceDB, "spiceDB").
		RequireNotNil(kratosClient, "kratosClient").
		RequireNotNil(fileService, "fileService").
		RequireNotNil(asyncPublisher, "asyncPublisher").
		RequireNotNil(shareLinks, "shareLinks").
		RequireNotNil(mediaLoader, "mediaLoader").
		RequireNotNil(members, "members").
		RequireNotNil(versionRestore, "versionRestore").
		Validate()
	service := &PostService{
		db: db, cdnDomain: cdnDomain, ogRefresher: ogRefresher, spiceDB: spiceDB,
		kratosClient: kratosClient,
		authz:        authz.NewSpiceDBResourceChecker(spiceDB, db, "post"),
		fileService:  fileService, asyncPublisher: asyncPublisher, shareLinks: shareLinks,
		mediaLoader: mediaLoader, members: members, versionRestore: versionRestore,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func NewAuditedPostService(
	db *gorm.DB,
	cdnDomain string,
	ogRefresher *og.Refresher,
	spiceDB *auth.SpiceDBClient,
	kratosClient auth.IdentityManager,
	fileService FileService,
	asyncPublisher AsyncPublisher,
	shareLinks ShareLinkValidator,
	mediaLoader ContentBlockMediaLoader,
	members MemberSummaryLoader,
	versionRestore VersionRestoreSupport,
	auditWriter domainaudit.Appender,
	options ...PostServiceOption,
) *PostService {
	if auditWriter == nil {
		panic("post audit writer is required")
	}
	service := NewPostService(
		db, cdnDomain, ogRefresher, spiceDB, kratosClient, fileService, asyncPublisher,
		shareLinks, mediaLoader, members, versionRestore, options...,
	)
	service.auditWriter = auditWriter
	return service
}

func (s *PostService) appendPostLifecycleAudit(ctx context.Context, tx *gorm.DB, postID string, previous, next model.PostStatus) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostStatusLifecycleAuditRecord(metadata, postID, postAuditState(previous), postAuditState(next))
	})
}

func (s *PostService) appendPostScheduleAudit(ctx context.Context, tx *gorm.DB, postID string, previous, next model.PostStatus, scheduledAt time.Time, timeZone string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostScheduleLifecycleAuditRecord(metadata, postID, postAuditState(previous), postAuditState(next), scheduledAt, timeZone)
	})
}

func postAuditState(status model.PostStatus) sharedtelemetry.AuditState {
	switch status {
	case model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()):
		return sharedtelemetry.AuditStateDraft
	case model.PostStatus(managev1.PostStatus_POST_STATUS_SCHEDULED.String()):
		return sharedtelemetry.AuditStateScheduled
	case model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()):
		return sharedtelemetry.AuditStatePublished
	case model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()):
		return sharedtelemetry.AuditStateArchived
	default:
		return sharedtelemetry.AuditState(status)
	}
}

// GetPost retrieves a post by ID
func (s *PostService) GetPost(
	ctx context.Context,
	req *connect.Request[managev1.GetPostRequest],
) (*connect.Response[managev1.Post], error) {
	var post model.Post
	if err := s.db.WithContext(ctx).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		First(&post, "id = ?", req.Msg.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("post", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	// The manage surface is only for exact editors/read-only archived editors.
	// Public and ShareLink reads use open.v1 and never enter the private inline
	// delivery path.
	if _, err := requirePostViewForStatus(ctx, s.spiceDB, post.ID, post.Status); err != nil {
		return nil, err
	}
	if err := s.overlayPostSourceLocaleDocument(ctx, &post); err != nil {
		return nil, err
	}
	return s.postResponseWithReadyOg(ctx, &post)
}

// ListPostsAdmin returns a paginated list of all posts for admin
func (s *PostService) ListPostsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListPostsAdminRequest],
) (*connect.Response[managev1.ListPostsAdminResponse], error) {
	if err := requirePostList(ctx, s.spiceDB); err != nil {
		return nil, err
	}

	var posts []model.Post
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Post{})

	// 1. Handle join filters directly (category_id, tag_id, author_id require subqueries)
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
		case "author_id":
			// Authorship is a durable domain relation, independent of the current
			// SpiceDB authorization grant.
			var authorIDs []string
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				authorIDs = []string{f.GetValue()}
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				authorIDs = f.GetValues()
			}
			if len(authorIDs) > 0 {
				query = query.Where("id IN (SELECT post_id FROM post_author WHERE member_id IN ?)", authorIDs)
			}
		default:
			remainingFilters = append(remainingFilters, f)
		}
	}

	// 2. Apply remaining filters using FilterConfig (status, series_id, published_at)
	var err error
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
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&posts).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayPostSourceLocaleDocuments(ctx, posts); err != nil {
		return nil, err
	}
	// The admin list is metadata-only; full Content Document hydration belongs
	// to GetPost so pagination does not perform one snapshot transaction per row.
	readyOgAssets, err := s.loadReadyPostOgAssets(ctx, posts)
	if err != nil {
		return nil, err
	}
	authorMembers, err := s.loadPostAuthorMembers(ctx, collectManagePostIDs(posts))
	if err != nil {
		return nil, err
	}

	commentCounts, err := s.loadPostCommentCounts(ctx, collectManagePostIDs(posts))
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
	postsWithStats := make([]*managev1.PostWithStats, len(posts))
	for i := range posts {
		postsWithStats[i] = &managev1.PostWithStats{
			Post: s.toProtoPostWithProjection(&posts[i], manageOgAssetFromReadyMap(
				readyOgAssets,
				posts[i].SourceLocaleOgAssetID,
				posts[i].OgAssetID,
			), authorMembers[posts[i].ID], featuredDeliveries[posts[i].ID], authorities[posts[i].ID]),
			ViewCount:    0,
			CommentCount: commentCounts[posts[i].ID],
		}
	}

	return connect.NewResponse(&managev1.ListPostsAdminResponse{
		Posts: postsWithStats,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreatePost creates a new post
func (s *PostService) CreatePost(
	ctx context.Context,
	req *connect.Request[managev1.CreatePostRequest],
) (*connect.Response[managev1.Post], error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}

	post, title, err := s.preparePostCreate(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	_, err = s.persistNewPost(
		ctx,
		post,
		req.Msg,
		title,
		user.MemberID.String(),
		user.IdentityID,
		req.Header().Get("Accept-Language"),
	)
	if err != nil {
		return nil, mapCreatePostError(err, req.Msg.Slug)
	}

	loadedPost, err := s.loadCreatedPost(ctx, post.ID)
	if err != nil {
		return nil, err
	}
	response, err := s.postResponseWithReadyOg(ctx, loadedPost)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// UpdatePost updates an existing post
func (s *PostService) UpdatePost(
	ctx context.Context,
	req *connect.Request[managev1.UpdatePostRequest],
) (*connect.Response[managev1.UpdatePostResponse], error) {
	var post model.Post
	if err := s.db.WithContext(ctx).First(&post, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("post", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	update, err := s.buildPostUpdate(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	changed := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		changed, err = s.updatePostWithDB(ctx, tx, &post, update)
		return err
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if changed {
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildManagePostContentUpdatedEvent(req.Msg))
	}
	return connect.NewResponse(&managev1.UpdatePostResponse{
		Id:              post.ID,
		Changed:         changed,
		Slug:            post.Slug,
		CommentsEnabled: post.CommentsEnabled,
		MapPlaceId:      post.MapPlaceID,
		DocumentLayout:  post.DocumentLayout.Proto(),
		UpdatedAt:       timestamppb.New(post.UpdatedAt),
	}), nil
}

type postUpdate struct {
	fields         structured.Fields
	normalizedSlug *string
	slugPresent    bool
}

func (s *PostService) buildPostUpdate(ctx context.Context, request *managev1.UpdatePostRequest) (postUpdate, error) {
	update := postUpdate{fields: structured.Fields{}}
	update.normalizedSlug, update.slugPresent = normalizeOptionalNullableString(request.Slug)
	if update.normalizedSlug != nil {
		if err := validateSlugWithoutSlash(*update.normalizedSlug); err != nil {
			return postUpdate{}, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "post", "posts", *update.normalizedSlug); err != nil {
			return postUpdate{}, err
		}
	}
	if update.slugPresent {
		update.fields["slug"] = optionalPostStringValue(update.normalizedSlug)
	}
	if request.CommentsEnabled != nil {
		update.fields["comments_enabled"] = *request.CommentsEnabled
	}
	if request.MapPlaceId != nil {
		mapPlaceID, err := s.normalizeMapPlaceID(ctx, *request.MapPlaceId)
		if err != nil {
			return postUpdate{}, err
		}
		update.fields["map_place_id"] = optionalPostStringValue(mapPlaceID)
	}
	if request.DocumentLayout != nil {
		documentLayout, err := model.DocumentLayoutFromProto(request.DocumentLayout)
		if err != nil {
			return postUpdate{}, errs.InvalidArgumentMsg(err.Error())
		}
		update.fields["document_layout"] = documentLayout
	}
	return update, nil
}

func optionalPostStringValue(value *string) structured.Value {
	if value == nil {
		return nil
	}
	return *value
}

func (s *PostService) updatePostWithDB(ctx context.Context, tx *gorm.DB, post *model.Post, update postUpdate) (bool, error) {
	if err := lockPostRootForLocaleWrite(ctx, tx, post.ID); err != nil {
		return false, err
	}
	var locked model.Post
	if err := tx.WithContext(ctx).Take(&locked, "id = ?", post.ID).Error; err != nil {
		return false, err
	}
	*post = locked
	if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, post.ID, post.Status, policyv1.Post.Edit); err != nil {
		return false, err
	}
	if update.slugPresent && update.normalizedSlug != nil {
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "post", "posts", *update.normalizedSlug); err != nil {
			return false, err
		}
	}
	changedFields := postUpdateChangedFields(post, update.fields)
	if len(changedFields) == 0 {
		return false, nil
	}
	mutationNow := time.Now()
	update.fields["updated_at"] = mutationNow
	if err := tx.Model(post).Updates(update.fields).Error; err != nil {
		return false, err
	}
	applyPostUpdateFields(post, update.fields)
	post.UpdatedAt = mutationNow
	if s.auditWriter == nil {
		return true, nil
	}
	if err := domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostConfigurationAuditRecord(metadata, post.ID, changedFields)
	}); err != nil {
		return false, err
	}
	return true, nil
}

func postUpdateChangedFields(post *model.Post, fields structured.Fields) []string {
	changed := make([]string, 0, len(fields))
	for field, next := range fields {
		var current any
		switch field {
		case "slug":
			current = post.Slug
		case "comments_enabled":
			current = post.CommentsEnabled
		case "map_place_id":
			current = post.MapPlaceID
		case "document_layout":
			current = post.DocumentLayout
		default:
			continue
		}
		if !reflect.DeepEqual(current, next) && !optionalPostValueEqual(current, next) {
			changed = append(changed, field)
		}
	}
	return changed
}

func optionalPostValueEqual(current, next any) bool {
	currentValue, ok := current.(*string)
	if !ok {
		return false
	}
	if next == nil {
		return currentValue == nil
	}
	nextValue, ok := next.(string)
	return ok && currentValue != nil && *currentValue == nextValue
}

func applyPostUpdateFields(post *model.Post, fields structured.Fields) {
	if value, exists := fields["slug"]; exists {
		post.Slug = structuredStringPointer(value)
	}
	if value, ok := fields["comments_enabled"].(bool); ok {
		post.CommentsEnabled = value
	}
	if value, exists := fields["map_place_id"]; exists {
		post.MapPlaceID = structuredStringPointer(value)
	}
	if value, ok := fields["document_layout"].(model.DocumentLayout); ok {
		post.DocumentLayout = value
	}
}

func structuredStringPointer(value any) *string {
	if value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}

// DeletePost deletes a post
func (s *PostService) DeletePost(
	ctx context.Context,
	req *connect.Request[managev1.DeletePostRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Post content Block store is not configured")
	}
	var post model.Post
	if err := s.db.WithContext(ctx).First(&post, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("post", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := lockPostRootForLocaleWrite(ctx, tx, post.ID); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Take(&post, "id = ?", post.ID).Error; err != nil {
			return err
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, post.ID, post.Status, policyv1.Post.Delete); err != nil {
			return err
		}
		if post.Status == model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()) {
			return errs.FailedPrecondition("archived posts must be republished before deletion")
		}
		snapshotPlan, err := policyv1.Post.Snapshot(post.ID)
		if err != nil {
			return err
		}
		snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
		if err != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		deleteRelationships, restoreRelationships, err := postAuthorizationDeletionBatches(post.ID, snapshots)
		if err != nil {
			return err
		}
		if err := validateResourceDeletionAuthorizationBatchSize("post", deleteRelationships, restoreRelationships); err != nil {
			return err
		}
		if len(deleteRelationships) == 0 {
			return errs.InternalMsg("post authorization relationships are missing")
		}
		if err := tx.
			Where("entity_type = ? AND entity_id = ?", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST.String(), req.Msg.Id).
			Delete(&model.ShareLink{}).Error; err != nil {
			return errs.Internal(err)
		}
		if err := cancelAndReleaseEntityOgWithDB(
			ctx, tx, s.cdnDomain,
			managev1.OgEntityType_OG_ENTITY_TYPE_POST,
			"post", req.Msg.Id,
		); err != nil {
			return err
		}
		documentID, err := loadPostContentDocumentID(ctx, tx, post.ID)
		if err != nil {
			return err
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx,
			tx,
			documentID,
			postLockedDocumentFence(post.ID, true),
		); err != nil {
			return normalizePostContentBlockError(err)
		}
		if err := tx.Delete(&post).Error; err != nil {
			return err
		}
		if s.auditWriter != nil {
			if err := domainaudit.AppendRequest(
				ctx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditPostDeleted,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewPostDeletedAuditRecord(metadata, post.ID)
				},
			); err != nil {
				return err
			}
		}
		return write(deleteRelationships, restoreRelationships)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// PublishPost publishes a draft post
func (s *PostService) PublishPost(
	ctx context.Context,
	req *connect.Request[managev1.PublishPostRequest],
) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	var post model.Post
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&post, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("post", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, post.ID, post.Status, policyv1.Post.Publish); err != nil {
			return err
		}
		if post.Status == model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()) {
			return errs.FailedPrecondition(errs.MsgPostAlreadyPublished)
		}
		if post.Status != model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()) &&
			post.Status != model.PostStatus(managev1.PostStatus_POST_STATUS_SCHEDULED.String()) {
			return errs.FailedPrecondition("only draft or scheduled posts can be published")
		}

		now := time.Now()
		previousStatus := post.Status
		updates := structured.Fields{
			"status":              managev1.PostStatus_POST_STATUS_PUBLISHED.String(),
			"scheduled_at":        nil,
			"scheduled_time_zone": nil,
			"updated_at":          now,
		}
		if post.PublishedAt == nil {
			updates["published_at"] = now
		}
		if err := tx.Model(&post).Updates(updates).Error; err != nil {
			return errs.Internal(err)
		}
		post.Status = model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String())
		post.ScheduledAt = nil
		post.ScheduledTimeZone = nil
		post.UpdatedAt = now
		if post.PublishedAt == nil {
			post.PublishedAt = &now
		}
		return s.appendPostLifecycleAudit(ctx, tx, post.ID, previousStatus, post.Status)
	}); err != nil {
		return nil, err
	}
	response, err := s.postLifecycleMutationResponse(ctx, &post, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

// UnpublishPost unpublishes a post back to draft
func (s *PostService) UnpublishPost(
	ctx context.Context,
	req *connect.Request[managev1.UnpublishPostRequest],
) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	var post model.Post
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&post, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("post", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, post.ID, post.Status, policyv1.Post.Publish); err != nil {
			return err
		}
		if post.Status != model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()) {
			return errs.FailedPrecondition("only published posts can be unpublished")
		}
		now := time.Now()
		previousStatus := post.Status
		if err := tx.Model(&post).Updates(structured.Fields{
			"status":              managev1.PostStatus_POST_STATUS_DRAFT.String(),
			"scheduled_at":        nil,
			"scheduled_time_zone": nil,
			"updated_at":          now,
		}).Error; err != nil {
			return errs.Internal(err)
		}
		post.Status = model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String())
		post.ScheduledAt = nil
		post.ScheduledTimeZone = nil
		post.UpdatedAt = now
		return s.appendPostLifecycleAudit(ctx, tx, post.ID, previousStatus, post.Status)
	}); err != nil {
		return nil, err
	}
	response, err := s.postLifecycleMutationResponse(ctx, &post, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

// ArchivePost archives a post
func (s *PostService) ArchivePost(
	ctx context.Context,
	req *connect.Request[managev1.ArchivePostRequest],
) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	var post model.Post
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&post, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("post", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, post.ID, post.Status, policyv1.Post.Publish); err != nil {
			return err
		}
		if post.Status != model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()) {
			return errs.FailedPrecondition("only published posts can be archived")
		}
		now := time.Now()
		previousStatus := post.Status
		if err := tx.Model(&post).Updates(structured.Fields{
			"status":     managev1.PostStatus_POST_STATUS_ARCHIVED.String(),
			"updated_at": now,
		}).Error; err != nil {
			return errs.Internal(err)
		}
		post.Status = model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String())
		post.UpdatedAt = now
		return s.appendPostLifecycleAudit(ctx, tx, post.ID, previousStatus, post.Status)
	}); err != nil {
		return nil, err
	}
	response, err := s.postLifecycleMutationResponse(ctx, &post, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (s *PostService) postLifecycleMutationResponse(
	ctx context.Context,
	post *model.Post,
	changed bool,
) (*managev1.PostLifecycleMutationResponse, error) {
	authorities, err := s.loadCurrentPostAuthorities(ctx, []string{post.ID})
	if err != nil {
		return nil, err
	}
	authority := authorities[post.ID]
	response := &managev1.PostLifecycleMutationResponse{
		Id:                post.ID,
		Changed:           changed,
		Status:            managev1.PostStatus(managev1.PostStatus_value[string(post.Status)]),
		ScheduledTimeZone: post.ScheduledTimeZone,
		UpdatedAt:         timestamppb.New(post.UpdatedAt),
		AllowedActions:    postAllowedActions(post.ID, post.Status, authority),
	}
	if post.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*post.PublishedAt)
	}
	if post.ScheduledAt != nil {
		response.ScheduledAt = timestamppb.New(*post.ScheduledAt)
	}
	return response, nil
}

// SetPostFeaturedImage sets the featured image for a post
func (s *PostService) SetPostFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.SetPostFeaturedImageRequest],
) (*connect.Response[managev1.SetPostFeaturedImageResponse], error) {
	// Verify post exists and check permission.
	var post model.Post
	if err := s.db.WithContext(ctx).First(&post, "id = ?", req.Msg.PostId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("post", req.Msg.PostId)
		}
		return nil, errs.Internal(err)
	}

	var ogRunID *string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Post
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status", "featured_image_file_id").
			First(&current, "id = ?", req.Msg.PostId).Error; err != nil {
			return err
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, current.ID, current.Status, policyv1.Post.Edit); err != nil {
			return err
		}
		if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, []string{req.Msg.FileId}); err != nil {
			return err
		}
		var file model.File
		if err := tx.First(&file, "id = ?", req.Msg.FileId).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Post{}).Where("id = ?", req.Msg.PostId).Updates(structured.Fields{
			"featured_image_file_id": req.Msg.FileId,
			"updated_at":             time.Now(),
		}).Error; err != nil {
			return err
		}
		if s.auditWriter != nil {
			if err := domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPostFeaturedImageAuditRecord(metadata, current.ID, req.Msg.FileId, sharedtelemetry.AuditCollectionOperationAdded)
			}); err != nil {
				return err
			}
		}
		plan, err := s.ogRefresher.RequestCurrentWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_POST,
			req.Msg.PostId, "", true, "post_featured_image_updated",
		)
		if err != nil {
			return err
		}
		ogRunID = &plan.RunID
		return nil
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("file", req.Msg.FileId)
		}
		return nil, err
	}

	featuredImageFileID := req.Msg.FileId
	featuredDelivery, err := s.resolveAuthorizedPostFeaturedImage(ctx, &model.Post{
		ID:                  post.ID,
		FeaturedImageFileID: &featuredImageFileID,
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.SetPostFeaturedImageResponse{
		ImageDelivery: featuredDelivery, OgGenerationRunId: ogRunID,
	}), nil
}

// DeletePostFeaturedImage removes the featured image from a post
func (s *PostService) DeletePostFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.DeletePostFeaturedImageRequest],
) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	var post model.Post
	if err := s.db.WithContext(ctx).First(&post, "id = ?", req.Msg.PostId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("post", req.Msg.PostId)
		}
		return nil, errs.Internal(err)
	}

	var ogRunID *string

	// Clear the reference and its binding atomically.
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status", "featured_image_file_id").
			First(&post, "id = ?", req.Msg.PostId).Error; err != nil {
			return err
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, post.ID, post.Status, policyv1.Post.Edit); err != nil {
			return err
		}
		if err := tx.Model(&post).Updates(structured.Fields{
			"featured_image_file_id": nil,
			"updated_at":             time.Now(),
		}).Error; err != nil {
			return err
		}
		if post.FeaturedImageFileID != nil && s.auditWriter != nil {
			if err := domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPostFeaturedImageAuditRecord(metadata, post.ID, *post.FeaturedImageFileID, sharedtelemetry.AuditCollectionOperationRemoved)
			}); err != nil {
				return err
			}
		}
		if post.FeaturedImageFileID != nil {
			ogPlan, err := s.ogRefresher.RequestCurrentWithDB(
				ctx, tx,
				managev1.OgEntityType_OG_ENTITY_TYPE_POST,
				post.ID, "", true, "post_featured_image_removed",
			)
			if err != nil {
				return err
			}
			if ogPlan != nil {
				ogRunID = &ogPlan.RunID
			}
			return nil
		}
		return nil
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: ogRunID}), nil
}
