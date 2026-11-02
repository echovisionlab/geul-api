package filemediatest

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

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
	MeshCandidateRows       map[string]domain.MeshCandidate
	MeshJobs                []domain.MeshJob
	MeshJobError            error
	MeshPublisherError      error
	IngestCommands          []domain.IngestTranscodeCommand
	IngestTranscodeError    error
	TrackOriginals          map[string]string
	UploadTracks            map[string]bool
	UploadSessions          map[string]domain.UploadSession
	UploadPartRows          map[string][]domain.UploadPart
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
	return &LibraryStore{MeshCandidateRows: map[string]domain.MeshCandidate{}, UploadSessions: map[string]domain.UploadSession{}, UploadPartRows: map[string][]domain.UploadPart{}, ScopedOwners: map[string]ScopedOwner{}, Policies: map[string]PolicyRecord{}, Segments: map[string]domain.PolicySegment{}, PolicyDenied: map[string]error{}, AssetIDs: map[string]bool{}, Generations: map[string]mediaasset.Generation{}, References: map[string][]string{}, TrackReleases: map[string]string{}, MediaAssets: assets, MeshRecords: map[string]MeshRecord{}, Derivatives: map[string][]domain.StoredDerivativeDelivery{}, IngestBindings: map[string]domain.IngestBinding{}, PrincipalFence: &identitystatetest.Fence{}, ReadyMeshes: map[string]domain.MeshDeliveryWitness{}, AvatarOwners: map[string]string{}, DeliveryOwners: map[string]domain.DeliveryOwner{}, ExactUsages: map[string]bool{}, Files: map[string]CatalogFileRecord{}, Folders: map[string]domain.FileFolder{}}
}
func (s *LibraryStore) Savepoint() mutationtest.Checkpoint {
	assets := s.MediaAssets.Savepoint()
	meshCandidateRows := mutationtest.ClonePersisted(s.MeshCandidateRows)
	meshJobs := mutationtest.ClonePersisted(s.MeshJobs)
	ingestCommands := mutationtest.ClonePersisted(s.IngestCommands)
	trackOriginals := mutationtest.ClonePersisted(s.TrackOriginals)
	generations := mutationtest.ClonePersisted(s.Generations)
	derivatives := mutationtest.ClonePersisted(s.Derivatives)
	ingestBindings := mutationtest.ClonePersisted(s.IngestBindings)
	uploadSessions := cloneUploads(s.UploadSessions)
	uploadPartRows := cloneUploadParts(s.UploadPartRows)
	assetIDs := mutationtest.ClonePersisted(s.AssetIDs)
	folders := mutationtest.ClonePersisted(s.Folders)
	files := mutationtest.ClonePersisted(s.Files)
	policies := clonePolicies(s.Policies)
	policySignals := mutationtest.ClonePersisted(s.PolicySignals)
	meshRecords := mutationtest.ClonePersisted(s.MeshRecords)
	readyMeshes := mutationtest.ClonePersisted(s.ReadyMeshes)
	faviconDeletions := mutationtest.ClonePersisted(s.FaviconDeletions)
	deletionOutbox := mutationtest.ClonePersisted(s.DeletionOutbox)
	audits := mutationtest.ClonePersisted(s.Audits)
	writes := s.Writes
	return mutationtest.Checkpoint{
		State: map[string]any{
			"MeshCandidateRows": meshCandidateRows,
			"MeshJobs":          meshJobs,
			"IngestCommands":    ingestCommands,
			"TrackOriginals":    trackOriginals,
			"Generations":       generations,
			"Derivatives":       derivatives,
			"IngestBindings":    ingestBindings,
			"UploadSessions":    uploadSessions,
			"UploadPartRows":    uploadPartRows,
			"AssetIDs":          assetIDs,
			"Folders":           folders,
			"Files":             files,
			"Policies":          policies,
			"PolicySignals":     policySignals,
			"MeshRecords":       meshRecords,
			"ReadyMeshes":       readyMeshes,
			"FaviconDeletions":  faviconDeletions,
			"DeletionOutbox":    deletionOutbox,
			"Audits":            audits,
			"MediaAssets":       assets.State,
		},
		Restore: func() {
			assets.Restore()
			s.MeshCandidateRows = meshCandidateRows
			s.MeshJobs = meshJobs
			s.IngestCommands = ingestCommands
			s.TrackOriginals = trackOriginals
			s.Generations = generations
			s.Derivatives = derivatives
			s.IngestBindings = ingestBindings
			s.UploadSessions = uploadSessions
			s.UploadPartRows = uploadPartRows
			s.AssetIDs = assetIDs
			s.Folders = folders
			s.Files = files
			s.Policies = policies
			s.PolicySignals = policySignals
			s.MeshRecords = meshRecords
			s.ReadyMeshes = readyMeshes
			s.FaviconDeletions = faviconDeletions
			s.DeletionOutbox = deletionOutbox
			s.Audits = audits
			s.Writes = writes
		},
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
