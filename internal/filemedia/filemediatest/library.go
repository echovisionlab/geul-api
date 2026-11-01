package filemediatest

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	telemetry "github.com/echovisionlab/geul-telemetry"

	"github.com/echovisionlab/geul-api/internal/mediaasset/mediaassettest"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
)

type MeshRecord struct {
	Selected             bool
	CancelledAt          *time.Time
	ExpiresAt, UpdatedAt time.Time
	Witness              domain.MeshDeliveryWitness
	Status               string
}

type LibraryStore struct {
	ScopedOwners            map[string]ScopedOwner
	ScopedAttachments       []ScopedAttachmentRecord
	ValidatedPolicySegments [][]string
	Policies                map[string]PolicyRecord
	Segments                map[string]domain.PolicySegment
	PolicyChecks            []PolicyCheck
	PolicyDenied            map[string]error
	PolicySignals           []domain.PolicyChangeSignal
	PolicySignalError       error
	AssetIDs                map[string]bool
	Generations             map[string]mediaasset.Generation
	References              map[string][]string
	FaviconDeletions        []string
	DeletionOutbox          []domain.DeletionTarget
	Audits                  []telemetry.AuditRecord
	EnqueueError            error
	Usages                  []domain.FileUsageRow
	Slugs                   []domain.FileUsageSlug
	TrackReleases           map[string]string
	MeshRecords             map[string]MeshRecord
	Derivatives             map[string][]domain.StoredDerivativeDelivery
	MediaAssets             *mediaassettest.Store
	Failures                domain.ProcessingFailureCounts
	DeliveryUsages          []domain.CurrentDeliveryUsage
	IngestBindings          map[string]domain.IngestBinding
	PrincipalFence          *identitystatetest.Fence
	DeliveryLocks           []string
	ReadyMeshes             map[string]domain.MeshDeliveryWitness
	AvatarOwners            map[string]string
	DeliveryOwners          map[string]domain.DeliveryOwner
	ExactUsages             map[string]bool
	Folders                 map[string]domain.FileFolder
	Files                   map[string]CatalogFileRecord
	Fail                    error
	Writes                  int
}

func NewLibraryStore() *LibraryStore {
	assets, _ := mediaassettest.New()
	return &LibraryStore{ScopedOwners: map[string]ScopedOwner{}, Policies: map[string]PolicyRecord{}, Segments: map[string]domain.PolicySegment{}, PolicyDenied: map[string]error{}, AssetIDs: map[string]bool{}, Generations: map[string]mediaasset.Generation{}, References: map[string][]string{}, TrackReleases: map[string]string{}, MediaAssets: assets, MeshRecords: map[string]MeshRecord{}, Derivatives: map[string][]domain.StoredDerivativeDelivery{}, IngestBindings: map[string]domain.IngestBinding{}, PrincipalFence: &identitystatetest.Fence{}, ReadyMeshes: map[string]domain.MeshDeliveryWitness{}, AvatarOwners: map[string]string{}, DeliveryOwners: map[string]domain.DeliveryOwner{}, ExactUsages: map[string]bool{}, Files: map[string]CatalogFileRecord{}, Folders: map[string]domain.FileFolder{}}
}
func (s *LibraryStore) Savepoint() func() {
	assetIDs := maps.Clone(s.AssetIDs)
	saved := maps.Clone(s.Folders)
	files := maps.Clone(s.Files)
	writes := s.Writes
	policies, signals := clonePolicies(s.Policies), slices.Clone(s.PolicySignals)
	mesh, ready := maps.Clone(s.MeshRecords), maps.Clone(s.ReadyMeshes)
	assets := s.MediaAssets.Savepoint()
	favicon, outbox, audits := slices.Clone(s.FaviconDeletions), slices.Clone(s.DeletionOutbox), slices.Clone(s.Audits)
	return func() {
		s.AssetIDs = assetIDs
		s.Folders = saved
		s.Files = files
		s.Writes = writes
		s.Policies = policies
		s.PolicySignals = signals
		s.MeshRecords = mesh
		s.ReadyMeshes = ready
		assets()
		s.FaviconDeletions = favicon
		s.DeletionOutbox = outbox
		s.Audits = audits
	}
}
func (s *LibraryStore) FolderExists(_ context.Context, id string) (bool, error) {
	_, exists := s.Folders[id]
	return exists, s.Fail
}
func (s *LibraryStore) Folder(_ context.Context, id string) (domain.FileFolder, error) {
	if s.Fail != nil {
		return domain.FileFolder{}, s.Fail
	}
	row, ok := s.Folders[id]
	if !ok {
		return domain.FileFolder{}, domain.ErrFolderNotFound
	}
	return row, nil
}
func (s *LibraryStore) LockFolder(ctx context.Context, id string) (domain.FileFolder, error) {
	return s.Folder(ctx, id)
}
func (s *LibraryStore) LockFolderHierarchy(context.Context) error { return s.Fail }
func (s *LibraryStore) InsertFolder(_ context.Context, row domain.FileFolder) (domain.FileFolder, error) {
	if s.Fail != nil {
		return domain.FileFolder{}, s.Fail
	}
	if s.duplicate(row) {
		return domain.FileFolder{}, domain.ErrFolderNameExists
	}
	now := time.Now().UTC()
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = now
	}
	s.Folders[row.ID] = row
	s.Writes++
	return row, nil
}
func (s *LibraryStore) duplicate(row domain.FileFolder) bool {
	for id, value := range s.Folders {
		if id != row.ID && value.Name == row.Name && sameParent(value.ParentID, row.ParentID) {
			return true
		}
	}
	return false
}
func sameParent(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func (s *LibraryStore) FolderMoveCreatesCycle(_ context.Context, parent, id string) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	seen := map[string]bool{}
	for parent != "" && !seen[parent] {
		if parent == id {
			return true, nil
		}
		seen[parent] = true
		row, ok := s.Folders[parent]
		if !ok || row.ParentID == nil {
			break
		}
		parent = *row.ParentID
	}
	return false, nil
}
func (s *LibraryStore) RenameFolder(_ context.Context, id, name string) error {
	if s.Fail != nil {
		return s.Fail
	}
	row, ok := s.Folders[id]
	if !ok {
		return nil
	}
	row.Name = name
	if s.duplicate(row) {
		return domain.ErrFolderNameExists
	}
	row.UpdatedAt = time.Now().UTC()
	s.Folders[id] = row
	s.Writes++
	return nil
}
func (s *LibraryStore) MoveFolder(_ context.Context, id string, parent *string) error {
	if s.Fail != nil {
		return s.Fail
	}
	row, ok := s.Folders[id]
	if !ok {
		return nil
	}
	row.ParentID = parent
	if s.duplicate(row) {
		return domain.ErrFolderNameExists
	}
	row.UpdatedAt = time.Now().UTC()
	s.Folders[id] = row
	s.Writes++
	return nil
}
