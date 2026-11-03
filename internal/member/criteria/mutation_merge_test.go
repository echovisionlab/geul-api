package criteria

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/stretchr/testify/require"
)

func TestMergeCriteriaConfigPreservesUnobservedPeerChanges(t *testing.T) {
	observedAfter := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	observedBefore := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC)
	currentAfter := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	currentBefore := time.Date(2026, time.February, 28, 0, 0, 0, 0, time.UTC)
	desiredAfter := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)

	current := Config{
		MemberTagIDs:     []string{"tag-a", "tag-b", "tag-peer"},
		AccountRoles:     []string{"admin", "user"},
		ExcludeMemberIDs: []string{"member-a", "member-peer"},
		CreatedAfter:     &currentAfter,
		CreatedBefore:    &currentBefore,
	}
	observed := Config{
		MemberTagIDs:     []string{"tag-a", "tag-b"},
		AccountRoles:     []string{"user"},
		ExcludeMemberIDs: []string{"member-a"},
		CreatedAfter:     &observedAfter,
		CreatedBefore:    &observedBefore,
	}
	desired := Config{
		MemberTagIDs:     []string{"tag-a", "tag-c"},
		AccountRoles:     []string{"user"}, // Unchanged from baseline: retain the peer's admin addition.
		ExcludeMemberIDs: []string{},
		CreatedAfter:     &desiredAfter,   // Changed from baseline: local explicit edit wins.
		CreatedBefore:    &observedBefore, // Unchanged from baseline: retain the peer's edit.
	}

	got := mergeCriteriaConfig(current, observed, desired)
	require.Equal(t, []string{"tag-a", "tag-c", "tag-peer"}, got.MemberTagIDs)
	require.Equal(t, []string{"admin", "user"}, got.AccountRoles)
	require.Equal(t, []string{"member-peer"}, got.ExcludeMemberIDs)
	require.Equal(t, desiredAfter, *got.CreatedAfter)
	require.Equal(t, currentBefore, *got.CreatedBefore)
}

func TestMergeObservedStringSetAppliesOnlyObservedDeltas(t *testing.T) {
	got := mergeObservedStringSet(
		[]string{"kept", "peer-added", "peer-removed"},
		[]string{"kept", "remove-me"},
		[]string{"kept", "add-me"},
	)
	require.Equal(t, []string{"add-me", "kept", "peer-added", "peer-removed"}, got)
}

func TestCriteriaUpdatePlanTypeSwitchResetsConfigAtomically(t *testing.T) {
	observedType := KindMemberTags
	desiredType := KindAllMembers
	config, err := normalizeCriteriaConfig(&CriteriaConfigInput{MemberTagIDs: []string{"7b1cf6a2-eafa-4a46-b4e5-4084f481c77d"}})
	require.NoError(t, err)
	plan := criteriaUpdatePlan{
		command: UpdateCriteriaCommand{Kind: &desiredType},
		requestedConfig: func() *Config {
			return &Config{}
		}(),
		observed: &criteriaConfigBaseline{kind: observedType, config: config},
	}
	current := Criteria{
		Type: KindFilter,
		Config: Config{
			AccountRoles: []string{"admin"},
		},
	}

	nextType, nextConfig, err := plan.nextState(current)
	require.NoError(t, err)
	require.Equal(t, desiredType, nextType)
	require.Equal(t, Config{}, nextConfig)
}

func TestCriteriaUpdatePlanSameTypeUsesBaselineMergeAndCurrentType(t *testing.T) {
	kind := KindFilter
	observedAfter := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	currentAfter := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	desiredBefore := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	observed := Config{
		AccountRoles: []string{"user"},
		CreatedAfter: &observedAfter,
	}
	desired := Config{
		AccountRoles:  []string{"user"},
		CreatedAfter:  &observedAfter,
		CreatedBefore: &desiredBefore,
	}
	plan := criteriaUpdatePlan{
		command:         UpdateCriteriaCommand{},
		requestedConfig: &desired,
		observed:        &criteriaConfigBaseline{kind: kind, config: observed},
	}
	current := Criteria{
		Type: kind,
		Config: Config{
			AccountRoles: []string{"admin", "user"},
			CreatedAfter: &currentAfter,
		},
	}

	nextType, nextConfig, err := plan.nextState(current)
	require.NoError(t, err)
	require.Equal(t, kind, nextType)
	require.Equal(t, []string{"admin", "user"}, nextConfig.AccountRoles)
	require.Equal(t, currentAfter, *nextConfig.CreatedAfter)
	require.Equal(t, desiredBefore, *nextConfig.CreatedBefore)
}

func TestBuildCriteriaUpdatePlanRequiresObservedForConfigOrTypeWrites(t *testing.T) {
	desiredType := KindFilter
	for _, test := range []struct {
		name    string
		request UpdateCriteriaCommand
	}{
		{name: "type", request: UpdateCriteriaCommand{Kind: &desiredType}},
		{name: "config", request: UpdateCriteriaCommand{Config: &CriteriaConfigInput{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildCriteriaUpdatePlan(test.request)
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
		})
	}
}

func TestBuildCriteriaUpdatePlanAllowsScalarOnlyWithoutObserved(t *testing.T) {
	name := "updated"
	plan, err := buildCriteriaUpdatePlan(UpdateCriteriaCommand{Name: &name})
	require.NoError(t, err)
	require.Nil(t, plan.observed)
	require.Nil(t, plan.requestedConfig)
	current := Criteria{
		Type:   KindMemberTags,
		Config: Config{MemberTagIDs: []string{"tag-id"}},
	}
	nextType, nextConfig, err := plan.nextState(current)
	require.NoError(t, err)
	require.Equal(t, current.Type, nextType)
	require.Equal(t, current.Config, nextConfig)
}

func TestBuildCriteriaUpdatePlanParsesObservedBaseline(t *testing.T) {
	tagID := "7b1cf6a2-eafa-4a46-b4e5-4084f481c77d"
	kind := KindMemberTags
	plan, err := buildCriteriaUpdatePlan(UpdateCriteriaCommand{
		Kind:   &kind,
		Config: &CriteriaConfigInput{MemberTagIDs: []string{tagID}},
		Observed: &ObservedCriteria{
			Kind:   kind,
			Config: &CriteriaConfigInput{MemberTagIDs: []string{tagID}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, plan.observed)
	require.Equal(t, kind, plan.observed.kind)
	require.Equal(t, []string{tagID}, plan.observed.config.MemberTagIDs)
	require.Equal(t, []string{tagID}, plan.requestedConfig.MemberTagIDs)
}

func TestBuildCriteriaUpdatePlanResetsObservedTypeSwitchWithoutConfig(t *testing.T) {
	tagID := "7b1cf6a2-eafa-4a46-b4e5-4084f481c77d"
	observedType := KindMemberTags
	desiredType := KindAllMembers
	observedConfig := &CriteriaConfigInput{MemberTagIDs: []string{tagID}}
	plan, err := buildCriteriaUpdatePlan(UpdateCriteriaCommand{
		Kind: &desiredType,
		Observed: &ObservedCriteria{
			Kind:   observedType,
			Config: observedConfig,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, plan.requestedConfig)
	require.Equal(t, Config{}, *plan.requestedConfig)

	current := &Criteria{
		Type:   observedType,
		Config: Config{MemberTagIDs: []string{tagID}},
	}
	nextType, nextConfig, err := plan.nextState(*current)
	require.NoError(t, err)
	change, changed, configChanged := buildCriteriaChange(*current, plan, nextType, nextConfig)
	require.True(t, configChanged)
	require.Contains(t, changed, "member_tag_ids")
	require.Equal(t, desiredType, *change.Type)
}
