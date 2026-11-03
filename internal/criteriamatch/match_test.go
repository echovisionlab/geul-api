package criteriamatch

import (
	"testing"
	"time"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestMatches(t *testing.T) {
	const tag = "6f1f7d1e-8d6c-4b8e-9a43-2b5b4f0f2c11"
	member := Member{ID: "0b6f0f4e-3c58-4f6a-8f5e-6f7a1e2d3c4b", CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tagged := Facts{ExistingTagIDs: map[string]struct{}{tag: {}}, MemberTagIDs: map[string]struct{}{tag: {}}}
	for _, test := range []struct {
		name     string
		criteria Criteria
		facts    Facts
		want     bool
	}{
		{"every Member", Criteria{Kind: KindAllMembers}, Facts{}, true},
		{"assigned tag", Criteria{Kind: KindMemberTags, Config: Config{MemberTagIDs: []string{tag}}}, tagged, true},
		{"deleted tag", Criteria{Kind: KindMemberTags, Config: Config{MemberTagIDs: []string{tag}}}, Facts{MemberTagIDs: tagged.MemberTagIDs}, false},
		{"held role", Criteria{Kind: KindFilter, Config: Config{AccountRoles: []string{"admin"}}}, Facts{Roles: map[policyv1.RoleID]bool{policyv1.Role.Admin(): true}}, true},
		{"missing role", Criteria{Kind: KindFilter, Config: Config{AccountRoles: []string{"admin"}}}, Facts{}, false},
		{"joined before the window", Criteria{Kind: KindFilter, Config: Config{CreatedAfter: &after}}, Facts{}, false},
		{"excluded Member", Criteria{Kind: KindFilter, Config: Config{ExcludeMemberIDs: []string{member.ID}}}, Facts{}, false},
		{"unknown kind", Criteria{Kind: "MEMBER_CRITERIA_KIND_UNSPECIFIED"}, Facts{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, Matches(member, test.criteria, test.facts))
		})
	}
}
