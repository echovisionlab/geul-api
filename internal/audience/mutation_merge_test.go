package audience

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMergeSegmentConfigPreservesUnobservedPeerChanges(t *testing.T) {
	observedAfter := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	observedBefore := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC)
	currentAfter := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	currentBefore := time.Date(2026, time.February, 28, 0, 0, 0, 0, time.UTC)
	desiredAfter := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)

	current := model.AudienceSegmentConfig{
		MemberTagIDs:     []string{"tag-a", "tag-b", "tag-peer"},
		AccountRoles:     []string{"admin", "user"},
		ExcludeMemberIDs: []string{"member-a", "member-peer"},
		CreatedAfter:     &currentAfter,
		CreatedBefore:    &currentBefore,
	}
	observed := model.AudienceSegmentConfig{
		MemberTagIDs:     []string{"tag-a", "tag-b"},
		AccountRoles:     []string{"user"},
		ExcludeMemberIDs: []string{"member-a"},
		CreatedAfter:     &observedAfter,
		CreatedBefore:    &observedBefore,
	}
	desired := model.AudienceSegmentConfig{
		MemberTagIDs:     []string{"tag-a", "tag-c"},
		AccountRoles:     []string{"user"}, // Unchanged from baseline: retain the peer's admin addition.
		ExcludeMemberIDs: []string{},
		CreatedAfter:     &desiredAfter,   // Changed from baseline: local explicit edit wins.
		CreatedBefore:    &observedBefore, // Unchanged from baseline: retain the peer's edit.
	}

	got := mergeSegmentConfig(current, observed, desired)
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

func TestSegmentUpdatePlanTypeSwitchResetsConfigAtomically(t *testing.T) {
	observedType := model.AudienceSegmentMemberTags
	desiredType := model.AudienceSegmentAllMembers
	config, err := normalizeSegmentConfig(&SegmentConfigInput{MemberTagIDs: []string{"7b1cf6a2-eafa-4a46-b4e5-4084f481c77d"}})
	require.NoError(t, err)
	plan := segmentUpdatePlan{
		command: UpdateSegmentCommand{SegmentType: &desiredType},
		requestedConfig: func() *model.AudienceSegmentConfig {
			return &model.AudienceSegmentConfig{}
		}(),
		observed: &segmentConfigBaseline{segmentType: observedType, config: config},
	}
	current := model.AudienceSegment{
		SegmentType: model.AudienceSegmentMembersByFilter,
		Config: model.AudienceSegmentConfig{
			AccountRoles: []string{"admin"},
		},
	}

	nextType, nextConfig, err := plan.nextState(current)
	require.NoError(t, err)
	require.Equal(t, desiredType, nextType)
	require.Equal(t, model.AudienceSegmentConfig{}, nextConfig)
}

func TestSegmentUpdatePlanSameTypeUsesBaselineMergeAndCurrentType(t *testing.T) {
	segmentType := model.AudienceSegmentMembersByFilter
	observedAfter := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	currentAfter := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	desiredBefore := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	observed := model.AudienceSegmentConfig{
		AccountRoles: []string{"user"},
		CreatedAfter: &observedAfter,
	}
	desired := model.AudienceSegmentConfig{
		AccountRoles:  []string{"user"},
		CreatedAfter:  &observedAfter,
		CreatedBefore: &desiredBefore,
	}
	plan := segmentUpdatePlan{
		command:         UpdateSegmentCommand{},
		requestedConfig: &desired,
		observed:        &segmentConfigBaseline{segmentType: segmentType, config: observed},
	}
	current := model.AudienceSegment{
		SegmentType: segmentType,
		Config: model.AudienceSegmentConfig{
			AccountRoles: []string{"admin", "user"},
			CreatedAfter: &currentAfter,
		},
	}

	nextType, nextConfig, err := plan.nextState(current)
	require.NoError(t, err)
	require.Equal(t, segmentType, nextType)
	require.Equal(t, []string{"admin", "user"}, nextConfig.AccountRoles)
	require.Equal(t, currentAfter, *nextConfig.CreatedAfter)
	require.Equal(t, desiredBefore, *nextConfig.CreatedBefore)
}

func TestBuildSegmentUpdatePlanRequiresObservedForConfigOrTypeWrites(t *testing.T) {
	desiredType := model.AudienceSegmentMembersByFilter
	for _, test := range []struct {
		name    string
		request UpdateSegmentCommand
	}{
		{name: "type", request: UpdateSegmentCommand{SegmentType: &desiredType}},
		{name: "config", request: UpdateSegmentCommand{Config: &SegmentConfigInput{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildSegmentUpdatePlan(test.request)
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
		})
	}
}

func TestBuildSegmentUpdatePlanAllowsScalarOnlyWithoutObserved(t *testing.T) {
	name := "updated"
	plan, err := buildSegmentUpdatePlan(UpdateSegmentCommand{Name: &name})
	require.NoError(t, err)
	require.Nil(t, plan.observed)
	require.Nil(t, plan.requestedConfig)
	current := model.AudienceSegment{
		SegmentType: model.AudienceSegmentMemberTags,
		Config:      model.AudienceSegmentConfig{MemberTagIDs: []string{"tag-id"}},
	}
	nextType, nextConfig, err := plan.nextState(current)
	require.NoError(t, err)
	require.Equal(t, current.SegmentType, nextType)
	require.Equal(t, current.Config, nextConfig)
}

func TestBuildSegmentUpdatePlanParsesObservedBaseline(t *testing.T) {
	tagID := "7b1cf6a2-eafa-4a46-b4e5-4084f481c77d"
	segmentType := model.AudienceSegmentMemberTags
	plan, err := buildSegmentUpdatePlan(UpdateSegmentCommand{
		SegmentType: &segmentType,
		Config:      &SegmentConfigInput{MemberTagIDs: []string{tagID}},
		Observed: &ObservedSegment{
			SegmentType: segmentType,
			Config:      &SegmentConfigInput{MemberTagIDs: []string{tagID}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, plan.observed)
	require.Equal(t, segmentType, plan.observed.segmentType)
	require.Equal(t, []string{tagID}, plan.observed.config.MemberTagIDs)
	require.Equal(t, []string{tagID}, plan.requestedConfig.MemberTagIDs)
}

func TestBuildSegmentUpdatePlanResetsObservedTypeSwitchWithoutConfig(t *testing.T) {
	tagID := "7b1cf6a2-eafa-4a46-b4e5-4084f481c77d"
	observedType := model.AudienceSegmentMemberTags
	desiredType := model.AudienceSegmentAllMembers
	observedConfig := &SegmentConfigInput{MemberTagIDs: []string{tagID}}
	plan, err := buildSegmentUpdatePlan(UpdateSegmentCommand{
		SegmentType: &desiredType,
		Observed: &ObservedSegment{
			SegmentType: observedType,
			Config:      observedConfig,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, plan.requestedConfig)
	require.Equal(t, model.AudienceSegmentConfig{}, *plan.requestedConfig)

	current := &model.AudienceSegment{
		SegmentType: observedType,
		Config:      model.AudienceSegmentConfig{MemberTagIDs: []string{tagID}},
	}
	nextType, nextConfig, err := plan.nextState(*current)
	require.NoError(t, err)
	updates, changed, configChanged := buildSegmentUpdates(current, plan, nextType, nextConfig)
	require.True(t, configChanged)
	require.Contains(t, changed, "member_tag_ids")
	require.Equal(t, desiredType, updates["segment_type"])
}
