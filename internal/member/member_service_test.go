//go:build integration

package member

import (
	"context"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

func TestMemberSummaryDeletedMemberNeverExposesProfilePII(t *testing.T) {
	nickname := "Former name"
	deletedAt := time.Now()
	avatar := &commonv1.AssetRef{AssetId: "former-avatar"}

	summary := memberSummary(model.Member{
		ID:        "018f0d9c-1f54-7d51-8abc-a1b2c3d4e5f6",
		Nickname:  nickname,
		DeletedAt: &deletedAt,
	}, avatar)

	if !summary.Deleted {
		t.Fatal("deleted summary must be marked deleted")
	}
	if summary.Nickname != nickname {
		t.Fatalf("nickname = %v, want %q", summary.Nickname, nickname)
	}
	if summary.AvatarAsset != nil {
		t.Fatal("deleted summary must not expose an avatar")
	}
}

func TestNormalizeRegistrationMemberInputHasNoProfileAuthority(t *testing.T) {
	input, err := normalizeRegistrationMemberInput(RegistrationMemberInput{
		IdentityID:      "018f0d9c-1f54-7d51-8abc-a1b2c3d4e5f6",
		Email:           " member@example.test ",
		PreferredLocale: " pt-PT ",
	})
	if err != nil {
		t.Fatalf("normalize registration input: %v", err)
	}
	if input.Email != "member@example.test" {
		t.Fatalf("email = %q, want trimmed input", input.Email)
	}
	if input.PreferredLocale != "pt-PT" {
		t.Fatalf("preferred locale = %q, want canonical locale", input.PreferredLocale)
	}
}

func TestNormalizeRegistrationMemberInputRejectsUnsupportedLocale(t *testing.T) {
	for _, locale := range []string{"", "  ", "xx-XX"} {
		_, err := normalizeRegistrationMemberInput(RegistrationMemberInput{
			IdentityID:      "018f0d9c-1f54-7d51-8abc-a1b2c3d4e5f6",
			Email:           "member@example.test",
			PreferredLocale: locale,
		})
		if err == nil {
			t.Fatalf("unsupported registration locale %q must fail", locale)
		}
	}
}

func TestNormalizeMemberNicknameIsTrimmedCaseSensitiveAndBounded(t *testing.T) {
	for _, value := range []string{"Member", "member", "M", "가"} {
		got, err := normalizeMemberNickname("  " + value + "  ")
		if err != nil || got != value {
			t.Fatalf("normalize %q = %q, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "   ", string(make([]rune, memberNicknameMaxLength+1))} {
		if utf8.RuneCountInString(value) == 0 && value != "" {
			continue
		}
		if _, err := normalizeMemberNickname(value); err == nil {
			t.Fatalf("normalize %q unexpectedly succeeded", value)
		}
	}
}

func TestNormalizeMemberPreferenceLocale(t *testing.T) {
	if got, err := normalizeMemberPreferenceLocale("pt-PT"); err != nil || got != "pt-PT" {
		t.Fatalf("normalize preferred locale = %q, %v; want pt-PT", got, err)
	}
	for _, value := range []string{"", "  ", "xx-XX"} {
		if got, err := normalizeMemberPreferenceLocale(value); err == nil || got != "" {
			t.Fatalf("normalize unsupported locale %q = %q, %v; want error", value, got, err)
		}
	}
}

func TestSelfExtendedProfileMutationRequiresAuthorRole(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	bio := "biography"
	website := "https://profile.example.test"
	links := map[string]string{"homepage": website}

	requireDenied := func(t *testing.T, err error) {
		t.Helper()
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("code = %s, error = %v; want permission_denied", connect.CodeOf(err), err)
		}
	}
	service := &MemberService{spicedb: stack.SpiceDBClient}
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	userCtx := auth.WithUser(context.Background(), user.AuthUserInfo())
	requireDenied(t, service.validateSelfMemberProfileMutation(userCtx, &bio, nil, nil))
	requireDenied(t, service.validateSelfMemberProfileMutation(userCtx, nil, &website, nil))
	requireDenied(t, service.validateSelfMemberProfileMutation(userCtx, nil, nil, links))
	if err := service.validateSelfMemberProfileMutation(userCtx, nil, nil, nil); err != nil {
		t.Fatalf("nickname-only mutation denied: %v", err)
	}
	for _, principal := range []*testutil.OryUser{author, admin} {
		ctx := auth.WithUser(context.Background(), principal.AuthUserInfo())
		if err := service.validateSelfMemberProfileMutation(ctx, &bio, &website, links); err != nil {
			t.Fatalf("%s extended mutation denied: %v", principal.Role, err)
		}
	}
}

func TestHideExtendedMemberProfilePreservesSummary(t *testing.T) {
	bio := "biography"
	website := "https://profile.example.test"
	profile := &managev1.MemberProfile{
		Summary:     &commonv1.MemberSummary{Id: "member-id", Nickname: "nickname"},
		Bio:         &bio,
		Website:     &website,
		SocialLinks: map[string]string{"homepage": website},
	}

	hideExtendedMemberProfile(profile)
	if profile.Bio != nil || profile.Website != nil || profile.SocialLinks != nil {
		t.Fatal("extended profile fields were not hidden")
	}
	if profile.Summary.GetId() != "member-id" || profile.Summary.GetNickname() != "nickname" {
		t.Fatalf("summary changed: %+v", profile.Summary)
	}
}

func TestGetCurrentSessionProjectsAuthenticatedIdentityIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.Author().ID())
	service := &MemberService{
		db:                   stack.DB,
		cdnDomain:            "https://cdn.example.test",
		spicedb:              stack.SpiceDBClient,
		accountSummaryReader: integrationAccountSummaryReader{},
	}

	response, err := service.GetCurrentSession(
		auth.WithUser(t.Context(), user.AuthUserInfo()),
		connect.NewRequest(&managev1.GetCurrentSessionRequest{}),
	)
	if err != nil {
		t.Fatalf("get current session: %v", err)
	}
	if response.Msg.AccountIdentityId != user.IdentityID {
		t.Fatalf("account identity = %q, want %q", response.Msg.AccountIdentityId, user.IdentityID)
	}
	if response.Msg.Member.GetSummary().GetId() != user.MemberID {
		t.Fatalf("member id = %q, want %q", response.Msg.Member.GetSummary().GetId(), user.MemberID)
	}
	if response.Msg.Member.GetRole() != policyv1.AuthorizationRole_AUTHOR {
		t.Fatalf("role = %s, want AUTHOR", response.Msg.Member.GetRole())
	}
}

type currentSessionAccountSummaryReader struct {
	summary *managev1.AccountSummary
}

func (reader currentSessionAccountSummaryReader) SessionSummaryForMember(
	context.Context, *gorm.DB, *auth.SpiceDBClient, string,
) (*managev1.AccountSummary, error) {
	return reader.summary, nil
}

func (reader currentSessionAccountSummaryReader) SummaryForMember(
	context.Context, *gorm.DB, *auth.SpiceDBClient, string,
) (*managev1.AccountSummary, error) {
	return reader.summary, nil
}

func (currentSessionAccountSummaryReader) SummariesForMembers(
	context.Context, *gorm.DB, *auth.SpiceDBClient, []string,
) (map[string]*managev1.AccountSummary, error) {
	panic("GetCurrentSession must not load a member collection")
}

func TestGetCurrentSessionUsesLeanProjectionWithoutProfileOrSectionAuthorizationIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.Author().ID())
	email := "author@example.test"
	service := &MemberService{
		db:        stack.DB,
		cdnDomain: "https://cdn.example.test",
		// Intentionally nil: the lean session boundary must not perform author
		// checks or My-section resource lookups itself.
		spicedb: nil,
		accountSummaryReader: currentSessionAccountSummaryReader{summary: &managev1.AccountSummary{
			MemberId:       user.MemberID,
			CanonicalEmail: &managev1.CanonicalEmailSummary{Email: email},
			Role:           policyv1.AuthorizationRole_AUTHOR,
			Status:         managev1.AccountStatus_ACCOUNT_STATUS_ACTIVE,
		}},
	}

	response, err := service.GetCurrentSession(
		auth.WithUser(t.Context(), user.AuthUserInfo()),
		connect.NewRequest(&managev1.GetCurrentSessionRequest{}),
	)
	if err != nil {
		t.Fatalf("get current session: %v", err)
	}
	if response.Msg.AccountIdentityId != user.IdentityID {
		t.Fatalf("account identity = %q, want %q", response.Msg.AccountIdentityId, user.IdentityID)
	}
	if response.Msg.Member.GetSummary().GetId() != user.MemberID {
		t.Fatalf("member id = %q, want %q", response.Msg.Member.GetSummary().GetId(), user.MemberID)
	}
	if response.Msg.Member.GetEmail() != email {
		t.Fatalf("email = %q, want %q", response.Msg.Member.GetEmail(), email)
	}
	if response.Msg.Member.GetRole() != policyv1.AuthorizationRole_AUTHOR {
		t.Fatalf("role = %s, want AUTHOR", response.Msg.Member.GetRole())
	}
}
