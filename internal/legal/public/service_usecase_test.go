package public_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/legaltest"
	"github.com/echovisionlab/geul-api/internal/legal/public"
)

type publicHarness struct {
	store    *legaltest.Store
	reads    legaltest.PublicStore
	ogImages legaltest.OGImages
	service  *public.Service
}

func newPublicHarness(t *testing.T) *publicHarness {
	t.Helper()
	store, reads, units := legaltest.NewPublic()
	h := &publicHarness{
		store: store, reads: reads,
		ogImages: legaltest.OGImages{Dispositions: map[string]public.OGDisposition{}, Images: map[string]*public.Asset{}},
	}
	var err error
	h.service, err = public.NewService(public.Deps{Units: units, Localizer: legaltest.Localizer{Store: store}, OGImages: h.ogImages})
	require.NoError(t, err)
	return h
}

func status(kind legaldomain.PolicyKind, state string) legaldomain.Status {
	stored, _ := legaldomain.PolicyStatus(kind, state)
	return stored
}

func (h *publicHarness) put(kind legaldomain.PolicyKind, state string, effectiveFrom, effectiveUntil *time.Time) legaldomain.Version {
	return h.store.PutVersion(kind, legaldomain.Version{
		Status: status(kind, state), EffectiveFrom: effectiveFrom, EffectiveUntil: effectiveUntil,
	}, "en")
}

func at(offset time.Duration) *time.Time {
	value := time.Now().UTC().Add(offset)
	return &value
}

func TestGetPolicyServesTheEffectiveVersionAndAnnouncesTheNextOne(t *testing.T) {
	for _, kind := range []legaldomain.PolicyKind{legaldomain.Terms, legaldomain.Privacy} {
		t.Run(string(kind), func(t *testing.T) {
			h := newPublicHarness(t)
			h.put(kind, "archived", at(-48*time.Hour), at(-time.Hour))
			current := h.put(kind, "active", at(-time.Hour), nil)
			next := h.put(kind, "scheduled", at(time.Hour), nil)
			h.put(kind, "draft", nil, nil)
			image := &public.Asset{ID: "og", URL: "https://cdn.example.test/og.webp", Disposition: public.DispositionInline}
			h.ogImages.Images[string(kind)+"/en"] = image

			policies, err := h.service.GetPolicy(t.Context(), kind, public.GetPolicyQuery{Now: time.Now().UTC()})
			require.NoError(t, err)
			require.Equal(t, current.ID, policies.Current.ID)
			require.Equal(t, current.Title, policies.Current.Title)
			require.Equal(t, image, policies.Current.OGAsset)
			require.Equal(t, "en", policies.Current.Localization.DisplayedLocale)
			require.Equal(t, public.PublicPolicy{
				ID: next.ID, Version: int32(next.Version), Title: next.Title, EffectiveFrom: next.EffectiveFrom,
			}, *policies.Scheduled, "an announced version carries only its identity, title and dates")

			h.store.Fail = map[string]error{"NextScheduledVersion": errors.New("lookup failed")}
			policies, err = h.service.GetPolicy(t.Context(), kind, public.GetPolicyQuery{Now: time.Now().UTC()})
			require.NoError(t, err, "a failed scheduled lookup serves the current version alone")
			require.Equal(t, current.ID, policies.Current.ID)
			require.Nil(t, policies.Scheduled)
		})
	}
}

func TestGetPolicyByIDServesOnlyPublishedHistory(t *testing.T) {
	h := newPublicHarness(t)
	archived := h.put(legaldomain.Privacy, "archived", at(-48*time.Hour), at(-time.Hour))
	draft := h.put(legaldomain.Privacy, "draft", nil, nil)
	scheduled := h.put(legaldomain.Privacy, "scheduled", at(time.Hour), nil)

	policies, err := h.service.GetPolicy(t.Context(), legaldomain.Privacy, public.GetPolicyQuery{ID: archived.ID, Now: time.Now()})
	require.NoError(t, err)
	require.Equal(t, archived.ID, policies.Current.ID)
	for _, hidden := range []string{draft.ID, scheduled.ID, uuid.NewString()} {
		policies, err = h.service.GetPolicy(t.Context(), legaldomain.Privacy, public.GetPolicyQuery{ID: hidden, Now: time.Now()})
		require.NoError(t, err)
		require.Equal(t, public.PublicPolicies{}, policies)
	}
}

func TestGetPolicyOpensAShareLinkOnlyForItsExactVersion(t *testing.T) {
	for _, kind := range []legaldomain.PolicyKind{legaldomain.Terms, legaldomain.Privacy} {
		t.Run(string(kind), func(t *testing.T) {
			h := newPublicHarness(t)
			shareType, _ := kind.ShareLinkEntityType()
			now := time.Now().UTC()
			archived := h.put(kind, "archived", at(-48*time.Hour), at(-time.Hour))
			scheduled := h.put(kind, "scheduled", at(time.Hour), nil)
			draft := h.put(kind, "draft", nil, nil)
			for _, version := range []legaldomain.Version{archived, scheduled, draft} {
				h.reads.PutShareLink(legaltest.ShareLink{Token: "token-" + version.ID, EntityType: shareType, EntityID: version.ID, ExpiresAt: now.Add(time.Hour)})
			}
			h.reads.PutShareLink(legaltest.ShareLink{Token: "protected", EntityType: shareType, EntityID: scheduled.ID, ExpiresAt: now.Add(time.Hour), Password: "secret"})
			h.reads.PutShareLink(legaltest.ShareLink{Token: "expired", EntityType: shareType, EntityID: archived.ID, ExpiresAt: now.Add(-time.Minute)})
			get := func(id, token, password string) public.PublicPolicies {
				t.Helper()
				policies, err := h.service.GetPolicy(t.Context(), kind, public.GetPolicyQuery{ID: id, ShareToken: token, SharePassword: password, Now: now})
				require.NoError(t, err)
				return policies
			}

			preview := get(scheduled.ID, "token-"+scheduled.ID, "")
			require.Nil(t, preview.Current)
			require.Equal(t, scheduled.ID, preview.Scheduled.ID, "a scheduled version opens as the scheduled one")
			require.NotNil(t, preview.Scheduled.Document)
			history := get(archived.ID, "token-"+archived.ID, "")
			require.Equal(t, archived.ID, history.Current.ID, "a published version opens as public history")
			require.Nil(t, history.Scheduled)

			require.Equal(t, public.PublicPolicies{}, get(archived.ID, "token-"+scheduled.ID, ""), "a token opens only its own version")
			require.Equal(t, public.PublicPolicies{}, get("", "token-"+scheduled.ID, ""))
			require.Equal(t, public.PublicPolicies{}, get(draft.ID, "token-"+draft.ID, ""))
			require.Equal(t, public.PublicPolicies{}, get(archived.ID, "expired", ""))
			require.Equal(t, public.PublicPolicies{}, get(scheduled.ID, "protected", ""))
			require.Equal(t, public.PublicPolicies{}, get(scheduled.ID, "protected", "wrong"))
			require.Equal(t, scheduled.ID, get(scheduled.ID, "protected", "secret").Scheduled.ID)

			passed := scheduled
			passed.EffectiveFrom = at(-time.Minute)
			h.store.PutVersion(kind, passed, "en")
			require.Equal(t, public.PublicPolicies{}, get(scheduled.ID, "token-"+scheduled.ID, ""), "a schedule that took effect is not a preview")
		})
	}
}

func TestGetPolicyFallsBackToTheSourceLocaleUntilItsOGImageIsReady(t *testing.T) {
	h := newPublicHarness(t)
	active := h.put(legaldomain.Terms, "active", at(-time.Hour), nil)
	h.store.PutTranslation(legaldomain.Terms, active.ID, legaldomain.Translation{Locale: "ko", Title: ptr("한국어 약관")})
	source := &public.Asset{ID: "source"}
	h.ogImages.Images["terms/en"] = source
	query := public.GetPolicyQuery{AcceptLanguage: "ko", Now: time.Now()}

	h.ogImages.Dispositions["terms/ko"] = public.OGPending
	policies, err := h.service.GetPolicy(t.Context(), legaldomain.Terms, query)
	require.NoError(t, err)
	require.Equal(t, "ko", policies.Current.Localization.DisplayedLocale, "a pending image keeps the translation")
	require.Equal(t, "한국어 약관", policies.Current.Title)
	require.Nil(t, policies.Current.OGAsset)

	translated := &public.Asset{ID: "ko"}
	h.ogImages.Dispositions["terms/ko"] = public.OGReady
	h.ogImages.Images["terms/ko"] = translated
	policies, err = h.service.GetPolicy(t.Context(), legaldomain.Terms, query)
	require.NoError(t, err)
	require.Equal(t, translated, policies.Current.OGAsset)

	h.ogImages.Dispositions["terms/ko"] = public.OGUnavailable
	policies, err = h.service.GetPolicy(t.Context(), legaldomain.Terms, query)
	require.NoError(t, err)
	require.Equal(t, "en", policies.Current.Localization.DisplayedLocale, "a failed image falls back to the source locale")
	require.Equal(t, active.Title, policies.Current.Title)
	require.Equal(t, source, policies.Current.OGAsset)
	require.True(t, policies.Current.Localization.IsFallback)
}

func TestGetPolicyAnswersNotFoundForAVersionWithoutItsDocument(t *testing.T) {
	h := newPublicHarness(t)
	active := h.put(legaldomain.Privacy, "active", at(-time.Hour), nil)
	documentID := uuid.MustParse(*active.ContentDocumentID)
	require.NoError(t, h.store.DeleteContentDocument(t.Context(), documentID, func(_ context.Context, _ legaldomain.Store, _ uuid.UUID) (contentblock.DomainContext, error) {
		return contentblock.DomainContext{}, nil
	}))
	_, err := h.service.GetPolicy(t.Context(), legaldomain.Privacy, public.GetPolicyQuery{ID: active.ID, Now: time.Now()})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
	require.Equal(t, "privacy not found: "+active.ID, errs.Wrap(err).Message())
}

func TestListArchivedPoliciesPagesLatestFirst(t *testing.T) {
	h := newPublicHarness(t)
	older := h.put(legaldomain.Terms, "archived", at(-72*time.Hour), at(-48*time.Hour))
	newer := h.put(legaldomain.Terms, "archived", at(-48*time.Hour), at(-time.Hour))
	h.put(legaldomain.Terms, "active", at(-time.Hour), nil)

	page, err := h.service.ListArchivedPolicies(t.Context(), legaldomain.Terms, public.ListArchivedPoliciesQuery{Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Equal(t, []public.PolicySummary{{
		ID: newer.ID, Version: int32(newer.Version), Title: newer.Title, EffectiveFrom: newer.EffectiveFrom, EffectiveUntil: newer.EffectiveUntil,
	}}, page.Items)
	page, err = h.service.ListArchivedPolicies(t.Context(), legaldomain.Terms, public.ListArchivedPoliciesQuery{Offset: 1})
	require.NoError(t, err)
	require.Equal(t, older.ID, page.Items[0].ID)

	h.store.Fail = map[string]error{"CountArchived": errors.New("count failed")}
	page, err = h.service.ListArchivedPolicies(t.Context(), legaldomain.Terms, public.ListArchivedPoliciesQuery{})
	require.NoError(t, err, "a failed count reports a total of zero")
	require.Zero(t, page.Total)
	require.Len(t, page.Items, 2)
}

func ptr(value string) *string { return &value }
