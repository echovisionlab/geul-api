package filemediatest

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/favicon/favicontest"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

func (s *LibraryStore) FaviconSource(_ context.Context, id string, _ bool) (domain.File, error) {
	if s.Fail != nil {
		return domain.File{}, s.Fail
	}
	row, ok := s.Files[id]
	if !ok {
		return domain.File{}, domain.ErrFileNotFound
	}
	file := deliveryFile(row)
	return domain.File{ID: file.ID, MimeType: file.MimeType, FileSize: file.FileSize, Extension: file.Extension}, nil
}
func (s *LibraryStore) FaviconSet(ctx context.Context, cdn, file string) (*favicon.Set, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	owner, _ := favicontest.New()
	owner.CDNDomain = cdn
	if row, ok := s.Files[file]; ok {
		source := deliveryFile(row)
		owner.SeedFile(file, source.MimeType)
	}
	derivatives := map[string]string{}
	for _, row := range s.Derivatives[file] {
		if row.AssetID != nil && strings.HasPrefix(row.Type, "FILE_DERIVATIVE_TYPE_FAVICON_") {
			derivatives[*row.AssetID] = row.Type
		}
	}
	for id := range s.AssetIDs {
		a, err := s.MediaAssets.Asset(ctx, id)
		if err != nil {
			return nil, err
		}
		owner.SeedAsset(file, derivatives[id], favicon.DerivativeAsset{ID: a.ID, SourceFileID: a.SourceFileID, Kind: a.Kind, ObjectKey: a.ObjectKey, Extension: a.Extension, MimeType: a.MimeType, FileSize: a.FileSize, SHA256: a.SHA256, Disposition: a.Disposition, DownloadFilename: a.DownloadFilename, Status: string(a.Status), CreatedAt: a.CreatedAt})
	}
	return favicon.LoadSet(ctx, owner, file)
}
func (s *LibraryStore) AttachFaviconSource(ctx context.Context, id, file string, now time.Time) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	a, err := s.MediaAssets.Asset(ctx, id)
	if errors.Is(err, mediaasset.ErrAssetNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.Kind != "favicon" || a.Status != mediaasset.AssetReady || a.SourceFileID != nil {
		return false, nil
	}
	if _, ok := s.Files[file]; !ok {
		return false, domain.ErrFileNotFound
	}
	a.SourceFileID = new(file)
	a.UpdatedAt = now
	s.MediaAssets.PutAsset(a)
	return true, nil
}
func (s *LibraryStore) InsertFaviconDerivative(ctx context.Context, id, file, typ, asset string) error {
	for _, row := range s.Derivatives[file] {
		if row.Type == typ {
			return errors.New("favicon derivative already exists")
		}
	}
	return s.UpsertIngestDerivative(ctx, domain.IngestDerivative{ID: id, FileID: file, Type: typ, AssetID: &asset, CreatedAt: time.Now().UTC()})
}
func (s *LibraryStore) ScheduleStagedFaviconDeletion(ctx context.Context, ids []string, now time.Time) error {
	if s.Fail != nil {
		return s.Fail
	}
	for _, id := range ids {
		a, err := s.MediaAssets.Asset(ctx, id)
		if errors.Is(err, mediaasset.ErrAssetNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if a.Status == mediaasset.AssetReady || a.Status == mediaasset.AssetFailed {
			a.Status = mediaasset.AssetDeletePending
			a.DeleteRequestedAt = new(now)
			a.UpdatedAt = now
			s.MediaAssets.PutAsset(a)
		}
	}
	return nil
}

type FaviconCommits struct {
	Units                   mutation.UnitOfWork[domain.Store]
	BeginError, CommitError error
}

func (c FaviconCommits) Commit(ctx context.Context, change func(domain.Store) error) domain.FaviconCommitResult {
	if c.BeginError != nil {
		return domain.FaviconCommitResult{Stage: domain.FaviconBeginFailed, Err: c.BeginError}
	}
	if err := c.Units.Transaction(ctx, false, func(tx mutation.Tx[domain.Store]) error { return change(tx.Store()) }); err != nil {
		return domain.FaviconCommitResult{Stage: domain.FaviconChangeFailed, Err: err}
	}
	if c.CommitError != nil {
		return domain.FaviconCommitResult{Stage: domain.FaviconCommitFailed, Err: c.CommitError}
	}
	return domain.FaviconCommitResult{}
}
