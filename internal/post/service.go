package post

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// msgPostAlreadyPublished answers publishing a published Post.
const msgPostAlreadyPublished = "post is already published"

// Deps are the collaborators of the Post use cases. Every field is
// required unless marked; NewService rejects a missing one.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	// CDNDomain builds OG asset URLs; empty serves relative paths.
	CDNDomain string
	OG        *og.Refresher
	Files     FileService
	// Signals schedules content.updated in the mutation's transaction.
	Signals    contentsignal.Publisher
	ShareLinks ShareLinkValidator
	// ShareLinkCleanup removes the share links of a deleted Post.
	ShareLinkCleanup ShareLinkCleanup
	MediaLoader      ContentBlockMediaLoader
	Media            ContentBlockMediaHydrator
	Members          MemberSummaryLoader
	VersionRestore   VersionRestoreSupport
	ContentBlocks    *contentblock.Store
	Audit            domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "OG": d.OG == nil, "Files": d.Files == nil,
		"Signals": d.Signals == nil, "ShareLinks": d.ShareLinks == nil, "ShareLinkCleanup": d.ShareLinkCleanup == nil,
		"MediaLoader": d.MediaLoader == nil, "Media": d.Media == nil, "Members": d.Members == nil,
		"VersionRestore": d.VersionRestore == nil, "ContentBlocks": d.ContentBlocks == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("post dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Post use cases: management, participants, versions,
// the collaboration boundary and AI documents. Adapters translate their
// requests into its commands and queries.
type Service struct {
	db               *gorm.DB
	cdnDomain        string
	spiceDB          *auth.SpiceDBClient
	fileService      FileService
	signals          contentsignal.Publisher
	shareLinks       ShareLinkValidator
	shareLinkCleanup ShareLinkCleanup
	mediaLoader      ContentBlockMediaLoader
	mediaHydrator    ContentBlockMediaHydrator
	members          MemberSummaryLoader
	versionRestore   VersionRestoreSupport
	contentBlocks    *contentblock.Store
	ogRefresher      *og.Refresher
	mutations        *mutation.Runner
}

// NewService validates deps and returns the Post use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, cdnDomain: deps.CDNDomain, spiceDB: deps.SpiceDB, fileService: deps.Files, signals: deps.Signals,
		shareLinks: deps.ShareLinks, shareLinkCleanup: deps.ShareLinkCleanup, mediaLoader: deps.MediaLoader,
		mediaHydrator: deps.Media, members: deps.Members, versionRestore: deps.VersionRestore,
		contentBlocks: deps.ContentBlocks, ogRefresher: deps.OG, mutations: mutations,
	}, nil
}

// postEffects collects what a Post mutation names while its change runs:
// the audit records appended once it commits, and the OG requests and
// content.updated signals of its outbox.
type postEffects struct {
	audits  []mutation.Audit
	og      []postOGRequest
	signals []*managev1.ContentUpdatedEvent
	// ogRunID is the run of the last OG request the outbox started.
	ogRunID *string
}

type postOGRequest struct {
	postID, locale, reason string
	force                  bool
}

func (e *postEffects) audit(action sharedtelemetry.AuditAction, build domainaudit.Builder) {
	e.audits = append(e.audits, mutation.RequestAudit(action, build))
}

func (e *postEffects) updated(build domainaudit.Builder) {
	e.audit(sharedtelemetry.AuditPostUpdated, build)
}

func (e *postEffects) requestOG(postID, locale string, force bool, reason string) {
	e.og = append(e.og, postOGRequest{postID: postID, locale: locale, force: force, reason: reason})
}

func (e *postEffects) signal(event *managev1.ContentUpdatedEvent) {
	e.signals = append(e.signals, event)
}

// runPostMutation runs change through the pipeline with spec's access and
// relationships; the records, OG requests and signals change names are its
// audit and outbox.
func runPostMutation[R any](
	ctx context.Context,
	s *Service,
	spec mutation.Spec[R],
	change func(context.Context, *gorm.DB, *postEffects) (R, error),
) (R, *postEffects, error) {
	effects := &postEffects{}
	spec.Change = func(ctx context.Context, tx *gorm.DB) (R, error) { return change(ctx, tx, effects) }
	spec.Audit = func(R) mutation.Audit { return mutation.Audits(effects.audits...) }
	spec.Outbox = func(ctx context.Context, tx *gorm.DB, _ R) error { return s.flushPostEffects(ctx, tx, effects) }
	result, err := mutation.Run(ctx, s.mutations, spec)
	return result, effects, err
}

func (s *Service) flushPostEffects(ctx context.Context, tx *gorm.DB, effects *postEffects) error {
	for _, request := range effects.og {
		plan, err := s.ogRefresher.RequestCurrentWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_POST, request.postID, request.locale, request.force, request.reason,
		)
		if err != nil {
			return err
		}
		if plan != nil {
			runID := plan.RunID
			effects.ogRunID = &runID
		}
	}
	for _, event := range effects.signals {
		if err := contentsignal.PublishInTransaction(ctx, tx, s.signals, event); err != nil {
			return err
		}
	}
	return nil
}

// readLocked runs read in one transaction after access, so the rows it
// locks agree with each other.
func (s *Service) readLocked(ctx context.Context, access mutation.Access, read func(context.Context, *gorm.DB) error) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) { return struct{}{}, read(ctx, tx) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

func postLifecycleAudit(effects *postEffects, postID string, previous, next model.PostStatus) {
	effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostStatusLifecycleAuditRecord(metadata, postID, postAuditState(previous), postAuditState(next))
	})
}

func postScheduleAudit(effects *postEffects, postID string, previous, next model.PostStatus, scheduledAt time.Time, timeZone string) {
	effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostScheduleLifecycleAuditRecord(metadata, postID, postAuditState(previous), postAuditState(next), scheduledAt, timeZone)
	})
}

func postAuditState(status model.PostStatus) sharedtelemetry.AuditState {
	switch status {
	case model.PostStatusDraft:
		return sharedtelemetry.AuditStateDraft
	case model.PostStatusScheduled:
		return sharedtelemetry.AuditStateScheduled
	case model.PostStatusPublished:
		return sharedtelemetry.AuditStatePublished
	case model.PostStatusArchived:
		return sharedtelemetry.AuditStateArchived
	default:
		return sharedtelemetry.AuditState(status)
	}
}

// Page selects a page of a list. A zero Limit reads the list's default.
type Page struct {
	Limit  int32
	Offset int32
}

func (p Page) limitOr(fallback int32) int32 {
	if p.Limit > 0 {
		return p.Limit
	}
	return fallback
}

// GetPostQuery reads a Post for its editors.
type GetPostQuery struct{ ID string }

// ListPostsQuery lists Posts. The filters category_id, tag_id and, for the
// administrator list, author_id select by relation.
type ListPostsQuery struct {
	Page    Page
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// CreatePostCommand creates a draft Post authored by the caller.
// SourceLocale, when empty, is the creator's preferred locale or one
// AcceptLanguage names.
type CreatePostCommand struct {
	Title           string
	Slug            *string
	Summary         *string
	CategoryIDs     []string
	TagIDs          []string
	CommentsEnabled bool
	MapPlaceID      *string
	SourceLocale    string
	Document        *contentv1.RichTextDocument
	AcceptLanguage  string
}

// UpdatePostCommand edits Post settings under the configuration revision
// the caller read. A nil field is left alone.
type UpdatePostCommand struct {
	ID                            string
	Slug                          *string
	CommentsEnabled               *bool
	MapPlaceID                    *string
	DocumentLayout                *commonv1.DocumentLayout
	ExpectedConfigurationRevision string
}

type DeletePostCommand struct{ ID string }

// PostLifecycleCommand names the Post a publish, unpublish, archive,
// cancellation or republish moves.
type PostLifecycleCommand struct{ ID string }

type SetPostFeaturedImageCommand struct{ PostID, FileID string }
type DeletePostFeaturedImageCommand struct{ PostID string }

// GetPost retrieves a post by ID
func (s *Service) GetPost(ctx context.Context, query GetPostQuery) (_ *managev1.Post, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.ID))

	var post model.Post
	if err := s.db.WithContext(ctx).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		First(&post, "id = ?", query.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("post", query.ID)
		}
		return nil, err
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

// applyPostRelationFilters applies the filters that select Posts by a
// relation table, which the filter configuration does not describe, and
// returns the others.
func applyPostRelationFilters(stored *gorm.DB, filters []*commonv1.FilterSpec, byAuthor bool) (*gorm.DB, []*commonv1.FilterSpec) {
	relations := map[string]string{
		"category_id": "id IN (SELECT post_id FROM post_category WHERE category_id %s)",
		"tag_id":      "id IN (SELECT post_id FROM post_tag WHERE tag_id %s)",
	}
	var remaining []*commonv1.FilterSpec
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		condition, related := relations[filter.GetField()]
		switch {
		case related && filter.GetOp() == commonv1.FilterOp_FILTER_OP_EQ:
			stored = stored.Where(fmt.Sprintf(condition, "= ?"), filter.GetValue())
		case related && filter.GetOp() == commonv1.FilterOp_FILTER_OP_IN:
			stored = stored.Where(fmt.Sprintf(condition, "IN ?"), filter.GetValues())
		case related:
		case byAuthor && filter.GetField() == "author_id":
			// Authorship is a durable domain relation, independent of the
			// current SpiceDB authorization grant.
			if authorIDs := postFilterValues(filter); len(authorIDs) > 0 {
				stored = stored.Where("id IN (SELECT post_id FROM post_author WHERE member_id IN ?)", authorIDs)
			}
		default:
			remaining = append(remaining, filter)
		}
	}
	return stored, remaining
}

func postFilterValues(filter *commonv1.FilterSpec) []string {
	switch filter.GetOp() {
	case commonv1.FilterOp_FILTER_OP_EQ:
		return []string{filter.GetValue()}
	case commonv1.FilterOp_FILTER_OP_IN:
		return filter.GetValues()
	default:
		return nil
	}
}

// postListPage is one page of Posts with the projections a list shows.
type postListPage struct {
	posts       []model.Post
	total       int64
	limit       int32
	offset      int32
	readyOg     map[string]*commonv1.AssetRef
	authors     map[string][]*commonv1.MemberSummary
	featured    map[string]*commonv1.MediaDelivery
	authorities map[string]PostAuthority
}

// listPostPage filters, counts and pages stored, then loads the projections
// every Post list shows.
func (s *Service) listPostPage(ctx context.Context, stored *gorm.DB, query ListPostsQuery, byAuthor bool, preload ...string) (postListPage, error) {
	stored, remaining := applyPostRelationFilters(stored, query.Filters, byAuthor)
	stored, err := PostAdminFilterConfig.ApplyFilters(stored, remaining)
	if err != nil {
		return postListPage{}, err
	}
	page := postListPage{limit: query.Page.limitOr(20), offset: query.Page.Offset}
	if err := stored.Count(&page.total).Error; err != nil {
		return postListPage{}, err
	}
	if stored, err = postSortConfig.ApplySort(stored, query.Sorts); err != nil {
		return postListPage{}, err
	}
	for _, relation := range preload {
		stored = stored.Preload(relation)
	}
	if err := stored.Limit(int(page.limit)).Offset(int(page.offset)).Find(&page.posts).Error; err != nil {
		return postListPage{}, err
	}
	if err := s.overlayPostSourceLocaleDocuments(ctx, page.posts); err != nil {
		return postListPage{}, err
	}
	return page, nil
}

func (s *Service) loadPostListProjections(ctx context.Context, page *postListPage) error {
	var err error
	if page.readyOg, err = s.loadReadyPostOgAssets(ctx, page.posts); err != nil {
		return err
	}
	if page.authors, err = s.loadPostAuthorMembers(ctx, collectManagePostIDs(page.posts)); err != nil {
		return err
	}
	if page.featured, err = s.loadPostPublicFeaturedImageDeliveries(ctx, page.posts); err != nil {
		return err
	}
	page.authorities, err = s.loadCurrentPostAuthorities(ctx, collectManagePostIDs(page.posts))
	return err
}

func (s *Service) projectListedPost(page postListPage, index int) (*managev1.Post, error) {
	post := &page.posts[index]
	return s.toProtoPostWithProjection(post, manageOgAssetFromReadyMap(
		page.readyOg, post.SourceLocaleOgAssetID, post.OgAssetID,
	), page.authors[post.ID], page.featured[post.ID], page.authorities[post.ID])
}

func (page postListPage) pagination() *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total:   int32(page.total),
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: page.offset+page.limit < int32(page.total),
	}
}

// ListPostsAdmin returns a paginated list of all posts for admin
func (s *Service) ListPostsAdmin(ctx context.Context, query ListPostsQuery) (*managev1.ListPostsAdminResponse, error) {
	if err := requirePostList(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	page, err := s.listPostPage(ctx, s.db.WithContext(ctx).Model(&model.Post{}), query, true, "Categories", "Tags")
	if err != nil {
		return nil, err
	}
	// The admin list is metadata-only; full Content Document hydration belongs
	// to GetPost so pagination does not perform one snapshot transaction per row.
	if err := s.loadPostListProjections(ctx, &page); err != nil {
		return nil, err
	}
	commentCounts, err := s.loadPostCommentCounts(ctx, collectManagePostIDs(page.posts))
	if err != nil {
		return nil, err
	}
	postsWithStats := make([]*managev1.PostWithStats, len(page.posts))
	for i := range page.posts {
		protoPost, err := s.projectListedPost(page, i)
		if err != nil {
			return nil, err
		}
		postsWithStats[i] = &managev1.PostWithStats{
			Post:         protoPost,
			ViewCount:    0,
			CommentCount: commentCounts[page.posts[i].ID],
		}
	}
	return &managev1.ListPostsAdminResponse{Posts: postsWithStats, Pagination: page.pagination()}, nil
}

// CreatePost creates a draft Post authored by the caller.
func (s *Service) CreatePost(ctx context.Context, command CreatePostCommand) (*managev1.Post, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, domainerr.AuthenticationRequired()
	}
	post, title, err := s.preparePostCreate(command)
	if err != nil {
		return nil, err
	}
	creator, err := policyv1.NewAccountIdentityActor(user.IdentityID.String())
	if err != nil {
		return nil, err
	}
	_, _, err = runPostMutation(ctx, s, mutation.Spec[*model.Post]{
		Access: postCreatorAccess(),
		Relationships: func(post *model.Post) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return postCreationRelationships(post.ID, creator)
		},
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) (*model.Post, error) {
		return post, s.persistNewPost(ctx, tx, effects, post, command, title, user.MemberID.String())
	})
	if err != nil {
		if !mutation.IsClassified(err) && dberrors.IsUniqueViolation(err) {
			return nil, domainerr.Duplicate("post", "slug", ptrStringValue(command.Slug)).WithCause(err)
		}
		return nil, err
	}
	loadedPost, err := s.loadCreatedPost(ctx, post.ID)
	if err != nil {
		return nil, err
	}
	return s.postResponseWithReadyOg(ctx, loadedPost)
}

// UpdatePost updates Post settings.
func (s *Service) UpdatePost(ctx context.Context, command UpdatePostCommand) (_ *managev1.UpdatePostResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.ID))

	if err := requireStoredPost(ctx, s.db, command.ID); err != nil {
		return nil, err
	}
	update, err := s.buildPostUpdate(ctx, command)
	if err != nil {
		return nil, err
	}
	var post model.Post
	// Manage metadata fields do not alter the typed translation source, so
	// an update publishes no content wake-up.
	changed, _, err := runPostMutation(ctx, s, mutation.Spec[bool]{
		Access: lockedPost(command.ID, policyv1.Post.Edit, nil),
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) (bool, error) {
		return updatePostWithDB(ctx, tx, effects, command.ID, &post, update)
	})
	if err != nil {
		return nil, err
	}
	documentLayout, err := post.DocumentLayout.Proto()
	if err != nil {
		return nil, err
	}
	return &managev1.UpdatePostResponse{
		Id:                    post.ID,
		Changed:               changed,
		Slug:                  post.Slug,
		CommentsEnabled:       post.CommentsEnabled,
		MapPlaceId:            post.MapPlaceID,
		DocumentLayout:        documentLayout,
		ConfigurationRevision: post.ConfigurationRevision,
		UpdatedAt:             timestamppb.New(post.UpdatedAt),
	}, nil
}

// requireStoredPost answers a missing Post before the mutation locks it.
func requireStoredPost(ctx context.Context, db *gorm.DB, postID string) error {
	var post model.Post
	if err := db.WithContext(ctx).Select("id").First(&post, "id = ?", postID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("post", postID)
		}
		return err
	}
	return nil
}

type postUpdate struct {
	fields                        structured.Fields
	normalizedSlug                *string
	slugPresent                   bool
	expectedConfigurationRevision string
}

func (s *Service) buildPostUpdate(ctx context.Context, command UpdatePostCommand) (postUpdate, error) {
	update := postUpdate{
		fields:                        structured.Fields{},
		expectedConfigurationRevision: command.ExpectedConfigurationRevision,
	}
	update.normalizedSlug, update.slugPresent = normalizeOptionalNullableString(command.Slug)
	if update.normalizedSlug != nil {
		if err := pathslug.Validate(*update.normalizedSlug); err != nil {
			return postUpdate{}, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "post", "posts", *update.normalizedSlug); err != nil {
			return postUpdate{}, err
		}
	}
	if update.slugPresent {
		update.fields["slug"] = optionalPostStringValue(update.normalizedSlug)
	}
	if command.CommentsEnabled != nil {
		update.fields["comments_enabled"] = *command.CommentsEnabled
	}
	if command.MapPlaceID != nil {
		mapPlaceID, err := s.normalizeMapPlaceID(ctx, *command.MapPlaceID)
		if err != nil {
			return postUpdate{}, err
		}
		update.fields["map_place_id"] = optionalPostStringValue(mapPlaceID)
	}
	if command.DocumentLayout != nil {
		documentLayout, err := model.DocumentLayoutFromProto(command.DocumentLayout)
		if err != nil {
			return postUpdate{}, domainerr.New(domainerr.Invalid, "", err.Error())
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

func updatePostWithDB(ctx context.Context, tx *gorm.DB, effects *postEffects, postID string, post *model.Post, update postUpdate) (bool, error) {
	if err := tx.WithContext(ctx).Take(post, "id = ?", postID).Error; err != nil {
		return false, err
	}
	if strings.TrimSpace(update.expectedConfigurationRevision) == "" {
		return false, domainerr.Precondition("Post configuration revision is required; reload before saving")
	}
	expectedRevision, err := uuidutil.ParseCanonical(update.expectedConfigurationRevision, "expected_configuration_revision")
	if err != nil {
		return false, domainerr.New(domainerr.Invalid, "", "expected_configuration_revision must be a canonical UUID").WithCause(err)
	}
	if expectedRevision.String() != post.ConfigurationRevision {
		return false, domainerr.Changed("Post configuration changed; reload before saving")
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
	update.fields["updated_at"] = dbtime.Now()
	if err := tx.Model(post).Clauses(clause.Returning{}).Updates(update.fields).Error; err != nil {
		return false, err
	}
	effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostConfigurationAuditRecord(metadata, post.ID, changedFields)
	})
	return true, nil
}

// DeletePost deletes a Post its lifecycle allows deleting, with its
// authorization relationships.
func (s *Service) DeletePost(ctx context.Context, command DeletePostCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.ID))

	if err := requireStoredPost(ctx, s.db, command.ID); err != nil {
		return err
	}
	var status model.PostStatus
	var restore []policyv1.RelationshipMutation
	_, _, err := runPostMutation(ctx, s, mutation.Spec[[]policyv1.RelationshipMutation]{
		Access: lockedPost(command.ID, policyv1.Post.Delete, &status),
		Relationships: func(remove []policyv1.RelationshipMutation) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return remove, restore, nil
		},
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) ([]policyv1.RelationshipMutation, error) {
		if status == model.PostStatusArchived {
			return nil, domainerr.Precondition("archived posts must be republished before deletion")
		}
		remove, compensate, err := s.postDeletionRelationships(ctx, command.ID)
		if err != nil {
			return nil, err
		}
		restore = compensate
		if err := s.deletePostWithDB(ctx, tx, command.ID); err != nil {
			return nil, err
		}
		effects.audit(sharedtelemetry.AuditPostDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostDeletedAuditRecord(metadata, command.ID)
		})
		return remove, nil
	})
	return err
}

// postDeletionRelationships snapshots the Post's relationships and returns
// their deletion batch and its exact restore.
func (s *Service) postDeletionRelationships(ctx context.Context, postID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	snapshotPlan, err := policyv1.Post.Snapshot(postID)
	if err != nil {
		return nil, nil, err
	}
	snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
	if err != nil {
		return nil, nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	remove, restore, err := postAuthorizationDeletionBatches(postID, snapshots)
	if err != nil {
		return nil, nil, err
	}
	if err := validateResourceDeletionAuthorizationBatchSize("post", remove, restore); err != nil {
		return nil, nil, err
	}
	if len(remove) == 0 {
		return nil, nil, errors.New("post authorization relationships are missing")
	}
	return remove, restore, nil
}

func (s *Service) deletePostWithDB(ctx context.Context, tx *gorm.DB, postID string) error {
	if err := s.shareLinkCleanup.DeleteEntityShareLinks(ctx, tx, postID); err != nil {
		return err
	}
	if err := cancelAndReleaseEntityOgWithDB(ctx, tx, s.cdnDomain, managev1.OgEntityType_OG_ENTITY_TYPE_POST, "post", postID); err != nil {
		return err
	}
	documentID, err := loadPostContentDocumentID(ctx, tx, postID)
	if err != nil {
		return err
	}
	if err := s.contentBlocks.DeleteDocument(ctx, tx, documentID, postLockedDocumentFence(postID, true)); err != nil {
		return normalizePostContentBlockError(err)
	}
	return tx.Delete(&model.Post{ID: postID}).Error
}

// postLifecycleMove moves a locked Post to next when allowed accepts its
// current status, and names the record of the move.
type postLifecycleMove struct {
	action  postAction
	allowed func(model.PostStatus) error
	apply   func(post *model.Post, updates structured.Fields, now time.Time)
	next    model.PostStatus
}

func (s *Service) movePostLifecycle(ctx context.Context, postID string, move postLifecycleMove) (_ *managev1.PostLifecycleMutationResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", postID))

	post, _, err := runPostMutation(ctx, s, mutation.Spec[model.Post]{
		Access: lockedPost(postID, move.action, nil),
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) (model.Post, error) {
		var post model.Post
		if err := tx.WithContext(ctx).First(&post, "id = ?", postID).Error; err != nil {
			return post, err
		}
		if err := move.allowed(post.Status); err != nil {
			return post, err
		}
		now := dbtime.Now()
		previousStatus := post.Status
		updates := structured.Fields{"status": string(move.next), "updated_at": now}
		move.apply(&post, updates, now)
		if err := tx.Model(&post).Updates(updates).Error; err != nil {
			return post, err
		}
		post.Status = move.next
		post.UpdatedAt = now
		postLifecycleAudit(effects, post.ID, previousStatus, post.Status)
		return post, nil
	})
	if err != nil {
		return nil, err
	}
	return s.postLifecycleMutationResponse(ctx, &post)
}

// clearPostSchedule leaves a moved Post without a schedule.
func clearPostSchedule(post *model.Post, updates structured.Fields) {
	updates["scheduled_at"] = nil
	updates["scheduled_time_zone"] = nil
	post.ScheduledAt = nil
	post.ScheduledTimeZone = nil
}

// publishPostAt clears the schedule and keeps the first publication time.
func publishPostAt(post *model.Post, updates structured.Fields, now time.Time) {
	clearPostSchedule(post, updates)
	if post.PublishedAt == nil {
		updates["published_at"] = now
		post.PublishedAt = &now
	}
}

// PublishPost publishes a draft or scheduled post now.
func (s *Service) PublishPost(ctx context.Context, command PostLifecycleCommand) (*managev1.PostLifecycleMutationResponse, error) {
	return s.movePostLifecycle(ctx, command.ID, postLifecycleMove{
		action: policyv1.Post.Publish,
		allowed: func(status model.PostStatus) error {
			if status == model.PostStatusPublished {
				return domainerr.Precondition(msgPostAlreadyPublished)
			}
			if status != model.PostStatusDraft &&
				status != model.PostStatusScheduled {
				return domainerr.Precondition("only draft or scheduled posts can be published")
			}
			return nil
		},
		apply: publishPostAt,
		next:  model.PostStatusPublished,
	})
}

// UnpublishPost returns a published or archived post to draft.
func (s *Service) UnpublishPost(ctx context.Context, command PostLifecycleCommand) (*managev1.PostLifecycleMutationResponse, error) {
	return s.movePostLifecycle(ctx, command.ID, postLifecycleMove{
		action: policyv1.Post.Publish,
		allowed: func(status model.PostStatus) error {
			if status != model.PostStatusPublished &&
				status != model.PostStatusArchived {
				return domainerr.Precondition("only published or archived posts can be unpublished")
			}
			return nil
		},
		apply: func(post *model.Post, updates structured.Fields, _ time.Time) { clearPostSchedule(post, updates) },
		next:  model.PostStatusDraft,
	})
}

// ArchivePost archives a published post.
func (s *Service) ArchivePost(ctx context.Context, command PostLifecycleCommand) (*managev1.PostLifecycleMutationResponse, error) {
	return s.movePostLifecycle(ctx, command.ID, postLifecycleMove{
		action: policyv1.Post.Publish,
		allowed: func(status model.PostStatus) error {
			if status != model.PostStatusPublished {
				return domainerr.Precondition("only published posts can be archived")
			}
			return nil
		},
		apply: func(*model.Post, structured.Fields, time.Time) {},
		next:  model.PostStatusArchived,
	})
}

// RepublishPost moves an archived post back to published.
func (s *Service) RepublishPost(ctx context.Context, command PostLifecycleCommand) (*managev1.PostLifecycleMutationResponse, error) {
	return s.movePostLifecycle(ctx, command.ID, postLifecycleMove{
		action: policyv1.Post.Publish,
		allowed: func(status model.PostStatus) error {
			if status != model.PostStatusArchived {
				return domainerr.Precondition("only archived posts can be republished")
			}
			return nil
		},
		apply: publishPostAt,
		next:  model.PostStatusPublished,
	})
}

func (s *Service) postLifecycleMutationResponse(
	ctx context.Context,
	post *model.Post,
) (*managev1.PostLifecycleMutationResponse, error) {
	authorities, err := s.loadCurrentPostAuthorities(ctx, []string{post.ID})
	if err != nil {
		return nil, err
	}
	authority := authorities[post.ID]
	response := &managev1.PostLifecycleMutationResponse{
		Id:                post.ID,
		Changed:           true,
		Status:            postStatusToProto(post.Status),
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
func (s *Service) SetPostFeaturedImage(ctx context.Context, command SetPostFeaturedImageCommand) (_ *managev1.SetPostFeaturedImageResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if err := requireStoredPost(ctx, s.db, command.PostID); err != nil {
		return nil, err
	}
	_, effects, err := runPostMutation(ctx, s, mutation.Spec[struct{}]{
		Access: lockedPost(command.PostID, policyv1.Post.Edit, nil),
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) (struct{}, error) {
		if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, []string{command.FileID}); err != nil {
			return struct{}{}, err
		}
		var file model.File
		if err := tx.First(&file, "id = ?", command.FileID).Error; err != nil {
			return struct{}{}, err
		}
		if err := tx.Model(&model.Post{}).Where("id = ?", command.PostID).Updates(structured.Fields{
			"featured_image_file_id": command.FileID,
			"updated_at":             time.Now(),
		}).Error; err != nil {
			return struct{}{}, err
		}
		effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFeaturedImageAuditRecord(metadata, command.PostID, command.FileID, sharedtelemetry.AuditCollectionOperationAdded)
		})
		effects.requestOG(command.PostID, "", true, "post_featured_image_updated")
		return struct{}{}, nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domainerr.ResourceNotFound("file", command.FileID)
	}
	if err != nil {
		return nil, err
	}
	featuredImageFileID := command.FileID
	featuredDelivery, err := s.resolveAuthorizedPostFeaturedImage(ctx, &model.Post{
		ID:                  command.PostID,
		FeaturedImageFileID: &featuredImageFileID,
	})
	if err != nil {
		return nil, err
	}
	return &managev1.SetPostFeaturedImageResponse{ImageDelivery: featuredDelivery, OgGenerationRunId: effects.ogRunID}, nil
}

// DeletePostFeaturedImage removes the featured image from a post
func (s *Service) DeletePostFeaturedImage(ctx context.Context, command DeletePostFeaturedImageCommand) (_ *managev1.OgAssetDeleteResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if err := requireStoredPost(ctx, s.db, command.PostID); err != nil {
		return nil, err
	}
	// Clear the reference and its binding atomically.
	_, effects, err := runPostMutation(ctx, s, mutation.Spec[struct{}]{
		Access: lockedPost(command.PostID, policyv1.Post.Edit, nil),
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) (struct{}, error) {
		var post model.Post
		if err := tx.Select("id", "status", "featured_image_file_id").First(&post, "id = ?", command.PostID).Error; err != nil {
			return struct{}{}, err
		}
		if err := tx.Model(&post).Updates(structured.Fields{
			"featured_image_file_id": nil,
			"updated_at":             time.Now(),
		}).Error; err != nil {
			return struct{}{}, err
		}
		if post.FeaturedImageFileID == nil {
			return struct{}{}, nil
		}
		removed := *post.FeaturedImageFileID
		effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFeaturedImageAuditRecord(metadata, post.ID, removed, sharedtelemetry.AuditCollectionOperationRemoved)
		})
		effects.requestOG(post.ID, "", true, "post_featured_image_removed")
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: effects.ogRunID}, nil
}
