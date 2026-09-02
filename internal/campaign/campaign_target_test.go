package campaign

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestCreateCampaignTargetDefinitionRequiresExplicitTarget(t *testing.T) {
	t.Parallel()

	segmentID := uuid.NewString()
	tests := []struct {
		name        string
		request     *managev1.CreateCampaignRequest
		wantMode    string
		wantSegment *string
		wantErr     bool
	}{
		{name: "nil request", wantErr: true},
		{name: "unset target", request: &managev1.CreateCampaignRequest{}, wantErr: true},
		{name: "empty all", request: &managev1.CreateCampaignRequest{Target: &managev1.CreateCampaignRequest_All{}}, wantErr: true},
		{name: "all", request: &managev1.CreateCampaignRequest{Target: &managev1.CreateCampaignRequest_All{All: &emptypb.Empty{}}}, wantMode: model.CampaignTargetModeAll},
		{name: "empty segment", request: &managev1.CreateCampaignRequest{Target: &managev1.CreateCampaignRequest_SegmentId{}}, wantErr: true},
		{name: "invalid segment", request: &managev1.CreateCampaignRequest{Target: &managev1.CreateCampaignRequest_SegmentId{SegmentId: "not-a-uuid"}}, wantErr: true},
		{name: "segment", request: &managev1.CreateCampaignRequest{Target: &managev1.CreateCampaignRequest_SegmentId{SegmentId: segmentID}}, wantMode: model.CampaignTargetModeSegment, wantSegment: &segmentID},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, segment, err := createCampaignTargetDefinition(test.request)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantMode, mode)
			require.Equal(t, test.wantSegment, segment)
		})
	}
}

func TestCreateCampaignSourceLocaleIsExplicitAndCanonical(t *testing.T) {
	t.Parallel()

	normalizer := campaignLocaleNormalizerFunc(localization.NormalizeSupportedLocale)
	locale, err := normalizeCampaignCreateSourceLocale(normalizer, "pt")
	require.NoError(t, err)
	require.Equal(t, "pt-BR", locale)

	_, err = normalizeCampaignCreateSourceLocale(normalizer, "")
	require.Error(t, err)

	_, err = normalizeCampaignCreateSourceLocale(normalizer, "xx")
	require.Error(t, err)
}
