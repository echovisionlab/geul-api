package post

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/uuidutil"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// msgPostAlreadyPublished answers publishing a published Post.
const msgPostAlreadyPublished = "post is already published"

// Authorizer is the SpiceDB port of the Post use cases: the mutation
// pipeline's checks and relationship writes, the checks of another Identity
// (participants, collaboration contributors), the lookups of the Posts a
// principal may read, and the relationship snapshot a deletion removes.
// *auth.SpiceDBClient is the production Authorizer.
type Authorizer interface {
	mutation.Authorizer
	CheckActorCan(ctx context.Context, actor policyv1.Actor, can policyv1.Can) (bool, error)
	LookupResources(ctx context.Context, lookup policyv1.ResourceLookup, actor policyv1.Actor) ([]string, error)
	SnapshotResourceRelationshipDescriptors(ctx context.Context, plan policyv1.RelationshipSnapshotPlan) ([]auth.RelationshipSnapshot, auth.ZedToken, error)
}

// Deps are the collaborators of the Post use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // postpostgres.NewUnits
	SpiceDB Authorizer
	Fence   identitystate.Fence // identitystate.PostgresFence{}
	Audit   domainaudit.Appender
	Files   FileService
	Members MemberSummaryLoader
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Files": d.Files == nil, "Members": d.Members == nil,
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
	units       mutation.UnitOfWork[Store]
	spiceDB     Authorizer
	fileService FileService
	members     MemberSummaryLoader
	mutations   *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Post use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, fileService: deps.Files, members: deps.Members, mutations: mutations,
	}, nil
}

// postEffects collects what a Post mutation names while its change runs:
// the audit records appended once it commits, and the OG requests and
// content.updated signals of its outbox.
type postEffects struct {
	audits  []mutation.Audit
	og      []postOGRequest
	signals []ContentUpdate
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

func (e *postEffects) signal(update ContentUpdate) {
	e.signals = append(e.signals, update)
}

// runPostMutation runs change through the pipeline with spec's access and
// relationships; the records, OG requests and signals change names are its
// audit and outbox.
func runPostMutation[R any](
	ctx context.Context,
	s *Service,
	spec mutation.StoreSpec[R, Store],
	change func(context.Context, Store, *postEffects) (R, error),
) (R, *postEffects, error) {
	effects := &postEffects{}
	spec.Change = func(ctx context.Context, store Store) (R, error) { return change(ctx, store, effects) }
	spec.Audit = func(R) mutation.Audit { return mutation.Audits(effects.audits...) }
	spec.Outbox = func(ctx context.Context, store Store, _ R) error { return flushPostEffects(ctx, store, effects) }
	result, err := mutation.Run(ctx, s.mutations, spec)
	return result, effects, err
}

func flushPostEffects(ctx context.Context, store Store, effects *postEffects) error {
	for _, request := range effects.og {
		runID, err := store.RequestOG(ctx, request.postID, request.locale, request.force, request.reason)
		if err != nil {
			return err
		}
		if runID != nil {
			effects.ogRunID = runID
		}
	}
	for _, update := range effects.signals {
		if err := store.PublishContentUpdate(ctx, update); err != nil {
			return err
		}
	}
	return nil
}

// readLocked runs read in one transaction after access, so the rows it
// locks agree with each other.
func (s *Service) readLocked(ctx context.Context, access mutation.Access, read func(context.Context, Store) error) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: access,
		Change: func(ctx context.Context, store Store) (struct{}, error) { return struct{}{}, read(ctx, store) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

// readStore runs load in one read-only snapshot.
func readStore[T any](ctx context.Context, s *Service, load func(Store) (T, error)) (T, error) {
	var result T
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		result, err = load(store)
		return err
	})
	return result, err
}

func postLifecycleAudit(effects *postEffects, postID string, previous, next Status) {
	effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostStatusLifecycleAuditRecord(metadata, postID, postAuditState(previous), postAuditState(next))
	})
}

func postScheduleAudit(effects *postEffects, postID string, previous, next Status, scheduledAt time.Time, timeZone string) {
	effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostScheduleLifecycleAuditRecord(metadata, postID, postAuditState(previous), postAuditState(next), scheduledAt, timeZone)
	})
}

func postAuditState(status Status) sharedtelemetry.AuditState {
	switch status {
	case StatusDraft:
		return sharedtelemetry.AuditStateDraft
	case StatusScheduled:
		return sharedtelemetry.AuditStateScheduled
	case StatusPublished:
		return sharedtelemetry.AuditStatePublished
	case StatusArchived:
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
// the caller read. A nil field is left alone. A DocumentLayout value the
// request left unspecified is empty.
type UpdatePostCommand struct {
	ID                            string
	Slug                          *string
	CommentsEnabled               *bool
	MapPlaceID                    *string
	DocumentLayout                *DocumentLayout
	ExpectedConfigurationRevision string
}

type DeletePostCommand struct{ ID string }

// PostLifecycleCommand names the Post a publish, unpublish, archive,
// cancellation or republish moves.
type PostLifecycleCommand struct{ ID string }

type SetPostFeaturedImageCommand struct{ PostID, FileID string }
type DeletePostFeaturedImageCommand struct{ PostID string }

// GetPost retrieves a post by ID
func (s *Service) GetPost(ctx context.Context, query GetPostQuery) (_ View, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.ID))

	stored, err := readStore(ctx, s, func(store Store) (Post, error) { return store.FindPost(ctx, query.ID) })
	if err != nil {
		if errors.Is(err, ErrPostNotFound) {
			return View{}, domainerr.ResourceNotFound("post", query.ID)
		}
		return View{}, err
	}
	// The manage surface is only for exact editors/read-only archived editors.
	// Public and ShareLink reads use open.v1 and never enter the private inline
	// delivery path.
	if _, err := requirePostViewForStatus(ctx, s.spiceDB, stored.ID, stored.Status); err != nil {
		return View{}, err
	}
	if err := s.overlayPostSourceLocaleDocument(ctx, &stored); err != nil {
		return View{}, err
	}
	return s.postView(ctx, stored)
}

// postListPage is one page of Posts with the projections a list shows.
type postListPage struct {
	posts       []Post
	total       int64
	limit       int32
	offset      int32
	readyOg     map[string]*mediaasset.Reference
	authors     map[string][]*memberref.Summary
	featured    map[string]*commonv1.MediaDelivery
	authorities map[string]PostAuthority
	// contents are the hydrated content projections of posts, by index; a
	// metadata-only list leaves them unset.
	contents []postContent
}

// listPostPage filters, counts and pages the Posts query selects, then
// overlays their source locale.
func (s *Service) listPostPage(ctx context.Context, query ListPostsQuery, list ListQuery) (postListPage, error) {
	page := postListPage{limit: query.Page.limitOr(20), offset: query.Page.Offset}
	list.Filters, list.Sorts, list.Limit, list.Offset = query.Filters, query.Sorts, page.limit, page.offset
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		page.posts, page.total, err = store.ListPosts(ctx, list)
		return err
	})
	if err != nil {
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

func (s *Service) projectListedPost(page postListPage, index int) (View, error) {
	stored := page.posts[index]
	var content postContent
	if page.contents != nil {
		content = page.contents[index]
	}
	return postViewWithProjection(stored, content, manageOgAssetFromReadyMap(
		page.readyOg, stored.SourceLocaleOgAssetID, stored.OgAssetID,
	), page.authors[stored.ID], page.featured[stored.ID], page.authorities[stored.ID])
}

func (page postListPage) pagination() Pagination {
	return Pagination{
		Total:   int32(page.total),
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: page.offset+page.limit < int32(page.total),
	}
}

// ListPostsAdmin returns a paginated list of all posts for admin
func (s *Service) ListPostsAdmin(ctx context.Context, query ListPostsQuery) (AdminPage, error) {
	if err := requirePostList(ctx, s.spiceDB); err != nil {
		return AdminPage{}, err
	}
	page, err := s.listPostPage(ctx, query, ListQuery{ByAuthor: true})
	if err != nil {
		return AdminPage{}, err
	}
	// The admin list is metadata-only; full Content Document hydration belongs
	// to GetPost so pagination does not perform one snapshot transaction per row.
	if err := s.loadPostListProjections(ctx, &page); err != nil {
		return AdminPage{}, err
	}
	commentCounts, err := s.loadPostCommentCounts(ctx, collectManagePostIDs(page.posts))
	if err != nil {
		return AdminPage{}, err
	}
	posts := make([]AdminView, len(page.posts))
	for i := range page.posts {
		view, err := s.projectListedPost(page, i)
		if err != nil {
			return AdminPage{}, err
		}
		posts[i] = AdminView{View: view, CommentCount: commentCounts[page.posts[i].ID]}
	}
	return AdminPage{Posts: posts, Pagination: page.pagination()}, nil
}

// CreatePost creates a draft Post authored by the caller.
func (s *Service) CreatePost(ctx context.Context, command CreatePostCommand) (View, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return View{}, domainerr.AuthenticationRequired()
	}
	created, title, err := preparePostCreate(command)
	if err != nil {
		return View{}, err
	}
	creator, err := policyv1.NewAccountIdentityActor(user.IdentityID.String())
	if err != nil {
		return View{}, err
	}
	_, _, err = runPostMutation(ctx, s, mutation.StoreSpec[*Post, Store]{
		Access: postCreatorAccess(),
		Relationships: func(created *Post) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return postCreationRelationships(created.ID, creator)
		},
	}, func(ctx context.Context, store Store, effects *postEffects) (*Post, error) {
		return created, persistNewPost(ctx, store, effects, created, command, title, user.MemberID.String())
	})
	if err != nil {
		if !mutation.IsClassified(err) && errors.Is(err, ErrSlugTaken) {
			return View{}, domainerr.Duplicate("post", "slug", ptrStringValue(command.Slug)).WithCause(err)
		}
		return View{}, err
	}
	loaded, err := s.loadCreatedPost(ctx, created.ID)
	if err != nil {
		return View{}, err
	}
	return s.postView(ctx, loaded)
}

// UpdatePost updates Post settings.
func (s *Service) UpdatePost(ctx context.Context, command UpdatePostCommand) (_ SettingsUpdate, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.ID))

	if err := s.requireStoredPost(ctx, command.ID); err != nil {
		return SettingsUpdate{}, err
	}
	update, err := s.buildPostUpdate(ctx, command)
	if err != nil {
		return SettingsUpdate{}, err
	}
	var stored Post
	// Manage metadata fields do not alter the typed translation source, so
	// an update publishes no content wake-up.
	changed, _, err := runPostMutation(ctx, s, mutation.StoreSpec[bool, Store]{
		Access: lockedPost(command.ID, policyv1.Post.Edit, nil),
	}, func(ctx context.Context, store Store, effects *postEffects) (bool, error) {
		return updatePost(ctx, store, effects, command.ID, &stored, update)
	})
	if err != nil {
		return SettingsUpdate{}, err
	}
	if err := stored.DocumentLayout.validateStored(); err != nil {
		return SettingsUpdate{}, err
	}
	return SettingsUpdate{
		ID: stored.ID, Changed: changed, Slug: stored.Slug, CommentsEnabled: stored.CommentsEnabled,
		MapPlaceID: stored.MapPlaceID, DocumentLayout: stored.DocumentLayout,
		ConfigurationRevision: stored.ConfigurationRevision, UpdatedAt: stored.UpdatedAt,
	}, nil
}

// requireStoredPost answers a missing Post before the mutation locks it.
func (s *Service) requireStoredPost(ctx context.Context, postID string) error {
	err := s.units.Read(ctx, func(store Store) error { return store.FindPostID(ctx, postID) })
	if errors.Is(err, ErrPostNotFound) {
		return domainerr.ResourceNotFound("post", postID)
	}
	return err
}

type postUpdate struct {
	change                        SettingsChange
	normalizedSlug                *string
	slugPresent                   bool
	expectedConfigurationRevision string
}

func (s *Service) buildPostUpdate(ctx context.Context, command UpdatePostCommand) (postUpdate, error) {
	update := postUpdate{expectedConfigurationRevision: command.ExpectedConfigurationRevision}
	update.normalizedSlug, update.slugPresent = normalizeOptionalNullableString(command.Slug)
	if update.normalizedSlug != nil {
		if err := pathslug.Validate(*update.normalizedSlug); err != nil {
			return postUpdate{}, err
		}
		if err := s.units.Read(ctx, func(store Store) error { return store.CheckRoute(ctx, *update.normalizedSlug) }); err != nil {
			return postUpdate{}, err
		}
	}
	if update.slugPresent {
		update.change.Slug = &OptionalString{Value: update.normalizedSlug}
	}
	update.change.CommentsEnabled = command.CommentsEnabled
	if command.MapPlaceID != nil {
		mapPlaceID, err := s.normalizeMapPlaceID(ctx, *command.MapPlaceID)
		if err != nil {
			return postUpdate{}, err
		}
		update.change.MapPlaceID = &OptionalString{Value: mapPlaceID}
	}
	if command.DocumentLayout != nil {
		if err := command.DocumentLayout.validateRequested(); err != nil {
			return postUpdate{}, domainerr.New(domainerr.Invalid, "", err.Error())
		}
		layout := *command.DocumentLayout
		update.change.DocumentLayout = &layout
	}
	return update, nil
}

// postUpdateChangedFields names the settings change would move on stored,
// in the order of the API's field names.
func postUpdateChangedFields(stored Post, change SettingsChange) []string {
	changed := make([]string, 0, 4)
	if change.CommentsEnabled != nil && stored.CommentsEnabled != *change.CommentsEnabled {
		changed = append(changed, "comments_enabled")
	}
	if change.DocumentLayout != nil && !reflect.DeepEqual(stored.DocumentLayout, *change.DocumentLayout) {
		changed = append(changed, "document_layout")
	}
	if change.MapPlaceID != nil && !nullableStringEqual(stored.MapPlaceID, change.MapPlaceID.Value) {
		changed = append(changed, "map_place_id")
	}
	if change.Slug != nil && !nullableStringEqual(stored.Slug, change.Slug.Value) {
		changed = append(changed, "slug")
	}
	return changed
}

func updatePost(ctx context.Context, store Store, effects *postEffects, postID string, stored *Post, update postUpdate) (bool, error) {
	var err error
	if *stored, err = store.TakePost(ctx, postID); err != nil {
		return false, err
	}
	if strings.TrimSpace(update.expectedConfigurationRevision) == "" {
		return false, domainerr.Precondition("Post configuration revision is required; reload before saving")
	}
	expectedRevision, err := uuidutil.ParseCanonical(update.expectedConfigurationRevision, "expected_configuration_revision")
	if err != nil {
		return false, domainerr.New(domainerr.Invalid, "", "expected_configuration_revision must be a canonical UUID").WithCause(err)
	}
	if expectedRevision.String() != stored.ConfigurationRevision {
		return false, domainerr.Retry("Post configuration changed; reload before saving")
	}
	if update.slugPresent && update.normalizedSlug != nil {
		if err := store.LockRoute(ctx, *update.normalizedSlug); err != nil {
			return false, err
		}
	}
	changedFields := postUpdateChangedFields(*stored, update.change)
	if len(changedFields) == 0 {
		return false, nil
	}
	update.change.UpdatedAt = dbtime.Now()
	if err := store.UpdateSettings(ctx, stored, update.change); err != nil {
		return false, err
	}
	updatedID := stored.ID
	effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostConfigurationAuditRecord(metadata, updatedID, changedFields)
	})
	return true, nil
}

// DeletePost deletes a Post its lifecycle allows deleting, with its
// authorization relationships.
func (s *Service) DeletePost(ctx context.Context, command DeletePostCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.ID))

	if err := s.requireStoredPost(ctx, command.ID); err != nil {
		return err
	}
	var status Status
	var restore []policyv1.RelationshipMutation
	_, _, err := runPostMutation(ctx, s, mutation.StoreSpec[[]policyv1.RelationshipMutation, Store]{
		Access: lockedPost(command.ID, policyv1.Post.Delete, &status),
		Relationships: func(remove []policyv1.RelationshipMutation) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return remove, restore, nil
		},
	}, func(ctx context.Context, store Store, effects *postEffects) ([]policyv1.RelationshipMutation, error) {
		if status == StatusArchived {
			return nil, domainerr.Precondition("archived posts must be republished before deletion")
		}
		remove, compensate, err := s.postDeletionRelationships(ctx, command.ID)
		if err != nil {
			return nil, err
		}
		restore = compensate
		if err := deletePost(ctx, store, command.ID); err != nil {
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

func deletePost(ctx context.Context, store Store, postID string) error {
	if err := store.DeleteShareLinks(ctx, postID); err != nil {
		return err
	}
	if err := store.CancelOG(ctx, postID); err != nil {
		return err
	}
	documentID, err := loadPostContentDocumentID(ctx, store, postID)
	if err != nil {
		return err
	}
	if err := store.DeleteContentDocument(ctx, documentID, postLockedDocumentFence(postID, true)); err != nil {
		return normalizePostContentBlockError(err)
	}
	return store.DeletePost(ctx, postID)
}

// postLifecycleMove moves a locked Post to next when allowed accepts its
// current status, and names the record of the move.
type postLifecycleMove struct {
	action  postAction
	allowed func(Status) error
	apply   func(stored *Post, change *LifecycleChange, now time.Time)
	next    Status
}

func (s *Service) movePostLifecycle(ctx context.Context, postID string, move postLifecycleMove) (_ Lifecycle, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", postID))

	moved, _, err := runPostMutation(ctx, s, mutation.StoreSpec[Post, Store]{
		Access: lockedPost(postID, move.action, nil),
	}, func(ctx context.Context, store Store, effects *postEffects) (Post, error) {
		stored, err := store.ReadPost(ctx, postID)
		if err != nil {
			return stored, err
		}
		if err := move.allowed(stored.Status); err != nil {
			return stored, err
		}
		now := dbtime.Now()
		previousStatus := stored.Status
		change := LifecycleChange{Status: move.next, UpdatedAt: now}
		move.apply(&stored, &change, now)
		if err := store.UpdateLifecycle(ctx, stored.ID, change); err != nil {
			return stored, err
		}
		stored.Status = move.next
		stored.UpdatedAt = now
		postLifecycleAudit(effects, stored.ID, previousStatus, stored.Status)
		return stored, nil
	})
	if err != nil {
		return Lifecycle{}, err
	}
	return s.postLifecycle(ctx, moved)
}

// clearPostSchedule leaves a moved Post without a schedule.
func clearPostSchedule(stored *Post, change *LifecycleChange) {
	change.ClearSchedule = true
	stored.ScheduledAt = nil
	stored.ScheduledTimeZone = nil
}

// publishPostAt clears the schedule and keeps the first publication time.
func publishPostAt(stored *Post, change *LifecycleChange, now time.Time) {
	clearPostSchedule(stored, change)
	if stored.PublishedAt == nil {
		change.PublishedAt = &now
		stored.PublishedAt = &now
	}
}

// PublishPost publishes a draft or scheduled post now.
func (s *Service) PublishPost(ctx context.Context, command PostLifecycleCommand) (Lifecycle, error) {
	return s.movePostLifecycle(ctx, command.ID, postLifecycleMove{
		action: policyv1.Post.Publish,
		allowed: func(status Status) error {
			if status == StatusPublished {
				return domainerr.Precondition(msgPostAlreadyPublished)
			}
			if status != StatusDraft &&
				status != StatusScheduled {
				return domainerr.Precondition("only draft or scheduled posts can be published")
			}
			return nil
		},
		apply: publishPostAt,
		next:  StatusPublished,
	})
}

// UnpublishPost returns a published or archived post to draft.
func (s *Service) UnpublishPost(ctx context.Context, command PostLifecycleCommand) (Lifecycle, error) {
	return s.movePostLifecycle(ctx, command.ID, postLifecycleMove{
		action: policyv1.Post.Publish,
		allowed: func(status Status) error {
			if status != StatusPublished &&
				status != StatusArchived {
				return domainerr.Precondition("only published or archived posts can be unpublished")
			}
			return nil
		},
		apply: func(stored *Post, change *LifecycleChange, _ time.Time) { clearPostSchedule(stored, change) },
		next:  StatusDraft,
	})
}

// ArchivePost archives a published post.
func (s *Service) ArchivePost(ctx context.Context, command PostLifecycleCommand) (Lifecycle, error) {
	return s.movePostLifecycle(ctx, command.ID, postLifecycleMove{
		action: policyv1.Post.Publish,
		allowed: func(status Status) error {
			if status != StatusPublished {
				return domainerr.Precondition("only published posts can be archived")
			}
			return nil
		},
		apply: func(*Post, *LifecycleChange, time.Time) {},
		next:  StatusArchived,
	})
}

// RepublishPost moves an archived post back to published.
func (s *Service) RepublishPost(ctx context.Context, command PostLifecycleCommand) (Lifecycle, error) {
	return s.movePostLifecycle(ctx, command.ID, postLifecycleMove{
		action: policyv1.Post.Publish,
		allowed: func(status Status) error {
			if status != StatusArchived {
				return domainerr.Precondition("only archived posts can be republished")
			}
			return nil
		},
		apply: publishPostAt,
		next:  StatusPublished,
	})
}

func (s *Service) postLifecycle(ctx context.Context, stored Post) (Lifecycle, error) {
	authorities, err := s.loadCurrentPostAuthorities(ctx, []string{stored.ID})
	if err != nil {
		return Lifecycle{}, err
	}
	return Lifecycle{
		ID: stored.ID, Status: stored.Status, ScheduledAt: stored.ScheduledAt, ScheduledTimeZone: stored.ScheduledTimeZone,
		PublishedAt: stored.PublishedAt, UpdatedAt: stored.UpdatedAt,
		Actions: postAllowedActions(stored.ID, stored.Status, authorities[stored.ID]),
	}, nil
}

// SetPostFeaturedImage sets the featured image for a post
func (s *Service) SetPostFeaturedImage(ctx context.Context, command SetPostFeaturedImageCommand) (_ FeaturedImage, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if err := s.requireStoredPost(ctx, command.PostID); err != nil {
		return FeaturedImage{}, err
	}
	_, effects, err := runPostMutation(ctx, s, mutation.StoreSpec[struct{}, Store]{
		Access: lockedPost(command.PostID, policyv1.Post.Edit, nil),
	}, func(ctx context.Context, store Store, effects *postEffects) (struct{}, error) {
		if err := store.LockAttachableFiles(ctx, []string{command.FileID}); err != nil {
			return struct{}{}, err
		}
		if err := store.FindFile(ctx, command.FileID); err != nil {
			return struct{}{}, err
		}
		fileID := command.FileID
		if err := store.SetFeaturedImage(ctx, command.PostID, &fileID, time.Now()); err != nil {
			return struct{}{}, err
		}
		effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFeaturedImageAuditRecord(metadata, command.PostID, command.FileID, sharedtelemetry.AuditCollectionOperationAdded)
		})
		effects.requestOG(command.PostID, "", true, "post_featured_image_updated")
		return struct{}{}, nil
	})
	if errors.Is(err, ErrFileNotFound) {
		return FeaturedImage{}, domainerr.ResourceNotFound("file", command.FileID)
	}
	if err != nil {
		return FeaturedImage{}, err
	}
	featuredImageFileID := command.FileID
	featuredDelivery, err := s.resolveAuthorizedPostFeaturedImage(ctx, &Post{
		ID:                  command.PostID,
		FeaturedImageFileID: &featuredImageFileID,
	})
	if err != nil {
		return FeaturedImage{}, err
	}
	return FeaturedImage{Delivery: featuredDelivery, OgRunID: effects.ogRunID}, nil
}

// DeletePostFeaturedImage removes the featured image from a post and
// returns the OG generation the change started, if any.
func (s *Service) DeletePostFeaturedImage(ctx context.Context, command DeletePostFeaturedImageCommand) (_ *string, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if err := s.requireStoredPost(ctx, command.PostID); err != nil {
		return nil, err
	}
	// Clear the reference and its binding atomically.
	_, effects, err := runPostMutation(ctx, s, mutation.StoreSpec[struct{}, Store]{
		Access: lockedPost(command.PostID, policyv1.Post.Edit, nil),
	}, func(ctx context.Context, store Store, effects *postEffects) (struct{}, error) {
		current, err := store.FeaturedImageFileID(ctx, command.PostID)
		if err != nil {
			return struct{}{}, err
		}
		if err := store.SetFeaturedImage(ctx, command.PostID, nil, time.Now()); err != nil {
			return struct{}{}, err
		}
		if current == nil {
			return struct{}{}, nil
		}
		removed := *current
		effects.updated(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFeaturedImageAuditRecord(metadata, command.PostID, removed, sharedtelemetry.AuditCollectionOperationRemoved)
		})
		effects.requestOG(command.PostID, "", true, "post_featured_image_removed")
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return effects.ogRunID, nil
}
