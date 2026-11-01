package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
)

// LoadSummaries resolves a bounded set of onboarded Member profiles and their
// current avatar bindings in a fixed number of reads. Public and ordinary
// domain reads must not call Kratos or load an asset once per row.
func LoadSummaries(ctx context.Context, store Store, memberIDs []string) (map[string]member.Summary, error) {
	ids := sliceutil.UniqueNonZero(memberIDs)
	result := make(map[string]member.Summary, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	members, err := store.PublicMembers(ctx, ids)
	if err != nil {
		return nil, err
	}
	avatars, err := store.PublicAvatars(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, value := range members {
		result[value.ID] = projectPublicMemberSummary(value, avatars[value.ID])
	}
	return result, nil
}

func projectPublicMemberSummary(value member.Member, avatar *member.Asset) member.Summary {
	deleted := value.DeletedAt != nil || value.AccountIdentityID == nil
	if deleted {
		avatar = nil
	}
	return member.Summary{ID: value.ID, Nickname: value.Nickname, Avatar: avatar, Deleted: deleted}
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
