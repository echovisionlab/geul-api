package post

import (
	"context"
	"errors"
	"fmt"
	"strings"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

// ListMyPosts returns the Posts the caller may edit.
func (s *Service) ListMyPosts(ctx context.Context, query ListPostsQuery) (PostPage, error) {
	scope, err := s.authorizedPostScope(ctx)
	if err != nil {
		return PostPage{}, err
	}
	page, err := s.listPostPage(ctx, query, ListQuery{Scope: &scope, Series: true})
	if err != nil {
		return PostPage{}, err
	}
	if page.contents, err = s.hydratePostContentProjections(ctx, page.posts); err != nil {
		return PostPage{}, err
	}
	if err := s.loadPostListProjections(ctx, &page); err != nil {
		return PostPage{}, err
	}
	views := make([]View, len(page.posts))
	for i := range page.posts {
		if views[i], err = s.projectListedPost(page, i); err != nil {
			return PostPage{}, err
		}
	}
	return PostPage{Posts: views, Pagination: page.pagination()}, nil
}

func (s *Service) overlayPostSourceLocaleDocument(ctx context.Context, stored *Post) error {
	states, err := readStore(ctx, s, func(store Store) (map[string]SourceLocaleState, error) {
		return store.SourceLocaleStates(ctx, []string{stored.ID})
	})
	if err != nil {
		return err
	}
	state, ok := states[stored.ID]
	if !ok {
		return domainerr.ResourceNotFound("post_translation", stored.ID)
	}
	OverlaySourceLocale(stored, state)
	return nil
}

func (s *Service) overlayPostSourceLocaleDocuments(ctx context.Context, posts []Post) error {
	sourceStates, err := readStore(ctx, s, func(store Store) (map[string]SourceLocaleState, error) {
		return store.SourceLocaleStates(ctx, collectManagePostIDs(posts))
	})
	if err != nil {
		return err
	}
	for i := range posts {
		state, ok := sourceStates[posts[i].ID]
		if !ok {
			return domainerr.ResourceNotFound("post_translation", posts[i].ID)
		}
		OverlaySourceLocale(&posts[i], state)
	}
	return nil
}

func (s *Service) loadReadyPostOgAssets(ctx context.Context, posts []Post) (map[string]*mediaasset.Reference, error) {
	candidates := make([]*string, 0, len(posts)*2)
	for i := range posts {
		candidates = append(
			candidates,
			posts[i].SourceLocaleOgAssetID,
			posts[i].OgAssetID,
		)
	}
	return s.loadReadyManageOgAssetRefs(ctx, candidates...)
}

// postView is the editor read of stored: its content, ready OG image,
// authors, featured image and the actions the caller may take.
func (s *Service) postView(ctx context.Context, stored Post) (View, error) {
	content, err := s.hydratePostContentProjection(ctx, &stored)
	if err != nil {
		return View{}, err
	}
	ogAsset, err := s.readyManageOgAssetRef(ctx, stored.SourceLocaleOgAssetID, stored.OgAssetID)
	if err != nil {
		return View{}, err
	}
	authors, err := s.loadPostAuthorMembers(ctx, []string{stored.ID})
	if err != nil {
		return View{}, err
	}
	featuredDelivery, err := s.resolveAuthorizedPostFeaturedImage(ctx, &stored)
	if err != nil {
		return View{}, err
	}
	authorities, err := s.loadCurrentPostAuthorities(ctx, []string{stored.ID})
	if err != nil {
		return View{}, err
	}
	return postViewWithProjection(stored, content, ogAsset, authors[stored.ID], featuredDelivery, authorities[stored.ID])
}

// postContent is the content projection a manage read attaches to a Post.
type postContent struct {
	document   *contentv1.RichTextDocument
	blockMedia []*contentv1.ContentBlockMediaItem
}

// hydratePostContentProjection reads the Post's source-locale document and
// its block media, and records the document revision on stored.
func (s *Service) hydratePostContentProjection(ctx context.Context, stored *Post) (postContent, error) {
	if strings.TrimSpace(stored.SourceLocale) == "" {
		return postContent{}, domainerr.Precondition("Post source locale is not initialized")
	}
	documentID, err := readStore(ctx, s, func(store Store) (uuid.UUID, error) {
		return loadPostContentDocumentID(ctx, store, stored.ID)
	})
	if err != nil {
		return postContent{}, err
	}
	var content postContent
	err = s.units.ReadLocked(ctx, func(store Store) error {
		root, err := lockPostContentDocumentRoot(ctx, store, stored.ID, documentID, true)
		if err != nil {
			return err
		}
		if _, err := requirePostViewForStatus(ctx, s.spiceDB, stored.ID, root.Status); err != nil {
			return err
		}
		if root.SourceLocale != stored.SourceLocale {
			return domainerr.Precondition("Post source locale changed; reload before reading")
		}
		snapshot, err := store.LockedContentSnapshot(ctx, documentID, stored.SourceLocale)
		if err != nil {
			return normalizePostContentBlockError(err)
		}
		document, err := contentblock.SnapshotToRichTextDocument(snapshot)
		if err != nil {
			return normalizePostContentBlockError(err)
		}
		mediaItems, err := store.BlockMedia(ctx, stored.ID, documentID, auth.GetUser(ctx))
		if err != nil {
			return err
		}
		stored.ContentRevision = snapshot.Document.Revision.String()
		content = postContent{document: document, blockMedia: mediaItems}
		return nil
	})
	return content, err
}

func (s *Service) hydratePostContentProjections(ctx context.Context, posts []Post) ([]postContent, error) {
	contents := make([]postContent, len(posts))
	for i := range posts {
		var err error
		if contents[i], err = s.hydratePostContentProjection(ctx, &posts[i]); err != nil {
			return nil, err
		}
	}
	return contents, nil
}

// LoadLocalizedContent materializes a selected locale of a Post's content
// document, in store's snapshot, only after the public caller has completed
// Post view authorization.
func LoadLocalizedContent(
	ctx context.Context,
	store ContentReads,
	postID string,
	locale string,
) (*contentv1.LocalizedRichTextDocument, string, error) {
	sourceLocale, err := store.SourceLocale(ctx, postID)
	if err != nil {
		if errors.Is(err, ErrPostNotFound) {
			return nil, "", domainerr.ResourceNotFound("post", postID)
		}
		return nil, "", err
	}
	if strings.TrimSpace(sourceLocale) == "" {
		return nil, "", domainerr.Precondition("Post source locale is not initialized")
	}
	if strings.TrimSpace(locale) == "" {
		locale = sourceLocale
	}
	documentID, err := loadPostContentDocumentID(ctx, store, postID)
	if err != nil {
		return nil, "", err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return nil, "", normalizePostContentBlockError(err)
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return nil, "", normalizePostContentBlockError(err)
	}
	return document, snapshot.Document.Revision.String(), nil
}

func postViewWithProjection(
	stored Post,
	content postContent,
	ogAsset *mediaasset.Reference,
	authors []*memberref.Summary,
	featuredImage *fileref.Delivery,
	authority PostAuthority,
) (View, error) {
	if err := stored.DocumentLayout.validateStored(); err != nil {
		return View{}, err
	}
	return View{
		Post: stored, Document: content.document, BlockMedia: content.blockMedia, OgAsset: ogAsset,
		Authors: authors, FeaturedImage: featuredImage,
		Actions: postAllowedActions(stored.ID, stored.Status, authority),
	}, nil
}

func (s *Service) loadPostAuthorMembers(ctx context.Context, postIDs []string) (map[string][]*memberref.Summary, error) {
	result := make(map[string][]*memberref.Summary, len(postIDs))
	if len(postIDs) == 0 {
		return result, nil
	}
	links, err := readStore(ctx, s, func(store Store) ([]AuthorLink, error) { return store.AuthorLinks(ctx, postIDs) })
	if err != nil {
		return nil, fmt.Errorf("load post authors: %w", err)
	}
	memberIDs := make([]string, 0, len(links))
	for _, link := range links {
		memberIDs = append(memberIDs, link.MemberID)
	}
	summaries, err := s.members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		if summary := summaries[link.MemberID]; summary != nil {
			result[link.PostID] = append(result[link.PostID], summary)
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
	spiceDB Authorizer,
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
	if len(postIDs) == 0 {
		return make(map[string]int32, len(postIDs)), nil
	}
	return readStore(ctx, s, func(store Store) (map[string]int32, error) { return store.CommentCounts(ctx, postIDs) })
}

func (s *Service) normalizeMapPlaceID(ctx context.Context, raw string) (*string, error) {
	return readStore(ctx, s, func(store Store) (*string, error) { return normalizeMapPlaceIDWithStore(ctx, store, raw) })
}

func normalizeMapPlaceIDWithStore(ctx context.Context, store Store, raw string) (*string, error) {
	mapPlaceID := strings.TrimSpace(raw)
	if mapPlaceID == "" {
		return nil, nil
	}
	exists, err := store.MapPlaceExists(ctx, mapPlaceID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, domainerr.InvalidField("map_place_id", "map place does not exist")
	}
	return &mapPlaceID, nil
}
