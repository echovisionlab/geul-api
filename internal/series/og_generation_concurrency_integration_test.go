//go:build integration

package series_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type seriesOgRenderConfig struct{}

func (seriesOgRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

type seriesOgSnapshot struct {
	Title string `json:"title"`
}

func TestAutomaticOgRequestReloadsCanonicalSeriesSnapshotAfterTargetLockIntegration(t *testing.T) {
	db := testutil.NewConcurrentPostIntegrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	seriesID := uuid.NewString()
	now := time.Now().UTC()
	contentDocumentID := uuid.NewString()
	contentDocumentRevision := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at) VALUES (?, 'compact', ?, ?, ?)`,
		contentDocumentID, contentDocumentRevision, now, now,
	).Error)
	require.NoError(t, db.Create(&model.Series{
		ID: seriesID, Slug: "series-" + seriesID, SourceLocale: "en",
		ContentDocumentID: contentDocumentID,
		Status:            managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String(), CreatedAt: now, UpdatedAt: &now,
	}).Error)
	require.NoError(t, db.Table("series_translation").Create(&model.SeriesTranslation{
		EntityID: seriesID, Locale: "en",
		Title: new("Title A"), CreatedAt: now, UpdatedAt: now,
	}).Error)
	locale := "en"
	target := model.OgGenerationTarget{
		ID: uuid.NewString(), EntityType: "series", EntityID: seriesID,
		TargetKind: "locale", Locale: &locale, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&target).Error)

	blocker := db.WithContext(ctx).Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	require.NoError(t, blocker.Exec("SELECT 1 FROM og_generation_target WHERE id = ? FOR UPDATE", target.ID).Error)

	planner := og.NewPlanner(db, "https://cdn.example.com", seriesOgRenderConfig{}, seriesadapter.NewProjection())
	refresher := og.NewRefresher(planner, og.NewResolver(seriesadapter.NewRequests()))
	started := make(chan struct{})
	result := make(chan struct {
		plan *og.Plan
		err  error
	}, 1)
	go func() {
		var plan *og.Plan
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			close(started)
			var requestErr error
			plan, requestErr = refresher.RequestCurrentWithDB(
				ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_SERIES,
				seriesID, "en", false, "series_concurrent_update",
			)
			return requestErr
		})
		result <- struct {
			plan *og.Plan
			err  error
		}{plan: plan, err: err}
	}()
	<-started
	time.Sleep(150 * time.Millisecond)
	require.NoError(t, db.WithContext(ctx).Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, "en").
		Updates(structured.Fields{"title": "Title B", "updated_at": time.Now().UTC()}).Error)
	require.NoError(t, blocker.Rollback().Error)

	requested := <-result
	require.NoError(t, requested.err)
	require.NotNil(t, requested.plan)
	require.Len(t, requested.plan.GenerationIDs, 1)
	var generation model.OgGeneration
	require.NoError(t, db.First(&generation, "id = ?", requested.plan.GenerationIDs[0]).Error)
	var snapshot seriesOgSnapshot
	require.NoError(t, json.Unmarshal(generation.EntitySnapshot, &snapshot))
	require.Equal(t, "Title B", snapshot.Title)
}
