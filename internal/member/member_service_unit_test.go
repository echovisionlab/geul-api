package member

import (
	"testing"
	"time"
	"unicode/utf8"
)

func TestMemberSummaryDeletedMemberNeverExposesProfilePII(t *testing.T) {
	t.Parallel()
	nickname := "Former name"
	deletedAt := time.Now()
	avatar := &Asset{ID: "former-avatar"}

	summary := memberSummary(Member{
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
	if summary.Avatar != nil {
		t.Fatal("deleted summary must not expose an avatar")
	}
}

func TestNormalizeRegistrationMemberInputHasNoProfileAuthority(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	if got, err := normalizeMemberPreferenceLocale("pt-PT"); err != nil || got != "pt-PT" {
		t.Fatalf("normalize preferred locale = %q, %v; want pt-PT", got, err)
	}
	for _, value := range []string{"", "  ", "xx-XX"} {
		if got, err := normalizeMemberPreferenceLocale(value); err == nil || got != "" {
			t.Fatalf("normalize unsupported locale %q = %q, %v; want error", value, got, err)
		}
	}
}

func TestHideExtendedMemberProfilePreservesSummary(t *testing.T) {
	t.Parallel()
	bio := "biography"
	website := "https://profile.example.test"
	profile := &Profile{
		Summary:     Summary{ID: "member-id", Nickname: "nickname"},
		Bio:         &bio,
		Website:     &website,
		SocialLinks: map[string]string{"homepage": website},
	}

	hideExtendedMemberProfile(profile)
	if profile.Bio != nil || profile.Website != nil || profile.SocialLinks != nil {
		t.Fatal("extended profile fields were not hidden")
	}
	if profile.Summary.ID != "member-id" || profile.Summary.Nickname != "nickname" {
		t.Fatalf("summary changed: %+v", profile.Summary)
	}
}
