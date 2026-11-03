package public_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	public "github.com/echovisionlab/geul-api/internal/filemedia/public"
	"github.com/echovisionlab/geul-api/internal/filemedia/public/publictest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type publicAuthorization struct{ allowed bool }

func (a publicAuthorization) Can(context.Context, policyv1.AuthorizationDecision) (bool, error) {
	return a.allowed, nil
}

type publicReadPhases struct {
	mutation.UnitOfWork[public.Store]
	reads           int
	afterEvaluation func()
}

func (u *publicReadPhases) ReadLocked(ctx context.Context, read func(public.Store) error) error {
	u.reads++
	err := u.UnitOfWork.ReadLocked(ctx, read)
	if err == nil && u.reads == 1 && u.afterEvaluation != nil {
		u.afterEvaluation()
	}
	return err
}

type publicFixture struct {
	store    *publictest.Store
	units    *publicReadPhases
	service  *public.Service
	query    public.AuthorizeDownloadQuery
	owner    public.OwnerWitness
	relation publictest.Relation
	file     publictest.FileRecord
	ctx      context.Context
	user     *auth.UserInfo
}

func newPublicFixture(t *testing.T) *publicFixture {
	t.Helper()
	store := publictest.NewStore()
	h := publictest.FakeHarness(store)
	file := publictest.File()
	owner := publictest.Owner("post")
	h.SeedFile(t, file)
	h.SeedOwner(t, owner)
	relation := publictest.Relation{BlockID: uuid.NewString(), ReferencePath: "file", FileID: file.ID, Kind: "file", Owner: owner, Access: mediaasset.FileDownloadAccessPublic}
	h.SeedRelation(t, relation)
	units := &publicReadPhases{UnitOfWork: h.Units}
	service, err := public.NewService(public.Deps{Units: units, SpiceDB: publicAuthorization{allowed: true}, CDNDomain: "https://cdn.example.test", MediaDomain: "https://media.example.test", MediaSecret: "unit-media-secret", DownloadTTL: mediaauth.DownloadTTL})
	require.NoError(t, err)
	user := &auth.UserInfo{SessionID: auth.SessionID(uuid.NewString()), IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()), Authenticated: true, Onboarded: true}
	store.Members[user.MemberID.String()] = mediaasset.DownloadMemberFacts{MemberID: user.MemberID.String(), IdentityID: user.IdentityID.String(), Onboarded: true, CreatedAt: time.Now().Add(-time.Hour)}
	store.ActivePrincipals[user.IdentityID.String()+":"+user.MemberID.String()] = true
	return &publicFixture{store: store, units: units, service: service, query: public.AuthorizeDownloadQuery{EntityType: public.EntityPost, EntityID: owner.ResourceID, ContentBlock: &public.ContentBlockFileSelector{BlockID: relation.BlockID, ReferencePath: "file"}}, owner: owner, relation: relation, file: file, ctx: context.Background(), user: user}
}
func (f *publicFixture) member() { f.ctx = auth.WithUser(f.ctx, f.user) }
func (f *publicFixture) restricted() {
	f.store.Relations[0].Access = mediaasset.FileDownloadAccessRestricted
	id := uuid.NewString()
	f.store.CriteriaRows[id] = mediaasset.DownloadCriteria{ID: id, Kind: mediaasset.DownloadAllMembers}
	f.store.Links = []mediaasset.DownloadAssociation{{PolicyKey: mediaasset.ContentBlockDownloadPolicyKey(f.relation.BlockID, "file"), MemberCriteriaID: id}}
}
func TestPublicDownloadAccessDecisionsOnStore(t *testing.T) {
	cases := map[string]struct {
		setup  func(*publicFixture)
		action public.DownloadAction
	}{
		"public anonymous":        {action: public.ActionDownload},
		"disabled":                {setup: func(f *publicFixture) { f.store.Relations[0].Access = mediaasset.FileDownloadAccessDisabled }, action: public.ActionNone},
		"authenticated anonymous": {setup: func(f *publicFixture) { f.store.Relations[0].Access = mediaasset.FileDownloadAccessAuthenticated }, action: public.ActionSignIn},
		"authenticated active member": {setup: func(f *publicFixture) {
			f.member()
			f.store.Relations[0].Access = mediaasset.FileDownloadAccessAuthenticated
		}, action: public.ActionDownload},
		"authenticated inactive member": {setup: func(f *publicFixture) {
			f.member()
			f.store.Relations[0].Access = mediaasset.FileDownloadAccessAuthenticated
			delete(f.store.Members, f.user.MemberID.String())
		}, action: public.ActionNone},
		"authenticated banned member": {setup: func(f *publicFixture) {
			f.user.Banned = true
			f.member()
			f.store.Relations[0].Access = mediaasset.FileDownloadAccessAuthenticated
		}, action: public.ActionNone},
		"restricted anonymous with criteria": {setup: func(f *publicFixture) { f.restricted() }, action: public.ActionSignIn},
		"restricted active member":           {setup: func(f *publicFixture) { f.member(); f.restricted() }, action: public.ActionDownload},
		"restricted no criteria":             {setup: func(f *publicFixture) { f.store.Relations[0].Access = mediaasset.FileDownloadAccessRestricted }, action: public.ActionNone},
		"restricted archived criteria": {setup: func(f *publicFixture) {
			f.member()
			f.restricted()
			for id, row := range f.store.CriteriaRows {
				now := time.Now()
				row.ArchivedAt = &now
				f.store.CriteriaRows[id] = row
			}
		}, action: public.ActionNone},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newPublicFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			result, err := f.service.AuthorizeDownload(f.ctx, f.query)
			require.NoError(t, err)
			require.Equal(t, tc.action, result.Access.Action)
			if tc.action == public.ActionDownload {
				require.NotNil(t, result.Download)
				require.Equal(t, f.file.ID, result.Download.FileID)
				require.Equal(t, mediaauth.PurposeDownload, result.Download.Purpose)
				require.WithinDuration(t, time.Now().Add(mediaauth.DownloadTTL), result.Download.ExpiresAt, 2*time.Second)
			} else {
				require.Nil(t, result.Download)
			}
		})
	}
}
func TestPublicDownloadRejectsChangesBetweenPasses(t *testing.T) {
	for name, change := range map[string]func(*publicFixture){
		"replacement": func(f *publicFixture) {
			other := publictest.File()
			f.store.Files[other.ID] = other
			f.store.Relations[0].FileID = other.ID
		},
		"detachment": func(f *publicFixture) { f.store.Relations = nil },
		"owner status": func(f *publicFixture) {
			row := f.owner
			row.Status = "POST_STATUS_DRAFT"
			f.store.OwnerRows["post:"+row.ResourceID] = row
		},
		"deletion request": func(f *publicFixture) {
			row := f.store.Files[f.file.ID]
			row.PendingDeletion = true
			f.store.Files[f.file.ID] = row
		},
		"download policy":     func(f *publicFixture) { f.store.Relations[0].Access = mediaasset.FileDownloadAccessDisabled },
		"member deactivation": func(f *publicFixture) { delete(f.store.Members, f.user.MemberID.String()) },
		"criteria revocation": func(f *publicFixture) { f.store.Links = nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newPublicFixture(t)
			f.member()
			f.restricted()
			f.units.afterEvaluation = func() { change(f) }
			result, err := f.service.AuthorizeDownload(f.ctx, f.query)
			require.NoError(t, err)
			require.Equal(t, public.ActionNone, result.Access.Action)
			require.Nil(t, result.Download)
			require.Equal(t, 2, f.units.reads)
		})
	}
}
func TestPublicDownloadPreservesLockOrder(t *testing.T) {
	f := newPublicFixture(t)
	f.member()
	f.restricted()
	result, err := f.service.AuthorizeDownload(f.ctx, f.query)
	require.NoError(t, err)
	require.NotNil(t, result.Download)
	require.Equal(t, []string{"criteria", "owner", "viewer", "relation"}, f.store.Locks)
}
func TestPublicDraftDownloadRequiresCurrentPrincipalAndCapsLifetime(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "active", false: "revoked"}[active], func(t *testing.T) {
			f := newPublicFixture(t)
			f.member()
			row := f.owner
			row.Status = "POST_STATUS_DRAFT"
			f.store.OwnerRows["post:"+row.ResourceID] = row
			f.units.afterEvaluation = func() { f.store.ActivePrincipals[f.user.IdentityID.String()+":"+f.user.MemberID.String()] = active }
			result, err := f.service.AuthorizeDownload(f.ctx, f.query)
			require.NoError(t, err)
			if active {
				require.NotNil(t, result.Download)
				require.WithinDuration(t, time.Now().Add(5*time.Minute), result.Download.ExpiresAt, 2*time.Second)
			} else {
				require.Nil(t, result.Download)
				require.Equal(t, public.ActionNone, result.Access.Action)
			}
			require.Contains(t, f.store.Locks, "principal")
		})
	}
}
func TestPublicHydrationKeepsExactRelationPolicyAndIndependentDeliveries(t *testing.T) {
	f := newPublicFixture(t)
	second := f.relation
	second.BlockID = uuid.NewString()
	second.Access = mediaasset.FileDownloadAccessDisabled
	f.store.Relations = append(f.store.Relations, second)
	f.store.BlockOwners[second.BlockID] = []public.OwnerWitness{f.owner}
	ctx := mediaasset.WithContentDownloadOwnerAuthorization(f.ctx, mediaasset.ContentDownloadOwnerAuthorization{ResourceType: f.owner.ResourceType, ResourceID: f.owner.ResourceID, DocumentID: f.owner.DocumentID, Status: f.owner.Status, Mode: mediaasset.ContentDownloadOwnerAccessPublic})
	items := []*public.ContentMediaItem{{FileID: f.file.ID, Selector: f.query.ContentBlock}, {FileID: f.file.ID, Selector: &public.ContentBlockFileSelector{BlockID: second.BlockID, ReferencePath: "file"}}}
	result, err := f.service.HydrateContentMedia(ctx, items)
	require.NoError(t, err)
	require.Equal(t, public.ActionDownload, result[0].DownloadAction)
	require.Equal(t, public.ActionNone, result[1].DownloadAction)
	require.NotNil(t, result[0].Delivery.Download)
	require.Nil(t, result[1].Delivery.Download)
	require.NotNil(t, result[1].Delivery.Inline)
	require.NotSame(t, result[0].Delivery, result[1].Delivery)
	require.NotSame(t, result[0].Delivery.Inline, result[1].Delivery.Inline)
}
func TestPublicHydrationRequiresStableOwnerWitness(t *testing.T) {
	for _, revoke := range []string{"missing", "owner", "relation"} {
		t.Run(revoke, func(t *testing.T) {
			f := newPublicFixture(t)
			ctx := f.ctx
			if revoke != "missing" {
				ctx = mediaasset.WithContentDownloadOwnerAuthorization(ctx, mediaasset.ContentDownloadOwnerAuthorization{ResourceType: f.owner.ResourceType, ResourceID: f.owner.ResourceID, DocumentID: f.owner.DocumentID, Status: f.owner.Status, Mode: mediaasset.ContentDownloadOwnerAccessPublic})
			}
			f.units.afterEvaluation = func() {
				switch revoke {
				case "owner":
					row := f.owner
					row.DocumentID = uuid.NewString()
					f.store.OwnerRows["post:"+row.ResourceID] = row
				case "relation":
					f.store.Relations = nil
				}
			}
			items, err := f.service.HydrateContentMedia(ctx, []*public.ContentMediaItem{{FileID: f.file.ID, Selector: f.query.ContentBlock}})
			require.NoError(t, err)
			require.Nil(t, items[0].Delivery)
			require.Equal(t, public.ActionNone, items[0].DownloadAction)
		})
	}
}
func TestPublicDisplayUsesOnlyReadyImageAssets(t *testing.T) {
	f := newPublicFixture(t)
	asset := publictest.Asset(f.file.ID, "image", "ready", time.Now())
	f.store.Assets[asset.AssetID] = asset
	result, err := f.service.ResolveDisplayMedia(f.ctx, []string{"", f.file.ID, f.file.ID})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, asset.AssetID, result[f.file.ID].Asset.AssetID)
	require.Nil(t, result[f.file.ID].Inline)
	require.Nil(t, result[f.file.ID].Download)
	asset.Status = "failed"
	f.store.Assets[asset.AssetID] = asset
	result, err = f.service.ResolveDisplayMedia(f.ctx, []string{f.file.ID})
	require.NoError(t, err)
	require.Empty(t, result)
}
func TestPublicOGAssetFallbackAndReadFailure(t *testing.T) {
	f := newPublicFixture(t)
	source := publictest.Asset(f.file.ID, "og", "ready", time.Now())
	localized := publictest.Asset(f.file.ID, "og", "ready", time.Now())
	f.store.Assets[source.AssetID] = source
	f.store.Assets[localized.AssetID] = localized
	result, err := f.service.ResolveOGAsset(f.ctx, &source.AssetID, &localized.AssetID)
	require.NoError(t, err)
	require.Equal(t, localized.AssetID, result.AssetID)
	delete(f.store.Assets, localized.AssetID)
	result, err = f.service.ResolveOGAsset(f.ctx, &source.AssetID, &localized.AssetID)
	require.NoError(t, err)
	require.Equal(t, source.AssetID, result.AssetID)
	failure := errors.New("read failed")
	f.store.Fail = failure
	_, err = f.service.ResolveOGAsset(f.ctx, &source.AssetID, &localized.AssetID)
	require.ErrorIs(t, err, failure)
}
