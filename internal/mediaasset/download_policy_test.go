package mediaasset_test

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaasset/mediaassettest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func downloadUser(id string) *auth.UserInfo {
	return &auth.UserInfo{IdentityID: auth.IdentityID(id), MemberID: auth.MemberID(id), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true}
}
func downloadSource(id string, access mediaasset.FileDownloadAccess) mediaasset.FileDownloadSource {
	return mediaasset.FileDownloadSource{PolicyKey: id, PolicyKind: mediaasset.FileDownloadPolicyTrack, TrackID: id, FileID: id, Access: access}
}
func TestDownloadAccessMatrixHasNoImplicitAdministratorBypass(t *testing.T) {
	store := mediaassettest.NewDownload()
	now := time.Now().UTC()
	user, administrator, source, tag, criteria := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	store.Members[user] = mediaasset.DownloadMemberFacts{MemberID: user, IdentityID: user, Onboarded: true, CreatedAt: now}
	store.Members[administrator] = mediaasset.DownloadMemberFacts{MemberID: administrator, IdentityID: administrator, Onboarded: true, CreatedAt: now}
	store.Tags[tag] = map[string]bool{user: true}
	store.CriteriaRows[criteria] = mediaasset.DownloadCriteria{ID: criteria, Kind: mediaasset.DownloadMemberTags, Config: mediaasset.DownloadCriteriaConfig{MemberTagIDs: []string{tag}}}
	store.Links = []mediaasset.DownloadAssociation{{PolicyKey: source, MemberCriteriaID: criteria}}
	for _, tc := range []struct {
		name   string
		access mediaasset.FileDownloadAccess
		user   *auth.UserInfo
		want   bool
	}{
		{"disabled anonymous", mediaasset.FileDownloadAccessDisabled, nil, false}, {"public anonymous", mediaasset.FileDownloadAccessPublic, nil, true}, {"authenticated active user", mediaasset.FileDownloadAccessAuthenticated, downloadUser(user), true}, {"restricted anonymous", mediaasset.FileDownloadAccessRestricted, nil, false}, {"restricted matching member", mediaasset.FileDownloadAccessRestricted, downloadUser(user), true}, {"restricted administrator", mediaasset.FileDownloadAccessRestricted, downloadUser(administrator), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, err := mediaasset.EvaluateFileDownloadAccess(t.Context(), store, nil, downloadSource(source, tc.access), tc.user)
			require.NoError(t, err)
			require.Equal(t, tc.want, allowed)
		})
	}
	current := store.CriteriaRows[criteria]
	current.ArchivedAt = &now
	store.CriteriaRows[criteria] = current
	restricted := downloadSource(source, mediaasset.FileDownloadAccessRestricted)
	allowed, err := mediaasset.EvaluateFileDownloadAccess(t.Context(), store, nil, restricted, downloadUser(user))
	require.NoError(t, err)
	require.False(t, allowed)
	presence, err := mediaasset.RestrictedFileDownloadCriteriaPresence(t.Context(), store, map[string]mediaasset.FileDownloadSource{source: restricted})
	require.NoError(t, err)
	require.False(t, presence[source])
}
func TestRestrictedDownloadRejectsStaleOrMalformedTagConfigurations(t *testing.T) {
	user, source, criteria, tag := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, tc := range []struct {
		name     string
		config   mediaasset.DownloadCriteriaConfig
		existing bool
	}{{"stale tag", mediaasset.DownloadCriteriaConfig{MemberTagIDs: []string{tag}}, false}, {"malformed tag", mediaasset.DownloadCriteriaConfig{MemberTagIDs: []string{"invalid"}}, true}, {"mixed filter", mediaasset.DownloadCriteriaConfig{MemberTagIDs: []string{tag}, AccountRoles: []string{"user"}}, true}, {"no tags", mediaasset.DownloadCriteriaConfig{}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			store := mediaassettest.NewDownload()
			store.Members[user] = mediaasset.DownloadMemberFacts{MemberID: user, IdentityID: user, Onboarded: true}
			store.CriteriaRows[criteria] = mediaasset.DownloadCriteria{ID: criteria, Kind: mediaasset.DownloadMemberTags, Config: tc.config}
			store.Links = []mediaasset.DownloadAssociation{{PolicyKey: source, MemberCriteriaID: criteria}}
			if tc.existing {
				store.Tags[tag] = map[string]bool{user: true}
			}
			allowed, err := mediaasset.EvaluateFileDownloadAccess(t.Context(), store, nil, downloadSource(source, mediaasset.FileDownloadAccessRestricted), downloadUser(user))
			require.NoError(t, err)
			require.False(t, allowed)
		})
	}
}
func TestRestrictedDownloadEvaluatesRoleAndCreationWindowBeforeExclusion(t *testing.T) {
	store := mediaassettest.NewDownload()
	now := time.Now().UTC()
	user, source, criteria := uuid.NewString(), uuid.NewString(), uuid.NewString()
	viewer := downloadUser(user)
	store.Members[user] = mediaasset.DownloadMemberFacts{MemberID: user, IdentityID: user, Onboarded: true, CreatedAt: now.Add(-time.Hour)}
	after, before := now.Add(-2*time.Hour), now.Add(time.Hour)
	row := mediaasset.DownloadCriteria{ID: criteria, Kind: mediaasset.DownloadMembersByFilter, Config: mediaasset.DownloadCriteriaConfig{AccountRoles: []string{policyv1.Role.Author().ID()}, CreatedAfter: &after, CreatedBefore: &before}}
	store.CriteriaRows[criteria] = row
	store.Links = []mediaasset.DownloadAssociation{{PolicyKey: source, MemberCriteriaID: criteria}}
	checker := (&authztest.Checker{}).For(user).Allow(policyv1.Platform.IsAuthor())
	request := downloadSource(source, mediaasset.FileDownloadAccessRestricted)
	allowed, err := mediaasset.EvaluateFileDownloadAccess(t.Context(), store, checker, request, viewer)
	require.NoError(t, err)
	require.True(t, allowed)
	row.Config.ExcludeMemberIDs = []string{user}
	store.CriteriaRows[criteria] = row
	allowed, err = mediaasset.EvaluateFileDownloadAccess(t.Context(), store, checker, request, viewer)
	require.NoError(t, err)
	require.False(t, allowed)
	row.Config.ExcludeMemberIDs = nil
	row.Config.CreatedAfter = &now
	store.CriteriaRows[criteria] = row
	allowed, err = mediaasset.EvaluateFileDownloadAccess(t.Context(), store, checker, request, viewer)
	require.NoError(t, err)
	require.False(t, allowed)
}
func TestAuthenticatedDownloadRequiresTheExactActiveMemberWitness(t *testing.T) {
	store := mediaassettest.NewDownload()
	id := uuid.NewString()
	user := downloadUser(id)
	request := downloadSource(uuid.NewString(), mediaasset.FileDownloadAccessAuthenticated)
	allowed, err := mediaasset.EvaluateFileDownloadAccess(t.Context(), store, nil, request, user)
	require.NoError(t, err)
	require.False(t, allowed)
	store.Members[id] = mediaasset.DownloadMemberFacts{MemberID: id, IdentityID: uuid.NewString(), Onboarded: true}
	allowed, err = mediaasset.EvaluateFileDownloadAccess(t.Context(), store, nil, request, user)
	require.NoError(t, err)
	require.False(t, allowed)
	store.Members[id] = mediaasset.DownloadMemberFacts{MemberID: id, IdentityID: id, Onboarded: true}
	user.Banned = true
	allowed, err = mediaasset.EvaluateFileDownloadAccess(t.Context(), store, nil, request, user)
	require.NoError(t, err)
	require.False(t, allowed)
}
