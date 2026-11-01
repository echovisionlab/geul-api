package member

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

// loadAvatars reads no row for no Members.
func loadAvatars(ctx context.Context, store Store, memberIDs []string) (map[string]*Asset, error) {
	if len(memberIDs) == 0 {
		return map[string]*Asset{}, nil
	}
	return store.Avatars(ctx, memberIDs)
}

func memberSummary(member Member, avatar *Asset) Summary {
	deleted := member.tombstone()
	if deleted {
		// A tombstone must never expose profile PII, even while asynchronous
		// cleanup of the former avatar binding is still pending.
		avatar = nil
	}
	return Summary{ID: member.ID, Nickname: member.Nickname, Avatar: avatar, Deleted: deleted}
}

// LoadSummaries reads the summary of each Member in memberIDs that exists.
func LoadSummaries(ctx context.Context, store Store, memberIDs []string) (map[string]Summary, error) {
	result := make(map[string]Summary, len(memberIDs))
	if len(memberIDs) == 0 {
		return result, nil
	}
	members, err := store.Members(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	assets, err := store.Avatars(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	for i := range members {
		result[members[i].ID] = memberSummary(members[i], assets[members[i].ID])
	}
	return result, nil
}

// LoadAuthorizationEligibleSummary reads the summary of an active, onboarded
// Member that may hold authority, answering any other as not found.
func LoadAuthorizationEligibleSummary(ctx context.Context, store Store, memberID string) (Summary, error) {
	if _, err := store.RequireTarget(ctx, memberID); err != nil {
		return Summary{}, err
	}
	summaries, err := LoadSummaries(ctx, store, []string{memberID})
	if err != nil {
		return Summary{}, err
	}
	summary, found := summaries[memberID]
	if !found || summary.Deleted {
		return Summary{}, domainerr.ResourceNotFound("member", memberID)
	}
	return summary, nil
}

func memberProfile(member Member, avatar *Asset) Profile {
	profile := Profile{
		Summary:   memberSummary(member, avatar),
		CreatedAt: member.CreatedAt,
	}
	if !member.tombstone() {
		profile.Bio = member.Bio
		profile.Website = member.Website
		profile.SocialLinks = member.SocialLinks
		profile.PreferredLocale = member.PreferredLocale
		if !member.UpdatedAt.IsZero() {
			updatedAt := member.UpdatedAt
			profile.UpdatedAt = &updatedAt
		}
	}
	return profile
}

func hideExtendedMemberProfile(profile *Profile) {
	profile.Bio = nil
	profile.Website = nil
	profile.SocialLinks = nil
}

func (s *Service) validateSelfMemberProfileMutation(
	ctx context.Context,
	bio *string,
	website *string,
	socialLinks map[string]string,
) error {
	author, err := s.isGlobalAuthor(ctx)
	if err != nil {
		return err
	}
	if author {
		return nil
	}
	if bio != nil || website != nil || socialLinks != nil {
		return domainerr.Denied("extended profile fields require Author or Admin role")
	}
	return nil
}

func (s *Service) memberProfileByID(ctx context.Context, memberID string) (Member, Profile, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return Member{}, Profile{}, domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	var (
		members []Member
		assets  map[string]*Asset
	)
	if err := s.read(ctx, func(store Store) error {
		var err error
		if members, err = store.Members(ctx, []string{memberID}); err != nil || len(members) != 1 {
			return err
		}
		assets, err = store.Avatars(ctx, []string{memberID})
		return err
	}); err != nil {
		return Member{}, Profile{}, err
	}
	if len(members) != 1 {
		return Member{}, Profile{}, domainerr.ResourceNotFound("member", memberID)
	}
	return members[0], memberProfile(members[0], assets[memberID]), nil
}
