package filemedia_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFaviconBundleCompletesAndReusesVerifiedPNGAndSVGAssets(t *testing.T) {
	for _, kind := range []string{"png", "svg"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			units := mutationtest.New[filemedia.Store](s)
			id := uuid.NewString()
			row, storage, processor, writes, deletes := filemedia.ExportFaviconFixture(t, id, kind, 0)
			s.Files[id] = filemediatest.CatalogFileRecord{Row: row}
			service := filemedia.ExportFaviconService(units, filemediatest.FaviconCommits{Units: units}, storage, processor)
			asset, err := filemedia.ExportGenerateFavicon(service, t.Context(), id)
			require.NoError(t, err)
			require.Equal(t, "image/png", asset.GetMimeType())
			expected := 7
			if kind == "svg" {
				expected = 8
			}
			require.Len(t, writes(), expected)
			require.Len(t, s.AssetIDs, expected)
			require.Len(t, s.Derivatives[id], 7)
			again, err := filemedia.ExportGenerateFavicon(service, t.Context(), id)
			require.NoError(t, err)
			require.Equal(t, asset.GetAssetId(), again.GetAssetId())
			require.Len(t, writes(), expected)
			require.Empty(t, deletes())
			set, err := s.FaviconSet(t.Context(), "https://cdn.example.com", id)
			require.NoError(t, err)
			if kind == "svg" {
				require.NotNil(t, set.IconSVG)
				require.Equal(t, "image/svg+xml", set.IconSVG.MimeType)
			} else {
				require.Nil(t, set.IconSVG)
			}
		})
	}
}

func TestFaviconWriteFailureRetainsCleanupAuthorityWithoutDeletingObjects(t *testing.T) {
	s := filemediatest.NewLibraryStore()
	units := mutationtest.New[filemedia.Store](s)
	id := uuid.NewString()
	row, storage, processor, _, deletes := filemedia.ExportFaviconFixture(t, id, "png", 3)
	s.Files[id] = filemediatest.CatalogFileRecord{Row: row}
	service := filemedia.ExportFaviconService(units, filemediatest.FaviconCommits{Units: units}, storage, processor)
	_, err := filemedia.ExportGenerateFavicon(service, t.Context(), id)
	require.ErrorContains(t, err, "stage favicon derivative")
	counts := map[mediaasset.AssetStatus]int{}
	for id := range s.AssetIDs {
		a, err := s.MediaAssets.Asset(t.Context(), id)
		require.NoError(t, err)
		counts[a.Status]++
	}
	require.Equal(t, map[mediaasset.AssetStatus]int{mediaasset.AssetDeletePending: 2, mediaasset.AssetAllocated: 1}, counts)
	require.Empty(t, s.Derivatives[id])
	require.Empty(t, deletes())
}

type rejectedFaviconCommits struct {
	units mutation.UnitOfWork[domain.Store]
	err   error
}

func (c rejectedFaviconCommits) Commit(ctx context.Context, change func(domain.Store) error) domain.FaviconCommitResult {
	err := c.units.Transaction(ctx, false, func(tx mutation.Tx[domain.Store]) error {
		if err := change(tx.Store()); err != nil {
			return err
		}
		return c.err
	})
	return domain.FaviconCommitResult{Stage: domain.FaviconChangeFailed, Err: err}
}

func TestFaviconFailurePhasesRetainCommittedWinnersAndCleanKnownFailures(t *testing.T) {
	for _, phase := range []string{"begin", "change", "commit acknowledged loss", "commit unreconciled"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			units := mutationtest.New[filemedia.Store](s)
			id := uuid.NewString()
			row, storage, processor, writes, deletes := filemedia.ExportFaviconFixture(t, id, "png", 0)
			s.Files[id] = filemediatest.CatalogFileRecord{Row: row}
			failure := errors.New("storage failure")
			commits := filemediatest.FaviconCommits{Units: units}
			var driver domain.FaviconCommits
			switch phase {
			case "begin":
				commits.BeginError = failure
				driver = commits
			case "change":
				driver = rejectedFaviconCommits{units: units, err: failure}
			default:
				commits.CommitError = failure
				driver = commits
			}
			service := filemedia.ExportFaviconService(units, driver, storage, processor)
			if phase == "commit unreconciled" {
				filemedia.ExportFaviconReconcileError(service, errors.New("reconciliation read failed"))
			}
			asset, err := filemedia.ExportGenerateFavicon(service, t.Context(), id)
			if phase == "commit acknowledged loss" {
				require.NoError(t, err)
				require.NotNil(t, asset)
			} else {
				require.Error(t, err)
				require.Nil(t, asset)
			}
			require.Len(t, writes(), 7)
			require.Empty(t, deletes())
			require.Contains(t, s.Files, id)
			want := mediaasset.AssetReady
			if phase == "begin" || phase == "change" {
				want = mediaasset.AssetDeletePending
				require.Empty(t, s.Derivatives[id])
				require.ErrorIs(t, err, failure)
			} else {
				require.Len(t, s.Derivatives[id], 7)
			}
			for id := range s.AssetIDs {
				a, readErr := s.MediaAssets.Asset(t.Context(), id)
				require.NoError(t, readErr)
				require.Equal(t, want, a.Status)
			}
			if phase == "commit unreconciled" {
				require.ErrorIs(t, err, filemedia.ExportFaviconCommitUncertain)
				filemedia.ExportFaviconReconcileError(service, nil)
				asset, err = filemedia.ExportGenerateFavicon(service, t.Context(), id)
				require.NoError(t, err)
				require.NotNil(t, asset)
				require.Len(t, writes(), 7)
			}
		})
	}
}

type barrierFavicon struct {
	inner   favicon.Processor
	entered chan struct{}
	release <-chan struct{}
}

func (p barrierFavicon) Process(ctx context.Context, source []byte, mime string) ([]favicon.Output, error) {
	select {
	case p.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-p.release:
		return p.inner.Process(ctx, source, mime)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestConcurrentFaviconFinalizersConvergeAndScheduleTheLosingBundle(t *testing.T) {
	s := filemediatest.NewLibraryStore()
	units := mutationtest.New[filemedia.Store](s)
	id := uuid.NewString()
	row, storage, processor, writes, deletes := filemedia.ExportFaviconFixture(t, id, "png", 0)
	s.Files[id] = filemediatest.CatalogFileRecord{Row: row}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	service := filemedia.ExportFaviconService(units, filemediatest.FaviconCommits{Units: units}, storage, barrierFavicon{inner: processor, entered: entered, release: release})
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			asset, err := filemedia.ExportGenerateFavicon(service, t.Context(), id)
			results <- result{asset.GetAssetId(), err}
		}()
	}
	<-entered
	<-entered
	close(release)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.NotEmpty(t, first.id)
	require.Equal(t, first.id, second.id)
	require.Len(t, writes(), 14)
	require.Empty(t, deletes())
	counts := map[mediaasset.AssetStatus]int{}
	for id := range s.AssetIDs {
		a, err := s.MediaAssets.Asset(t.Context(), id)
		require.NoError(t, err)
		counts[a.Status]++
	}
	require.Equal(t, map[mediaasset.AssetStatus]int{mediaasset.AssetReady: 7, mediaasset.AssetDeletePending: 7}, counts)
}

type failedFaviconProcessor struct{ err error }

func (p failedFaviconProcessor) Process(ctx context.Context, _ []byte, _ string) ([]favicon.Output, error) {
	if p.err != nil {
		return nil, p.err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestFaviconValidationProcessorFailureAndDeadlinePublishNothing(t *testing.T) {
	for _, mode := range []string{"unsafe svg", "processor", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			units := mutationtest.New[filemedia.Store](s)
			id := uuid.NewString()
			kind := "png"
			if mode == "unsafe svg" {
				kind = mode
			}
			row, storage, processor, writes, _ := filemedia.ExportFaviconFixture(t, id, kind, 0)
			s.Files[id] = filemediatest.CatalogFileRecord{Row: row}
			switch mode {
			case "processor":
				processor = failedFaviconProcessor{err: errors.New("processor failed")}
			case "deadline":
				processor = failedFaviconProcessor{}
			}
			service := filemedia.ExportFaviconService(units, filemediatest.FaviconCommits{Units: units}, storage, processor)
			if mode == "deadline" {
				filemedia.ExportFaviconTimeout(service, 20*time.Millisecond)
			}
			started := time.Now()
			asset, err := filemedia.ExportGenerateFavicon(service, t.Context(), id)
			require.Error(t, err)
			require.Nil(t, asset)
			require.Empty(t, s.AssetIDs)
			require.Empty(t, writes())
			switch mode {
			case "unsafe svg":
				require.ErrorContains(t, err, "style")
			case "processor":
				require.ErrorContains(t, err, "processor failed")
			case "deadline":
				require.ErrorContains(t, err, "deadline exceeded")
				require.Less(t, time.Since(started), time.Second)
			}
		})
	}
}
