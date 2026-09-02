package emailauthoring

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type translationOwnerReferences struct {
	templateIDs []string
	layoutIDs   []string
}

func (*translationOwnerReferences) TemplateDeliveryRunCounts(context.Context, *gorm.DB, []string) (map[string]int32, error) {
	return map[string]int32{}, nil
}

func (*translationOwnerReferences) LayoutExternalReferenceCounts(context.Context, *gorm.DB, []string) (map[string]LayoutExternalReferenceCounts, error) {
	return map[string]LayoutExternalReferenceCounts{}, nil
}

func (r *translationOwnerReferences) RequireTemplateMutable(_ context.Context, _ *gorm.DB, id string) error {
	r.templateIDs = append(r.templateIDs, id)
	return nil
}

func (r *translationOwnerReferences) RequireLayoutMutable(_ context.Context, _ *gorm.DB, id string) error {
	r.layoutIDs = append(r.layoutIDs, id)
	return nil
}

func (*translationOwnerReferences) DetachTemplateHistory(context.Context, *gorm.DB, string) error {
	return nil
}

func (*translationOwnerReferences) DetachLayoutHistory(context.Context, *gorm.DB, string) error {
	return nil
}

func TestRequireTranslationSourceMutableLocksOwnedRootBeforeCampaignAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE email_template (id TEXT PRIMARY KEY, updated_at DATETIME);
		CREATE TABLE email_layout (id TEXT PRIMARY KEY, updated_at DATETIME);
		INSERT INTO email_template (id) VALUES ('template-1');
		INSERT INTO email_layout (id) VALUES ('layout-1');
	`).Error)
	references := &translationOwnerReferences{}

	require.NoError(t, RequireTranslationSourceMutable(t.Context(), db, references, "email_template", "template-1"))
	require.NoError(t, RequireTranslationSourceMutable(t.Context(), db, references, "email_layout", "layout-1"))
	require.Equal(t, []string{"template-1"}, references.templateIDs)
	require.Equal(t, []string{"layout-1"}, references.layoutIDs)

	err = RequireTranslationSourceMutable(t.Context(), db, references, "email_template", "missing")
	require.Error(t, err)
	require.Equal(t, []string{"template-1"}, references.templateIDs)
}
