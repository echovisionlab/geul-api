//go:build integration

package application

import (
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRegenerateReturnsOneExactActiveJobUnderPostgresConcurrencyIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Now().UTC()
	requestedBy := uuid.NewString()
	require.NoError(t, db.Create(&model.Member{
		ID: requestedBy, Nickname: "Translation requester " + requestedBy,
		Onboarded: true, AvailableEmails: []string{}, SocialLinks: map[string]string{},
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	entityType := "menu"
	entityID := uuid.NewString()
	xliff := []byte("request")
	manifest := []byte("{}")
	digest := translation.RequestArtifactDigest(xliff, manifest)

	const callers = 8
	start := make(chan struct{})
	type result struct {
		job     model.TranslationJob
		created bool
		err     error
	}
	results := make(chan result, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			var output result
			output.err = db.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
				var err error
				output.job, output.created, err = createRegeneratedTranslationJobWithDB(
					t.Context(), tx, entityType, entityID, "en", "ko",
					requestedBy, uuid.NewString(), now,
					func(*model.TranslationJob) (translation.RequestArtifact, error) {
						return translation.RequestArtifact{XLIFF: xliff, Manifest: manifest, Digest: digest}, nil
					},
				)
				return err
			})
			results <- output
		}()
	}
	ready.Wait()
	close(start)

	jobIDs := make(map[string]struct{})
	createdCount := 0
	for range callers {
		output := <-results
		require.NoError(t, output.err)
		jobIDs[output.job.ID] = struct{}{}
		if output.created {
			createdCount++
		}
	}
	require.Len(t, jobIDs, 1)
	require.Equal(t, 1, createdCount)
	var activeCount int64
	require.NoError(t, db.Model(&model.TranslationJob{}).
		Where(
			"entity_type = ? AND entity_id = ? AND target_locale = ? AND status IN ?",
			entityType,
			entityID,
			"ko",
			[]string{translationJobStatusQueued, translationJobStatusRunning},
		).
		Count(&activeCount).Error)
	require.EqualValues(t, 1, activeCount)
}
