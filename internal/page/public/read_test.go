package public_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/page/pagetest"
	pagepublic "github.com/echovisionlab/geul-api/internal/page/public"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type reader struct {
	service *pagepublic.Service
	store   *pagetest.Store
	checker *authztest.Checker
	drafts  map[string]bool
	media   *media
	fence   *identitystatetest.Fence
}

func newReader(t *testing.T) *reader {
	t.Helper()
	store, units := pagetest.NewPublic()
	r := &reader{store: store, checker: &authztest.Checker{}, drafts: map[string]bool{}, media: &media{}, fence: &identitystatetest.Fence{}}
	store.Fence = r.fence
	service, err := pagepublic.NewService(pagepublic.Deps{
		Units: units, DraftAccess: draftAccess(r.drafts), Media: r.media, Text: plainText{}, Permissions: r.checker,
	})
	require.NoError(t, err)
	r.service = service
	return r
}

type draftAccess map[string]bool

func (d draftAccess) CanViewPageDraft(_ context.Context, pageID string) (bool, error) {
	return d[pageID], nil
}

type plainText struct{}

func (plainText) ResolvePublicText(_ context.Context, text string) (string, error) { return text, nil }

// media delivers what it is asked for and records the owner authorization
// the Block media was hydrated under.
type media struct {
	authorization mediaasset.ContentDownloadOwnerAuthorization
}

func (m *media) HydrateAuthorizedContentBlockMedia(ctx context.Context, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	m.authorization, _ = mediaasset.ContentDownloadOwnerAuthorizationFromContext(ctx)
	return items, nil
}

func (*media) ResolvePageFeaturedImageDelivery(_ context.Context, fileID *string) (*commonv1.MediaDelivery, error) {
	if fileID == nil {
		return nil, nil
	}
	return &commonv1.MediaDelivery{FileId: *fileID}, nil
}

func (*media) ResolvePageOGAsset(context.Context, *string, *string) (*commonv1.AssetRef, error) {
	return nil, nil
}

func signedIn(t *testing.T) context.Context {
	t.Helper()
	return auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func policy(t *testing.T, value pageaccess.Policy) []byte {
	t.Helper()
	encoded, err := pageaccess.Encode(value)
	require.NoError(t, err)
	return encoded
}

func (r *reader) put(slug string, status page.Status, access pageaccess.Policy, t *testing.T) page.Page {
	t.Helper()
	return r.store.PutPage(page.Page{Slug: &slug, Status: status, AccessPolicy: policy(t, access)}, "Title "+slug)
}

func TestPublicPageAccessFollowsStatusAudienceAndViewer(t *testing.T) {
	r := newReader(t)
	public := r.put("public", page.StatusPublished, pageaccess.Policy{}, t)
	authenticated := r.put("authenticated", page.StatusPublished, pageaccess.Policy{Mode: pageaccess.ModeAuthenticated}, t)
	subscribers := r.put("subscribers", page.StatusPublished, pageaccess.Policy{Mode: pageaccess.ModeConditions, NewsletterSubscriber: true}, t)
	draft := r.put("draft", page.StatusDraft, pageaccess.Policy{}, t)
	reader := signedIn(t)

	for _, test := range []struct {
		name    string
		ctx     context.Context
		value   page.Page
		reason  pageaccess.Reason
		visible bool
	}{
		{name: "anyone reads a public Page", ctx: t.Context(), value: public, reason: pageaccess.ReasonAllowed, visible: true},
		{name: "an anonymous reader must sign in", ctx: t.Context(), value: authenticated, reason: pageaccess.ReasonAuthenticationRequired},
		{name: "a signed-in reader reads an authenticated Page", ctx: reader, value: authenticated, reason: pageaccess.ReasonAllowed, visible: true},
		{name: "a reader outside the conditions", ctx: reader, value: subscribers, reason: pageaccess.ReasonConditionsNotMet},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, locator := range []string{*test.value.Slug, test.value.ID} {
				read, err := r.service.GetPage(test.ctx, pagepublic.GetPageQuery{Slug: locator})
				require.NoError(t, err)
				require.Equal(t, test.reason, read.AccessReason)
				require.Equal(t, test.visible, read.Visible)
				if !test.visible {
					require.Empty(t, read.Page.ID, "a denied read projects nothing")
					require.Nil(t, read.Document)
				}
			}
		})
	}

	read, err := r.service.GetPage(t.Context(), pagepublic.GetPageQuery{Slug: "public"})
	require.NoError(t, err)
	require.Equal(t, "Title public", read.Title)
	require.Equal(t, mediaasset.ContentDownloadOwnerAccessPublic, r.media.authorization.Mode)
	require.Equal(t, public.ID, r.media.authorization.ResourceID)

	_, err = r.service.GetPage(t.Context(), pagepublic.GetPageQuery{Slug: "draft"})
	requireNotFound(t, err)
	r.drafts[draft.ID] = true
	read, err = r.service.GetPage(reader, pagepublic.GetPageQuery{Slug: "draft"})
	require.NoError(t, err)
	require.True(t, read.Visible)
	require.Equal(t, mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft, r.media.authorization.Mode)
	_, err = r.service.GetPage(t.Context(), pagepublic.GetPageQuery{Slug: "missing"})
	requireNotFound(t, err)
}

func TestPublicPageAudienceRechecksTheReaderOnEveryRead(t *testing.T) {
	r := newReader(t)
	tag := uuid.NewString()
	subscribers := r.put("members", page.StatusPublished, pageaccess.Policy{Mode: pageaccess.ModeConditions, UserTagIDs: []string{tag}}, t)
	ctx := signedIn(t)
	user := auth.GetUser(ctx)
	read, err := r.service.GetPage(ctx, pagepublic.GetPageQuery{Slug: subscribers.ID})
	require.NoError(t, err)
	require.Equal(t, pageaccess.ReasonConditionsNotMet, read.AccessReason)

	r.store.PutUserTag(tag, user.MemberID.String())
	read, err = r.service.GetPage(ctx, pagepublic.GetPageQuery{Slug: subscribers.ID})
	require.NoError(t, err)
	require.True(t, read.Visible)

	r.fence.Deactivate(user.IdentityID.String())
	read, err = r.service.GetPage(ctx, pagepublic.GetPageQuery{Slug: subscribers.ID})
	require.NoError(t, err)
	require.Equal(t, pageaccess.ReasonAuthenticationRequired, read.AccessReason, "the stored identity state overrides the session")

	manage, err := policyv1.Page.Manage(subscribers.ID)
	require.NoError(t, err)
	r.checker.Allow(manage, nil)
	read, err = r.service.GetPage(signedIn(t), pagepublic.GetPageQuery{Slug: subscribers.ID})
	require.NoError(t, err)
	require.True(t, read.Visible, "a manager reads the Page whatever its conditions")
}

func TestPublicDraftOpensOnlyWithItsShareLink(t *testing.T) {
	r := newReader(t)
	draft := r.put("shared", page.StatusDraft, pageaccess.Policy{}, t)
	witness := r.store.PutShareLink(draft.ID, "token", "secret")

	_, err := r.service.GetPage(t.Context(), pagepublic.GetPageQuery{Slug: "shared", ShareToken: text("token"), SharePassword: "wrong"})
	requireNotFound(t, err)
	read, err := r.service.GetPage(t.Context(), pagepublic.GetPageQuery{Slug: "shared", ShareToken: text("token"), SharePassword: "secret"})
	require.NoError(t, err)
	require.True(t, read.Visible)
	require.Equal(t, mediaasset.ContentDownloadOwnerAccessShare, r.media.authorization.Mode)
	require.Equal(t, witness, r.media.authorization.ShareLink)
}

func TestPublicHomepageReadsOnlyAPublishedPage(t *testing.T) {
	r := newReader(t)
	read, err := r.service.GetPage(t.Context(), pagepublic.GetPageQuery{Slug: "/"})
	require.NoError(t, err)
	require.False(t, read.Visible)
	require.Equal(t, pageaccess.ReasonUnspecified, read.AccessReason)

	draft := r.put("home-draft", page.StatusDraft, pageaccess.Policy{}, t)
	r.drafts[draft.ID] = true
	r.store.PutHomepage(draft.ID)
	_, err = r.service.GetPage(signedIn(t), pagepublic.GetPageQuery{Slug: ""})
	requireNotFound(t, err)

	home := r.put("home", page.StatusPublished, pageaccess.Policy{}, t)
	r.store.PutHomepage(home.ID)
	read, err = r.service.GetPage(t.Context(), pagepublic.GetPageQuery{Slug: "/"})
	require.NoError(t, err)
	require.Equal(t, home.ID, read.Page.ID)

	r.store.PutHomepage(uuid.NewString())
	read, err = r.service.GetPage(t.Context(), pagepublic.GetPageQuery{Slug: "/"})
	require.NoError(t, err)
	require.False(t, read.Visible, "a homepage that no longer exists reads as none")
}

func requireNotFound(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
	require.Equal(t, "page not found", errs.Wrap(err).Message())
}

func text(value string) *string { return &value }
