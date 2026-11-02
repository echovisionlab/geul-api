package member_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/mutation"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestNewServiceRequiresEveryCollaborator(t *testing.T) {
	_, err := member.NewService(member.Deps{})
	require.EqualError(t, err, "member dependencies are required: Accounts, Audit, AvatarFiles, Fence, Identity, SpiceDB, Units")
}

func TestOwnProfileAuditRecordsOnlyApprovedFieldsAndSkipsNoOp(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	nickname, bio, website := "after", "approved biography", "https://profile.example.test"
	command := member.UpdateOwnProfileCommand{Nickname: &nickname, Bio: &bio, Website: &website, SocialLinks: map[string]string{"site": website}}

	summary, err := f.service.UpdateOwnProfile(f.ctx(t), command)
	require.NoError(t, err)
	require.Equal(t, nickname, summary.Nickname)
	records := f.memberUpdates()
	require.Len(t, records, 1)
	require.Equal(t, []string{"bio", "nickname", "social_links", "website"}, records[0].ChangedFields)
	require.Equal(t, nickname, records[0].Nickname)
	require.Equal(t, f.memberID, records[0].TargetID)
	stored, _ := f.store.Member(f.memberID)
	require.Equal(t, &bio, stored.Bio)
	require.Equal(t, map[string]string{"site": website}, stored.SocialLinks)

	_, err = f.service.UpdateOwnProfile(f.ctx(t), command)
	require.NoError(t, err)
	require.Len(t, f.memberUpdates(), 1)

	invalid := " "
	_, err = f.service.UpdateOwnProfile(f.ctx(t), member.UpdateOwnProfileCommand{Nickname: &invalid})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Len(t, f.memberUpdates(), 1)
}

func TestOwnProfileAuditFailureRollsBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	nickname := "must-not-commit"

	_, err := f.newService(t, &audittest.Recorder{Err: errAuditUnavailable}).UpdateOwnProfile(f.ctx(t), member.UpdateOwnProfileCommand{Nickname: &nickname})

	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	stored, _ := f.store.Member(f.memberID)
	require.Equal(t, "principal", stored.Nickname)
	require.Equal(t, mutation.StepRollback, f.units.Steps()[len(f.units.Steps())-1])
}

func TestProfileNicknameHeldByAnotherMemberAnswersAlreadyExists(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedMember("taken")
	nickname := " taken "

	_, err := f.service.UpdateOwnProfile(f.ctx(t), member.UpdateOwnProfileCommand{Nickname: &nickname})

	require.Equal(t, connect.CodeAlreadyExists, errs.Wrap(err).Code())
	require.ErrorContains(t, err, "member with nickname 'taken' already exists")
	require.Empty(t, f.memberUpdates())
}

func TestSelfExtendedProfileMutationRequiresAuthorRole(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.checker.Deny(policyv1.Platform.IsAuthor())
	bio := "author-only biography"

	_, err := f.service.UpdateOwnProfile(f.ctx(t), member.UpdateOwnProfileCommand{Bio: &bio})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.ErrorContains(t, err, "extended profile fields require Author or Admin role")

	nickname := "plain"
	_, err = f.service.UpdateOwnProfile(f.ctx(t), member.UpdateOwnProfileCommand{Nickname: &nickname})
	require.NoError(t, err)

	profile, err := f.service.OwnProfile(f.ctx(t))
	require.NoError(t, err)
	require.Nil(t, profile.Bio)
	require.Nil(t, profile.SocialLinks)
}

func TestAdminProfileUpdateRequiresFreshAdministratorAuthority(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	targetID, _ := f.seedMember("target")
	nickname := "renamed"
	f.fence.Deactivate(f.identityID)

	_, err := f.service.UpdateMemberProfile(f.ctx(t), member.UpdateMemberProfileCommand{MemberID: targetID, Nickname: &nickname})

	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	stored, _ := f.store.Member(targetID)
	require.Equal(t, "target", stored.Nickname)
	require.Empty(t, f.memberUpdates())
}

func TestAdminMutationsKeepAnUnonboardedMemberUnonboarded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	targetID, targetIdentityID := f.seedMember("pending")
	pending, _ := f.store.Member(targetID)
	pending.Onboarded = false
	f.store.SeedMember(pending)
	tagID := uuid.NewString()
	f.store.SeedTag(member.Tag{ID: tagID, Name: "patrons"})
	fileID, asset := uuid.NewString(), &memberref.Asset{ID: uuid.NewString(), URL: "https://cdn.example.test/avatar.webp"}
	f.store.SeedAvatarSource(fileID, uuid.NewString())
	f.avatars.assets[fileID] = asset
	bio := "set by an administrator"

	view, err := f.service.UpdateMemberProfile(f.ctx(t), member.UpdateMemberProfileCommand{MemberID: targetID, Bio: &bio})
	require.NoError(t, err)
	require.False(t, view.Onboarded)
	summary, err := f.service.SetMemberAvatar(f.ctx(t), member.SetMemberAvatarCommand{MemberID: targetID, FileID: fileID})
	require.NoError(t, err)
	require.Equal(t, asset, summary.Avatar)
	view, err = f.service.SetMemberTags(f.ctx(t), member.SetMemberTagsCommand{MemberID: targetID, TagIDs: []string{tagID}})
	require.NoError(t, err)
	require.Equal(t, []string{tagID}, view.TagIDs)
	require.False(t, view.Onboarded)

	unknown := uuid.NewString()
	_, err = f.service.SetMemberTags(f.ctx(t), member.SetMemberTagsCommand{MemberID: targetID, TagIDs: []string{unknown}})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.ErrorContains(t, err, "contains an unknown member tag")

	stored, _ := f.store.Member(targetID)
	require.False(t, stored.Onboarded)
	require.Equal(t, &targetIdentityID, stored.AccountIdentityID)
	require.Equal(t, []string{tagID}, f.store.MemberTags(targetID))
}

func TestPreferencesAndTagsAuditRecordExactAttributes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	locale := "ko"
	settings, err := f.service.UpdatePreferences(f.ctx(t), member.UpdatePreferencesCommand{
		PreferredLocale: &locale, CookieConsent: &member.CookieConsentInput{Analytics: true, Version: 2},
	})
	require.NoError(t, err)
	require.Equal(t, &locale, settings.PreferredLocale)
	require.NotNil(t, settings.CookieConsent)
	require.Equal(t, "settings", settings.CookieConsent.Source)
	records := f.memberUpdates()
	require.Len(t, records, 1)
	require.Equal(t, []string{"cookie_consent", "preferred_locale"}, records[0].ChangedFields)
	require.Equal(t, "ko", records[0].PreferredLocale)
	consents := f.store.Consents()
	require.Len(t, consents, 1)
	require.Equal(t, consents[0].ID, records[0].ConsentID)
	require.Equal(t, f.memberID, consents[0].MemberID)
	require.True(t, consents[0].Analytics)
	require.Equal(t, int32(2), consents[0].Version)

	_, err = f.service.UpdatePreferences(f.ctx(t), member.UpdatePreferencesCommand{PreferredLocale: &locale})
	require.NoError(t, err)
	require.Len(t, f.memberUpdates(), 1)

	firstTag, secondTag := uuid.NewString(), uuid.NewString()
	f.store.SeedTag(member.Tag{ID: firstTag, Name: "first"})
	f.store.SeedTag(member.Tag{ID: secondTag, Name: "second"})
	targetID, _ := f.seedMember("tagged")
	_, err = f.service.SetMemberTags(f.ctx(t), member.SetMemberTagsCommand{MemberID: targetID, TagIDs: []string{secondTag, firstTag, secondTag}})
	require.NoError(t, err)
	records = f.memberUpdates()
	require.Len(t, records, 2)
	require.Equal(t, []string{"tags"}, records[1].ChangedFields)
	expected := []string{firstTag, secondTag}
	if secondTag < firstTag {
		expected = []string{secondTag, firstTag}
	}
	require.Equal(t, &expected, records[1].TagIDs)

	_, err = f.service.SetMemberTags(f.ctx(t), member.SetMemberTagsCommand{MemberID: targetID, TagIDs: []string{}})
	require.NoError(t, err)
	records = f.memberUpdates()
	require.Equal(t, &[]string{}, records[2].TagIDs)
	_, err = f.service.SetMemberTags(f.ctx(t), member.SetMemberTagsCommand{MemberID: targetID, TagIDs: []string{}})
	require.NoError(t, err)
	require.Len(t, f.memberUpdates(), 3)
}

func TestPreferencesAuditFailureRollsBackLocaleAndConsent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	locale := "ko"

	_, err := f.newService(t, &audittest.Recorder{Err: errAuditUnavailable}).UpdatePreferences(f.ctx(t), member.UpdatePreferencesCommand{
		PreferredLocale: &locale, CookieConsent: &member.CookieConsentInput{Analytics: true, Version: 2},
	})

	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	stored, _ := f.store.Member(f.memberID)
	require.Nil(t, stored.PreferredLocale)
	require.Empty(t, f.store.Consents())
}

func TestCheckNicknameAvailabilityIgnoresTheMemberItselfAndDeletedMembers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	formerID, _ := f.seedMember("former")
	former, _ := f.store.Member(formerID)
	deletedAt := time.Now()
	former.DeletedAt = &deletedAt
	f.store.SeedMember(former)
	f.seedMember("held")

	for nickname, want := range map[string]bool{"principal": true, "former": true, "held": false, " ": false} {
		available, err := f.service.CheckNicknameAvailability(f.ctx(t), member.CheckNicknameAvailabilityQuery{Nickname: nickname})
		if nickname == " " {
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
			continue
		}
		require.NoError(t, err)
		require.Equal(t, want, available, nickname)
	}
}

func TestAvatarAuditRollsBackAndSkipsNoOp(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	fileID, asset := uuid.NewString(), &memberref.Asset{ID: uuid.NewString()}
	f.store.SeedAvatarSource(fileID, f.memberID)
	f.avatars.assets[fileID] = asset

	_, err := f.newService(t, &audittest.Recorder{Err: errAuditUnavailable}).SetOwnAvatar(f.ctx(t), member.SetOwnAvatarCommand{FileID: fileID})
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	_, bound := f.store.AvatarBinding(f.memberID)
	require.False(t, bound)

	summary, err := f.service.SetOwnAvatar(f.ctx(t), member.SetOwnAvatarCommand{FileID: fileID})
	require.NoError(t, err)
	require.Equal(t, asset, summary.Avatar)
	_, err = f.service.SetOwnAvatar(f.ctx(t), member.SetOwnAvatarCommand{FileID: fileID})
	require.NoError(t, err)
	records := f.memberUpdates()
	require.Len(t, records, 1)
	require.Equal(t, asset.ID, records[0].AssetID)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationAdded, records[0].CollectionOperation)

	_, err = f.service.DeleteOwnAvatar(f.ctx(t))
	require.NoError(t, err)
	_, err = f.service.DeleteOwnAvatar(f.ctx(t))
	require.NoError(t, err)
	records = f.memberUpdates()
	require.Len(t, records, 2)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationRemoved, records[1].CollectionOperation)
}

func TestOwnAvatarRejectsAnotherMembersUpload(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	fileID := uuid.NewString()
	f.store.SeedAvatarSource(fileID, uuid.NewString())

	_, err := f.service.SetOwnAvatar(f.ctx(t), member.SetOwnAvatarCommand{FileID: fileID})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.ErrorContains(t, err, "avatar source does not belong to this member")

	_, err = f.service.SetOwnAvatar(f.ctx(t), member.SetOwnAvatarCommand{FileID: "not-a-uuid"})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
}

func TestAvatarMutationDistinguishesMissingMemberFromProjectionFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.service.DeleteMemberAvatar(f.ctx(t), member.DeleteMemberAvatarCommand{MemberID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())

	f.store.Fail("Members", errAuditUnavailable)
	_, err = f.service.DeleteOwnAvatar(f.ctx(t))
	require.ErrorIs(t, err, errAuditUnavailable)
	require.ErrorContains(t, err, "load member after avatar mutation")
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
}
