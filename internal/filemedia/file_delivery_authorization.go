package filemedia

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type manageFileDeliveryAuthorization struct {
	principal *auth.UserInfo
	files     map[string]manageFileDeliveryGrant
}

type manageFileDeliveryGrant struct {
	lockFile            bool
	expectedUploader    string
	expectedAvatarOwner string
	usageWitnesses      []manageFileDeliveryUsageWitness
}

type manageFileDeliveryUsageWitness struct {
	kind             string
	fileID           string
	resourceType     string
	resourceID       string
	relationID       string
	referencePath    string
	ownerFingerprint string
}

func (w manageFileDeliveryUsageWitness) key() string {
	return w.kind + "\x00" + w.resourceType + "\x00" + w.resourceID + "\x00" + w.relationID + "\x00" + w.referencePath + "\x00" + w.fileID
}

func (s *Service) authorizeManageFileDeliveries(ctx context.Context, fileIDs []string) error {
	_, err := s.authorizeManageFileDeliveriesWithWitness(ctx, fileIDs)
	return err
}

func (s *Service) authorizeManageFileDeliveriesWithWitness(
	ctx context.Context,
	fileIDs []string,
) (*manageFileDeliveryAuthorization, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	if user.Banned {
		return nil, domainerr.AccountBanned()
	}
	uniqueFileIDs, err := sortedUniqueManageDeliveryFileIDs(fileIDs)
	if err != nil {
		return nil, err
	}
	fileByID, err := s.loadManageDeliveryFiles(ctx, uniqueFileIDs)
	if err != nil {
		return nil, err
	}
	listCan, err := policyv1.File.List()
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	hasLibraryAccess, err := checkSpiceDBCan(ctx, user, listCan, s.spiceDB)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	authorization := &manageFileDeliveryAuthorization{
		principal: user,
		files:     make(map[string]manageFileDeliveryGrant, len(uniqueFileIDs)),
	}
	if hasLibraryAccess {
		for _, fileID := range uniqueFileIDs {
			authorization.files[fileID] = manageFileDeliveryGrant{lockFile: true}
		}
		return authorization, nil
	}
	bindings, err := s.loadOptionalManageFileIngestBindings(ctx, uniqueFileIDs)
	if err != nil {
		return nil, err
	}
	usagesByFileID, err := s.currentManageFileDeliveryUsages(ctx, uniqueFileIDs)
	if err != nil {
		return nil, err
	}
	decided := make(map[string]error)
	for _, fileID := range uniqueFileIDs {
		if grant, owned := manageFileOwnerDeliveryGrant(user, fileByID[fileID], bindings[fileID]); owned {
			authorization.files[fileID] = grant
			continue
		}
		grant, err := s.manageFileUsageDeliveryGrant(ctx, user, usagesByFileID[fileID], decided)
		if err != nil {
			return nil, err
		}
		authorization.files[fileID] = grant
	}
	return authorization, nil
}

func sortedUniqueManageDeliveryFileIDs(fileIDs []string) ([]string, error) {
	uniqueFileIDs := make([]string, 0, len(fileIDs))
	seen := make(map[string]struct{}, len(fileIDs))
	for _, rawFileID := range fileIDs {
		fileID := strings.TrimSpace(rawFileID)
		if !IsValidUUID(fileID) {
			return nil, domainerr.InvalidField("file_id", "must be a valid UUID")
		}
		if _, ok := seen[fileID]; ok {
			continue
		}
		seen[fileID] = struct{}{}
		uniqueFileIDs = append(uniqueFileIDs, fileID)
	}
	sort.Strings(uniqueFileIDs)
	return uniqueFileIDs, nil
}

// loadManageDeliveryFiles reads the ownership of each File, denying the
// request when one is missing or pending deletion.
func (s *Service) loadManageDeliveryFiles(ctx context.Context, fileIDs []string) (map[string]File, error) {
	var files []File
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		files, err = store.DeliveryFileOwnership(ctx, fileIDs)
		return err
	}); err != nil {
		return nil, fmt.Errorf("load File ownership: %w", err)
	}
	fileByID := make(map[string]File, len(files))
	for _, file := range files {
		if file.DeleteRequestedAt != nil {
			continue
		}
		fileByID[file.ID] = file
	}
	for _, fileID := range fileIDs {
		if _, ok := fileByID[fileID]; !ok {
			return nil, domainerr.Denied("file access denied")
		}
	}
	return fileByID, nil
}

// manageFileOwnerDeliveryGrant grants the File's uploader, or the Member
// whose avatar it is, a delivery fenced on that ownership.
func manageFileOwnerDeliveryGrant(user *auth.UserInfo, file File, binding domain.IngestBinding) (manageFileDeliveryGrant, bool) {
	ownerKind := manageFileOwnerDeliveryKind(user, file, binding)
	if ownerKind == "" {
		return manageFileDeliveryGrant{}, false
	}
	grant := manageFileDeliveryGrant{lockFile: true}
	if ownerKind == "avatar" {
		grant.expectedAvatarOwner = user.MemberID.String()
	} else {
		grant.expectedUploader = user.MemberID.String()
	}
	return grant, true
}

// manageFileUsageDeliveryGrant grants a delivery witnessed by each usage
// whose owner the principal may manage, answering the first owner denial
// when there is none. decided holds each owner's answer across the request's
// Files, so an owner shared by several Files is checked once.
func (s *Service) manageFileUsageDeliveryGrant(
	ctx context.Context,
	user *auth.UserInfo,
	usages []manageFileDeliveryUsageWitness,
	decided map[string]error,
) (manageFileDeliveryGrant, error) {
	if len(usages) == 0 {
		return manageFileDeliveryGrant{}, domainerr.Denied("file access denied")
	}
	authorizedUsages := make([]manageFileDeliveryUsageWitness, 0, len(usages))
	var denied error
	for _, usage := range usages {
		target := manageFilePermissionTarget{resourceType: usage.resourceType, resourceID: usage.resourceID}
		answer, checked := decided[target.key()]
		if !checked {
			answer = s.authorizeManageFilePermissionTarget(ctx, user, target)
			if answer != nil && !mutation.IsDenied(answer) && !mutation.IsNotFound(answer) {
				return manageFileDeliveryGrant{}, answer
			}
			decided[target.key()] = answer
		}
		if answer == nil {
			authorizedUsages = append(authorizedUsages, usage)
		} else if denied == nil {
			denied = answer
		}
	}
	if len(authorizedUsages) == 0 {
		if denied != nil {
			return manageFileDeliveryGrant{}, denied
		}
		return manageFileDeliveryGrant{}, domainerr.Denied("file access denied")
	}
	return manageFileDeliveryGrant{usageWitnesses: authorizedUsages}, nil
}

// currentManageFileDeliveryUsages resolves exact current product references.
// Immutable ingest provenance is deliberately excluded: after detach it is no
// longer a delivery authority.
func (s *Service) currentManageFileDeliveryUsages(
	ctx context.Context,
	fileIDs []string,
) (map[string][]manageFileDeliveryUsageWitness, error) {
	result := make(map[string][]manageFileDeliveryUsageWitness)
	if len(fileIDs) == 0 {
		return result, nil
	}
	var rows []domain.CurrentDeliveryUsage
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.CurrentDeliveryUsages(ctx, fileIDs)
		return err
	}); err != nil {
		return nil, err
	}
	seen := make(map[string]map[string]struct{})
	for _, row := range rows {
		resourceType, ok := currentManageFileDeliveryResourceType(row.ResourceType)
		if row.FileID == "" || row.ResourceID == "" || !ok {
			continue
		}
		if seen[row.FileID] == nil {
			seen[row.FileID] = make(map[string]struct{})
		}
		witness := manageFileDeliveryUsageWitness{
			kind: row.Kind, fileID: row.FileID, resourceType: resourceType, resourceID: row.ResourceID,
			relationID: row.RelationID, referencePath: row.ReferencePath, ownerFingerprint: row.OwnerFingerprint,
		}
		if _, exists := seen[row.FileID][witness.key()]; exists {
			continue
		}
		seen[row.FileID][witness.key()] = struct{}{}
		result[row.FileID] = append(result[row.FileID], witness)
	}
	for fileID := range result {
		sort.Slice(result[fileID], func(i, j int) bool {
			return result[fileID][i].key() < result[fileID][j].key()
		})
	}
	return result, nil
}

func currentManageFileDeliveryResourceType(value string) (string, bool) {
	switch value {
	case "artist", "form", "label", "page", "post", "program_event", "release", "series", "work":
		return value, true
	default:
		return "", false
	}
}

func (s *Service) loadOptionalManageFileIngestBindings(
	ctx context.Context,
	fileIDs []string,
) (map[string]domain.IngestBinding, error) {
	result := make(map[string]domain.IngestBinding, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, nil
	}
	var bindings []domain.IngestBinding
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		bindings, err = store.DeliveryIngestBindings(ctx, fileIDs)
		return err
	}); err != nil {
		return nil, fmt.Errorf("load file ingest classification: %w", err)
	}
	for _, binding := range bindings {
		result[binding.FileID] = binding
	}
	return result, nil
}

func manageFileOwnerDeliveryKind(user *auth.UserInfo, file File, binding domain.IngestBinding) string {
	if user == nil {
		return ""
	}
	uploadTypeValue, ok := domain.ParseUploadType(binding.UploadType)
	if !ok {
		return ""
	}
	uploadType := UploadType(uploadTypeValue)
	if uploadType == UploadUserAvatar {
		if strings.TrimSpace(binding.EntityID) == user.MemberID.String() {
			return "avatar"
		}
		return ""
	}
	if file.UploadedByMemberID == nil || strings.TrimSpace(*file.UploadedByMemberID) != user.MemberID.String() {
		return ""
	}
	switch uploadType {
	case UploadGeneralFile,
		UploadEditorImage,
		UploadEditorVideo,
		UploadEditorAudio,
		UploadEditorAttachment,
		UploadEditorMesh:
		return "uploader"
	default:
		return ""
	}
}

type manageFilePermissionTarget struct {
	resourceType string
	resourceID   string
}

func (target manageFilePermissionTarget) key() string {
	return target.resourceType + "\x00" + target.resourceID
}

func (s *Service) authorizeManageFilePermissionTarget(
	ctx context.Context,
	user *auth.UserInfo,
	target manageFilePermissionTarget,
) error {
	if target.resourceType == "post" {
		return s.requireOwnerPreflight(ctx, "post", target.resourceID, false)
	}
	if target.resourceType == "program_event" {
		return s.requireOwnerPreflight(ctx, "program_event", target.resourceID, false)
	}
	adminCan, err := policyv1.File.ManageLibrary()
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	isAdmin, err := checkSpiceDBCan(ctx, user, adminCan, s.spiceDB)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if isAdmin {
		return nil
	}
	can, err := uploadPermissionEditCan(target.resourceType, target.resourceID)
	if errors.Is(err, errUnsupportedUploadAuthorizationResource) {
		return domainerr.Denied("file access denied")
	}
	if err != nil {
		return domainerr.Denied("file access denied").WithCause(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := s.spiceDB.Can(ctx, decision)
	if err != nil {
		return fmt.Errorf("permission check failed: %w", err)
	}
	if !allowed {
		return domainerr.Denied("file access denied")
	}
	return nil
}
