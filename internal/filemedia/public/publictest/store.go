package publictest

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/public"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaasset/mediaassettest"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
)

type AssetRecord struct {
	public.ReadyAsset
	Status    string
	CreatedAt time.Time
}
type GenerationRecord struct {
	public.ReadyGeneration
	Status string
}
type FileRecord struct {
	public.MediaFile
	PendingDeletion bool
}
type Relation struct {
	BlockID, ReferencePath, FileID, Kind string
	Owner                                public.OwnerWitness
	Access                               mediaasset.FileDownloadAccess
}
type Track struct {
	ID, ReleaseID, Status, FileID string
	Access                        mediaasset.FileDownloadAccess
}

// Store contains facts returned by the public File ports. Its queries never
// mutate them; locking is serialized by mutationtest's unit of work.
type Store struct {
	*mediaassettest.DownloadStore
	Assets           map[string]AssetRecord
	Generations      map[string]GenerationRecord
	Files            map[string]FileRecord
	DerivativeRows   []public.Derivative
	Relations        []Relation
	Tracks           map[string]Track
	OwnerRows        map[string]public.OwnerWitness
	BlockOwners      map[string][]public.OwnerWitness
	LinksByID        map[string]public.ShareLink
	PageReasons      map[string]pageaccess.Reason
	ActivePrincipals map[string]bool
	Locks            []string
}

func NewStore() *Store {
	return &Store{DownloadStore: mediaassettest.NewDownload(), Assets: map[string]AssetRecord{}, Generations: map[string]GenerationRecord{}, Files: map[string]FileRecord{}, Tracks: map[string]Track{}, OwnerRows: map[string]public.OwnerWitness{}, BlockOwners: map[string][]public.OwnerWitness{}, LinksByID: map[string]public.ShareLink{}, PageReasons: map[string]pageaccess.Reason{}, ActivePrincipals: map[string]bool{}}
}
func (s *Store) Savepoint() mutationtest.Checkpoint {
	return mutationtest.Checkpoint{State: struct{}{}, Restore: func() {}}
}
func (s *Store) ReadyAsset(_ context.Context, id string) (public.ReadyAsset, error) {
	if s.Fail != nil {
		return public.ReadyAsset{}, s.Fail
	}
	row, ok := s.Assets[strings.TrimSpace(id)]
	if !ok || row.Status != "ready" {
		return public.ReadyAsset{}, public.ErrAssetNotFound
	}
	return row.ReadyAsset, nil
}
func (s *Store) ReadyAssetsForFiles(_ context.Context, ids []string, kinds ...string) (map[string]public.ReadyAsset, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []AssetRecord
	for _, row := range s.Assets {
		if row.Status == "ready" && row.SourceFileID != nil && containsID(ids, *row.SourceFileID) && (len(kinds) == 0 || slices.Contains(kinds, row.Kind)) {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b AssetRecord) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.AssetID, a.AssetID)
	})
	result := map[string]public.ReadyAsset{}
	for _, row := range rows {
		if _, ok := result[*row.SourceFileID]; !ok {
			result[*row.SourceFileID] = row.ReadyAsset
		}
	}
	return result, nil
}
func (s *Store) ReadyAssets(ctx context.Context, ids []string) (map[string]public.ReadyAsset, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := map[string]public.ReadyAsset{}
	for _, id := range ids {
		row, err := s.ReadyAsset(ctx, id)
		if err == nil {
			result[row.AssetID] = row
		}
	}
	return result, nil
}
func (s *Store) ReadyGenerations(_ context.Context, ids []string) (map[string]public.ReadyGeneration, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := map[string]public.ReadyGeneration{}
	for id, row := range s.Generations {
		if containsID(ids, id) && row.Status == "ready" {
			result[id] = row.ReadyGeneration
		}
	}
	return result, nil
}
func (s *Store) MediaFiles(_ context.Context, ids []string) ([]public.MediaFile, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var result []public.MediaFile
	for id, row := range s.Files {
		if slices.Contains(ids, id) && !row.PendingDeletion {
			result = append(result, row.MediaFile)
		}
	}
	return result, nil
}
func (s *Store) Derivatives(_ context.Context, ids []string) ([]public.Derivative, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var result []public.Derivative
	for _, row := range s.DerivativeRows {
		if slices.Contains(ids, row.FileID) {
			result = append(result, row)
		}
	}
	return result, nil
}
func ownerKind(kind public.MediaEntityType) string {
	switch kind {
	case public.EntityPost:
		return "post"
	case public.EntityPage:
		return "page"
	case public.EntityWork:
		return "work"
	case public.EntityProgramEvent:
		return "program_event"
	case public.EntityRelease:
		return "release"
	default:
		return ""
	}
}
func (s *Store) LockContentOwner(_ context.Context, kind public.MediaEntityType, id string) (string, bool, error) {
	if s.Fail != nil {
		return "", false, s.Fail
	}
	name := ownerKind(kind)
	if name == "" || kind == public.EntityRelease {
		return "", false, domainerr.InvalidField("entity_type", "unsupported Content Block owner")
	}
	s.Locks = append(s.Locks, "owner")
	row, ok := s.OwnerRows[name+":"+id]
	return row.Status, ok, nil
}
func (s *Store) LockReleaseOwner(_ context.Context, id string) (string, bool, error) {
	if s.Fail != nil {
		return "", false, s.Fail
	}
	s.Locks = append(s.Locks, "owner")
	row, ok := s.OwnerRows["release:"+id]
	return row.Status, ok, nil
}
func (s *Store) ResolveContentSource(ctx context.Context, kind public.MediaEntityType, id, block, path string, lock bool) (mediaasset.FileDownloadSource, string, bool, error) {
	if s.Fail != nil {
		return mediaasset.FileDownloadSource{}, "", false, s.Fail
	}
	name := ownerKind(kind)
	if name == "" || kind == public.EntityRelease {
		return mediaasset.FileDownloadSource{}, "", false, domainerr.InvalidField("entity_type", "unsupported Content Block owner")
	}
	if lock {
		s.Locks = append(s.Locks, "relation")
	}
	for _, r := range s.Relations {
		if r.BlockID == block && r.ReferencePath == path && r.Kind == "file" && r.Owner.ResourceType == name && r.Owner.ResourceID == id {
			row, exists := s.OwnerRows[name+":"+id]
			if !exists {
				return mediaasset.FileDownloadSource{}, "", false, nil
			}
			source, ok := s.contentSource(r)
			return source, row.Status, ok, nil
		}
	}
	return mediaasset.FileDownloadSource{}, "", false, nil
}
func (s *Store) contentSource(r Relation) (mediaasset.FileDownloadSource, bool) {
	f, ok := s.Files[r.FileID]
	if !ok || f.PendingDeletion {
		return mediaasset.FileDownloadSource{}, false
	}
	return mediaasset.FileDownloadSource{PolicyKey: mediaasset.ContentBlockDownloadPolicyKey(r.BlockID, r.ReferencePath), PolicyKind: mediaasset.FileDownloadPolicyContentBlock, FileID: r.FileID, BlockID: r.BlockID, ReferencePath: r.ReferencePath, Extension: f.Extension, MimeType: f.MimeType, FileName: f.FileName, FileSize: f.FileSize, Access: r.Access}, r.Kind == "file"
}
func (s *Store) ContentSources(_ context.Context, ids []string) ([]mediaasset.FileDownloadSource, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.FileDownloadSource
	for _, r := range s.Relations {
		if slices.Contains(ids, r.BlockID) {
			row, ok := s.contentSource(r)
			if ok {
				rows = append(rows, row)
			}
		}
	}
	return rows, nil
}
func (s *Store) ResolveTrackSource(_ context.Context, release, id string, lock bool) (mediaasset.FileDownloadSource, string, bool, error) {
	if s.Fail != nil {
		return mediaasset.FileDownloadSource{}, "", false, s.Fail
	}
	if lock {
		s.Locks = append(s.Locks, "relation")
	}
	track, ok := s.Tracks[id]
	if !ok || track.ReleaseID != release || track.FileID == "" {
		return mediaasset.FileDownloadSource{}, "", false, nil
	}
	owner, ok := s.OwnerRows["release:"+release]
	if !ok {
		return mediaasset.FileDownloadSource{}, "", false, nil
	}
	file, ok := s.Files[track.FileID]
	if !ok || file.PendingDeletion {
		return mediaasset.FileDownloadSource{}, owner.Status, false, nil
	}
	return mediaasset.FileDownloadSource{PolicyKey: id, PolicyKind: mediaasset.FileDownloadPolicyTrack, TrackID: id, FileID: track.FileID, Extension: file.Extension, MimeType: file.MimeType, FileName: file.FileName, FileSize: file.FileSize, Access: track.Access}, owner.Status, true, nil
}
func (s *Store) PageAccess(_ context.Context, _ authz.AuthorizationDecisionChecker, id string) (pageaccess.Reason, error) {
	return s.PageReasons[id], s.Fail
}
func (s *Store) LockActivePrincipal(_ context.Context, user *auth.UserInfo) (bool, error) {
	s.Locks = append(s.Locks, "principal")
	return user != nil && s.ActivePrincipals[user.IdentityID.String()+":"+user.MemberID.String()], s.Fail
}
func (s *Store) ShareLink(_ context.Context, id string, lock bool) (public.ShareLink, bool, error) {
	if lock {
		s.Locks = append(s.Locks, "share")
	}
	row, ok := s.LinksByID[id]
	return row, ok, s.Fail
}
func (s *Store) ContentOwners(_ context.Context, selectors []mediaasset.ContentBlockDownloadSelector) (map[string]public.OwnerWitness, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := map[string]public.OwnerWitness{}
	for _, selector := range selectors {
		owners := s.BlockOwners[strings.TrimSpace(selector.BlockID)]
		if len(owners) == 1 {
			result[strings.TrimSpace(selector.BlockID)] = owners[0]
		}
	}
	return result, nil
}
func (s *Store) LockContentOwners(_ context.Context, expected map[string]public.OwnerWitness) (map[string]public.OwnerWitness, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	s.Locks = append(s.Locks, "owner")
	result := map[string]public.OwnerWitness{}
	for id, owner := range expected {
		if current, ok := s.OwnerRows[owner.ResourceType+":"+owner.ResourceID]; ok && current == owner {
			result[id] = current
		}
	}
	return result, nil
}
func (s *Store) ContentRelations(_ context.Context, selectors []mediaasset.ContentBlockDownloadSelector, lock bool) (map[string]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	if lock {
		s.Locks = append(s.Locks, "relation")
	}
	result := map[string]string{}
	for _, selector := range selectors {
		for _, r := range s.Relations {
			f, ok := s.Files[r.FileID]
			if ok && !f.PendingDeletion && r.BlockID == strings.TrimSpace(selector.BlockID) && r.ReferencePath == strings.TrimSpace(selector.ReferencePath) {
				result[mediaasset.ContentBlockDownloadPolicyKey(r.BlockID, r.ReferencePath)] = r.FileID
			}
		}
	}
	return result, nil
}
func (s *Store) LockCriteriaSet(ctx context.Context, ids []string) error {
	s.Locks = append(s.Locks, "criteria")
	return s.DownloadStore.LockCriteriaSet(ctx, ids)
}
func (s *Store) LockViewer(ctx context.Context, user *auth.UserInfo, authenticated bool) (bool, error) {
	s.Locks = append(s.Locks, "viewer")
	return s.DownloadStore.LockViewer(ctx, user, authenticated)
}
func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if strings.TrimSpace(v) == id {
			return true
		}
	}
	return false
}
