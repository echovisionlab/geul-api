package form

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type formProjectionAssets struct{ Assets }

func (formProjectionAssets) FeaturedImage(context.Context, *gorm.DB, string) *commonv1.AssetRef {
	return nil
}

type formProjectionOG struct{ OG }

func (formProjectionOG) ReadyAsset(context.Context, *gorm.DB, *string, *string) (*commonv1.AssetRef, error) {
	return nil, nil
}

func TestFormResponseKeepsSourceLocaleOfSelectedSchema(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	schema := []byte(`{"fields":[]}`)
	mock.ExpectQuery("SELECT title, content_json, content_text, og_asset_id, updated_at").
		WithArgs("form-1", "pt-BR").
		WillReturnRows(sqlmock.NewRows([]string{"title", "content_json", "content_text", "og_asset_id", "updated_at"}).
			AddRow("Formulário", schema, nil, nil, time.Now()))
	service := &FormService{db: db, assets: formProjectionAssets{}, og: formProjectionOG{}}
	response, err := service.toProtoForm(t.Context(), &model.Form{ID: "form-1", SourceLocale: "pt-BR"})
	require.NoError(t, err)
	require.Equal(t, "pt-BR", response.SourceLocale)
	require.Equal(t, "Formulário", response.Title)
	require.JSONEq(t, string(schema), string(response.Schema))
	require.NoError(t, mock.ExpectationsWereMet())
}
