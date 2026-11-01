package filemediapublicpostgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	public "github.com/echovisionlab/geul-api/internal/filemedia/public"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	pageaccesspostgres "github.com/echovisionlab/geul-api/internal/pageaccess/postgres"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Ports struct {
	Segments mediaassetpostgres.SegmentConfigLoader
}
type store struct {
	tx *gorm.DB
	mediaasset.DownloadStore
}

func NewUnits(db *gorm.DB, ports Ports) mutation.UnitOfWork[public.Store] {
	if db == nil {
		panic("public file media units: db is required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) public.Store { return Bind(tx, ports) })
}
func Bind(tx *gorm.DB, ports Ports) public.Store {
	return &store{tx: tx, DownloadStore: mediaassetpostgres.BindDownload(tx, ports.Segments)}
}

func readyPublicAssetSelect(alias string) string {
	return fmt.Sprintf(`
		%s.id AS asset_id,
		%s.source_file_id,
		%s.kind,
		%s.extension,
		%s.mime_type,
		%s.file_size,
		%s.sha256,
		%s.disposition,
		%s.download_filename
	`, alias, alias, alias, alias, alias, alias, alias, alias, alias)
}

func (s *store) ReadyAsset(
	ctx context.Context,
	assetID string,
) (public.ReadyAsset, error) {
	var row public.ReadyAsset
	err := s.tx.WithContext(ctx).
		Table("public_asset AS pa").
		Select(readyPublicAssetSelect("pa")).
		Where("pa.id = ? AND pa.status = 'ready'", strings.TrimSpace(assetID)).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = public.ErrAssetNotFound
	}
	return row, err
}

func (s *store) ReadyAssetsForFiles(
	ctx context.Context,
	sourceFileIDs []string,
	kinds ...string,
) (map[string]public.ReadyAsset, error) {
	ids := sliceutil.UniqueTrimmed(sourceFileIDs)
	result := make(map[string]public.ReadyAsset, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	query := s.tx.WithContext(ctx).
		Table("public_asset AS pa").
		Select(readyPublicAssetSelect("pa")).
		Where("pa.source_file_id IN ? AND pa.status = 'ready'", ids)
	if len(kinds) > 0 {
		query = query.Where("pa.kind IN ?", kinds)
	}

	var rows []public.ReadyAsset
	if err := query.Order("pa.created_at DESC, pa.id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.SourceFileID == nil {
			continue
		}
		if _, exists := result[*row.SourceFileID]; !exists {
			result[*row.SourceFileID] = row
		}
	}
	return result, nil
}

func (s *store) ReadyAssets(
	ctx context.Context,
	assetIDs []string,
) (map[string]public.ReadyAsset, error) {
	ids := sliceutil.UniqueTrimmed(assetIDs)
	result := make(map[string]public.ReadyAsset, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []public.ReadyAsset
	if err := s.tx.WithContext(ctx).
		Table("public_asset AS pa").
		Select(readyPublicAssetSelect("pa")).
		Where("pa.id IN ? AND pa.status = 'ready'", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.AssetID] = row
	}
	return result, nil
}

func (s *store) ReadyGenerations(
	ctx context.Context,
	generationIDs []string,
) (map[string]public.ReadyGeneration, error) {
	ids := sliceutil.UniqueTrimmed(generationIDs)
	result := make(map[string]public.ReadyGeneration, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []public.ReadyGeneration
	if err := s.tx.WithContext(ctx).
		Table("media_generation AS mg").
		Select("mg.file_id, mg.id AS generation_id, mg.manifest_name").
		Where("mg.id IN ? AND mg.status = 'ready'", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.GenerationID] = row
	}
	return result, nil
}

func (s *store) LockContentOwner(
	ctx context.Context,
	entityType public.MediaEntityType,
	entityID string,
) (string, bool, error) {
	ownerTable, ok := contentDownloadOwnerTable(entityType)
	if !ok {
		return "", false, domainerr.InvalidField("entity_type", "unsupported Content Block owner")
	}
	var owner struct {
		Status string `gorm:"column:status"`
	}
	result := s.tx.WithContext(ctx).Table(ownerTable).
		Select("status").Where("id = ?", entityID).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Take(&owner)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if result.Error != nil {
		return "", false, result.Error
	}
	return owner.Status, true, nil
}

func (s *store) LockReleaseOwner(
	ctx context.Context,
	releaseID string,
) (string, bool, error) {
	var owner struct {
		Status string `gorm:"column:status"`
	}
	result := s.tx.WithContext(ctx).Table("release").
		Select("status").Where("id = ?", releaseID).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Take(&owner)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if result.Error != nil {
		return "", false, result.Error
	}
	return owner.Status, true, nil
}

func (s *store) ResolveContentSource(
	ctx context.Context,
	entityType public.MediaEntityType,
	entityID string,
	blockID string,
	referencePath string,
	lock bool,
) (mediaasset.FileDownloadSource, string, bool, error) {
	ownerTable, ok := contentDownloadOwnerTable(entityType)
	if !ok {
		return mediaasset.FileDownloadSource{}, "", false, domainerr.InvalidField("entity_type", "unsupported Content Block owner")
	}

	var owner struct {
		Status string `gorm:"column:status"`
	}
	query := s.tx.WithContext(ctx).
		Table("content_block_attachment AS cbf").
		Select("owner.status").
		Joins("JOIN content_block AS cb ON cb.id = cbf.block_id").
		Joins("JOIN "+ownerTable+" AS owner ON owner.content_document_id = cb.document_id").
		Where("owner.id = ? AND cb.id = ? AND cb.kind = 'file' AND cbf.reference_path = ? AND cbf.selector_kind = 'active'", entityID, blockID, referencePath)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	result := query.Take(&owner)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return mediaasset.FileDownloadSource{}, "", false, nil
		}
		return mediaasset.FileDownloadSource{}, "", false, result.Error
	}
	sources, err := mediaasset.LoadContentBlockDownloadSources(ctx, s.DownloadStore, []mediaasset.ContentBlockDownloadSelector{{
		BlockID: blockID, ReferencePath: referencePath,
	}})
	if err != nil {
		return mediaasset.FileDownloadSource{}, "", false, err
	}
	source, found := sources[mediaasset.ContentBlockDownloadPolicyKey(blockID, referencePath)]
	return source, owner.Status, found, nil
}

func (s *store) ResolveTrackSource(
	ctx context.Context,
	releaseID string,
	trackID string,
	lock bool,
) (mediaasset.FileDownloadSource, string, bool, error) {
	var owner struct {
		Status string `gorm:"column:status"`
	}
	query := s.tx.WithContext(ctx).
		Table("track").
		Select("release.status").
		Joins("JOIN release ON release.id = track.release_id").
		Where("track.id = ? AND release.id = ? AND track.audio_original_file_id IS NOT NULL", trackID, releaseID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	result := query.Take(&owner)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return mediaasset.FileDownloadSource{}, "", false, nil
	}
	if result.Error != nil {
		return mediaasset.FileDownloadSource{}, "", false, result.Error
	}
	sources, err := mediaasset.LoadTrackDownloadSources(ctx, s.DownloadStore, []string{trackID})
	if err != nil {
		return mediaasset.FileDownloadSource{}, "", false, err
	}
	source, found := sources[trackID]
	return source, owner.Status, found, nil
}

func contentDownloadOwnerTable(entityType public.MediaEntityType) (string, bool) {
	switch entityType {
	case public.EntityPost:
		return "post", true
	case public.EntityPage:
		return "page", true
	case public.EntityWork:
		return "work", true
	case public.EntityProgramEvent:
		return "program_event", true
	default:
		return "", false
	}
}

func (s *store) ContentOwners(
	ctx context.Context,
	selectors []mediaasset.ContentBlockDownloadSelector,
) (map[string]public.OwnerWitness, error) {
	blockIDs := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		if blockID := strings.TrimSpace(selector.BlockID); blockID != "" {
			blockIDs = append(blockIDs, blockID)
		}
	}
	if len(blockIDs) == 0 {
		return map[string]public.OwnerWitness{}, nil
	}
	type row struct {
		BlockID      string `gorm:"column:block_id"`
		ResourceType string `gorm:"column:resource_type"`
		ResourceID   string `gorm:"column:resource_id"`
		Status       string `gorm:"column:status"`
		DocumentID   string `gorm:"column:document_id"`
	}
	var rows []row
	if err := s.tx.WithContext(ctx).Raw(`
		SELECT block.id AS block_id, 'post' AS resource_type, owner.id AS resource_id, CAST(owner.status AS TEXT) AS status, owner.content_document_id AS document_id
		FROM content_block AS block JOIN post AS owner ON owner.content_document_id = block.document_id WHERE block.id IN ?
		UNION ALL
		SELECT block.id, 'page', owner.id, CAST(owner.status AS TEXT), owner.content_document_id FROM content_block AS block JOIN page AS owner ON owner.content_document_id = block.document_id WHERE block.id IN ?
		UNION ALL
		SELECT block.id, 'work', owner.id, CAST(owner.status AS TEXT), owner.content_document_id FROM content_block AS block JOIN work AS owner ON owner.content_document_id = block.document_id WHERE block.id IN ?
		UNION ALL
		SELECT block.id, 'program_event', owner.id, CAST(owner.status AS TEXT), owner.content_document_id FROM content_block AS block JOIN program_event AS owner ON owner.content_document_id = block.document_id WHERE block.id IN ?
	`, blockIDs, blockIDs, blockIDs, blockIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	result := make(map[string]public.OwnerWitness, len(rows))
	for _, row := range rows {
		counts[row.BlockID]++
		result[row.BlockID] = public.OwnerWitness{
			ResourceType: row.ResourceType, ResourceID: row.ResourceID,
			Status: row.Status, DocumentID: row.DocumentID,
		}
	}
	for blockID, count := range counts {
		if count != 1 {
			delete(result, blockID)
		}
	}
	return result, nil
}

func (s *store) LockContentOwners(
	ctx context.Context,
	expected map[string]public.OwnerWitness,
) (map[string]public.OwnerWitness, error) {
	type target struct {
		BlockID string
		public.OwnerWitness
	}
	targets := make([]target, 0, len(expected))
	for blockID, owner := range expected {
		targets = append(targets, target{BlockID: blockID, OwnerWitness: owner})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].ResourceType == targets[j].ResourceType {
			return targets[i].ResourceID < targets[j].ResourceID
		}
		return targets[i].ResourceType < targets[j].ResourceType
	})
	result := make(map[string]public.OwnerWitness, len(targets))
	for _, target := range targets {
		var current public.OwnerWitness
		query := s.tx.WithContext(ctx).Table(target.ResourceType).
			Select("? AS resource_type, id AS resource_id, status, content_document_id AS document_id", target.ResourceType).
			Where("id = ?", target.ResourceID).
			Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Take(&current)
		if query.Error != nil {
			if errors.Is(query.Error, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, query.Error
		}
		if current == target.OwnerWitness {
			result[target.BlockID] = current
		}
	}
	return result, nil
}

func (s *store) ContentRelations(
	ctx context.Context,
	selectors []mediaasset.ContentBlockDownloadSelector,
	locked bool,
) (map[string]string, error) {
	requested := make(map[string]struct{}, len(selectors))
	blockIDs := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		blockID := strings.TrimSpace(selector.BlockID)
		referencePath := strings.TrimSpace(selector.ReferencePath)
		if blockID != "" && referencePath != "" {
			requested[mediaasset.ContentBlockDownloadPolicyKey(blockID, referencePath)] = struct{}{}
			blockIDs = append(blockIDs, blockID)
		}
	}
	if len(blockIDs) == 0 {
		return map[string]string{}, nil
	}
	var rows []struct {
		BlockID       string `gorm:"column:block_id"`
		ReferencePath string `gorm:"column:reference_path"`
		FileID        string `gorm:"column:file_id"`
	}
	query := s.tx.WithContext(ctx).
		Table("content_block_attachment AS attachment").
		Select("attachment.block_id, attachment.reference_path, attachment.file_id").
		Joins("JOIN file ON file.id = attachment.file_id").
		Where("attachment.block_id IN ? AND attachment.selector_kind = 'active' AND file.delete_requested_at IS NULL", blockIDs).
		Order("attachment.block_id, attachment.reference_path")
	if locked {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare, Table: clause.Table{Name: "attachment"}})
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		key := mediaasset.ContentBlockDownloadPolicyKey(row.BlockID, row.ReferencePath)
		if _, ok := requested[key]; ok {
			result[key] = row.FileID
		}
	}
	return result, nil
}

func (s *store) MediaFiles(ctx context.Context, ids []string) ([]public.MediaFile, error) {
	var rows []public.MediaFile
	err := s.tx.WithContext(ctx).Table("file").Select("id, extension, mime_type, file_size, file_name, duration_seconds").Where("id IN ? AND delete_requested_at IS NULL", ids).Find(&rows).Error
	return rows, err
}

func (s *store) Derivatives(ctx context.Context, ids []string) ([]public.Derivative, error) {
	var rows []public.Derivative
	err := s.tx.WithContext(ctx).Table("file_derivative").Select("file_id, type, asset_id, media_generation_id").Where("file_id IN ?", ids).Find(&rows).Error
	return rows, err
}

func (s *store) PageAccess(ctx context.Context, checker authz.AuthorizationDecisionChecker, id string) (pageaccess.Reason, error) {
	return pageaccesspostgres.EvaluateStored(ctx, s.tx, checker, id)
}

func (s *store) LockActivePrincipal(ctx context.Context, user *auth.UserInfo) (bool, error) {
	return identitystate.LockActivePrincipal(ctx, s.tx, user)
}

func (s *store) ShareLink(ctx context.Context, id string, lock bool) (public.ShareLink, bool, error) {
	var row model.ShareLink
	q := s.tx.WithContext(ctx).Where("id = ?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return public.ShareLink{}, false, nil
	}
	return public.ShareLink{ID: row.ID, EntityType: row.EntityType, EntityID: row.EntityID, ExpiresAt: row.ExpiresAt}, err == nil, err
}
