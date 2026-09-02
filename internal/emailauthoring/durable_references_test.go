package emailauthoring

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type durableReferenceStub struct {
	templateCount int32
	layoutCounts  LayoutExternalReferenceCounts
	templateErr   error
	layoutErr     error
}

func (stub durableReferenceStub) TemplateDeliveryRunCounts(context.Context, *gorm.DB, []string) (map[string]int32, error) {
	return map[string]int32{"template-1": stub.templateCount}, nil
}

func (stub durableReferenceStub) LayoutExternalReferenceCounts(context.Context, *gorm.DB, []string) (map[string]LayoutExternalReferenceCounts, error) {
	return map[string]LayoutExternalReferenceCounts{"layout-1": stub.layoutCounts}, nil
}

func (stub durableReferenceStub) RequireTemplateMutable(context.Context, *gorm.DB, string) error {
	return stub.templateErr
}

func (stub durableReferenceStub) RequireLayoutMutable(context.Context, *gorm.DB, string) error {
	return stub.layoutErr
}

func (durableReferenceStub) DetachTemplateHistory(context.Context, *gorm.DB, string) error {
	return nil
}
func (durableReferenceStub) DetachLayoutHistory(context.Context, *gorm.DB, string) error { return nil }

func TestDurableReferenceCountsAreOwnedByCampaignDeliveryPort(t *testing.T) {
	now := time.Now().UTC()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE email_template (id TEXT PRIMARY KEY, layout_id TEXT)`).Error)
	for index := 0; index < 7; index++ {
		require.NoError(t, db.Exec(`INSERT INTO email_template (id, layout_id) VALUES (?, 'layout-1')`, index).Error)
	}
	template := &model.EmailTemplate{ID: "template-1", CreatedAt: now}
	layout := &model.EmailLayout{ID: "layout-1", CreatedAt: now, UpdatedAt: now}
	references := durableReferenceStub{
		templateCount: 5,
		layoutCounts:  LayoutExternalReferenceCounts{Campaigns: 6, DeliveryRuns: 8},
	}
	require.NoError(t, loadEmailTemplateReferenceCounts(t.Context(), db, references, []*model.EmailTemplate{template}))
	require.NoError(t, loadEmailLayoutReferenceCounts(t.Context(), db, references, []*model.EmailLayout{layout}))
	require.Equal(t, int32(5), toProtoEmailTemplate(template).DeliveryRunCount)
	protoLayout := toProtoEmailLayout(layout)
	require.Equal(t, int32(6), protoLayout.CampaignCount)
	require.Equal(t, int32(7), protoLayout.TemplateCount)
	require.Equal(t, int32(8), protoLayout.DeliveryRunCount)
}
