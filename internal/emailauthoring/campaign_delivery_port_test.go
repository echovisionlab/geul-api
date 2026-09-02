package emailauthoring

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestEmailTemplateReferenceCountsFailClosedWithoutCampaignDeliveryPort(t *testing.T) {
	err := loadEmailTemplateReferenceCounts(
		context.Background(), nil, nil, []*model.EmailTemplate{{ID: "template-id"}},
	)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

func TestEmailLayoutReferenceCountsFailClosedWithoutCampaignDeliveryPort(t *testing.T) {
	err := loadEmailLayoutReferenceCounts(
		context.Background(), nil, nil, []*model.EmailLayout{{ID: "layout-id"}},
	)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}
