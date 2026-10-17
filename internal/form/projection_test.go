package form

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type formProjectionAssets struct{ Assets }

func (formProjectionAssets) FeaturedImage(context.Context, *gorm.DB, string) *commonv1.AssetRef {
	return nil
}

func TestSubmissionResponsePreservesCountryAndOptionalMetadataOnWire(t *testing.T) {
	for _, country := range []*string{nil, new("KR")} {
		response := new(FormService).toProtoSubmission(&model.FormSubmission{
			ID: "submission-1", FormID: "form-1", CountryCode: country,
			Data: []byte(`{"answer":"value"}`),
		})
		wire, err := protojson.Marshal(response)
		require.NoError(t, err)
		decoded := new(managev1.FormSubmission)
		require.NoError(t, protojson.Unmarshal(wire, decoded))
		require.Equal(t, country, decoded.CountryCode)
		require.Equal(t, "submission-1", decoded.Id)
		require.Equal(t, "form-1", decoded.FormId)
		require.JSONEq(t, `{"answer":"value"}`, string(decoded.Data))
		require.Nil(t, decoded.IpAddress)
		require.Nil(t, decoded.UserAgent)
	}
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
