package filemedia

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const maxFileDownloadMemberCriteria = 20

type filePolicyOwnerAction uint8

const (
	filePolicyOwnerView filePolicyOwnerAction = iota + 1
	filePolicyOwnerEdit
)

type fileDownloadPolicySelector struct {
	EntityType    EntityType
	EntityID      string
	BlockID       *string
	ReferencePath *string
}

type fileDownloadPolicyRelation struct {
	Selector     fileDownloadPolicySelector
	FileID       string
	Access       mediaasset.FileDownloadAccess
	ResourceType string
	ResourceID   string
}

// FileDownloadPolicyTarget names one File attachment: a File Block of a
// document, or a Track's original audio.
type FileDownloadPolicyTarget struct {
	EntityType    EntityType
	EntityID      string
	BlockID       *string
	ReferencePath *string
}

func (t FileDownloadPolicyTarget) selector() (fileDownloadPolicySelector, error) {
	return validateFileDownloadPolicySelector(t.EntityType, t.EntityID, t.BlockID, t.ReferencePath)
}

// GetFileDownloadPolicyQuery reads the download policy of one attachment.
type GetFileDownloadPolicyQuery struct {
	Target FileDownloadPolicyTarget
}

func (s *Service) GetFileDownloadPolicy(ctx context.Context, query GetFileDownloadPolicyQuery) (*DownloadPolicy, error) {
	selector, err := query.Target.selector()
	if err != nil {
		return nil, err
	}
	var policy *DownloadPolicy
	err = s.units.ReadLocked(ctx, func(tx Store) error {
		relation, resolveErr := s.resolveFileDownloadPolicyRelation(ctx, tx, selector, false)
		if resolveErr != nil {
			return resolveErr
		}
		if authorizeErr := s.authorizeFileDownloadPolicyOwner(ctx, tx, relation, filePolicyOwnerView); authorizeErr != nil {
			return authorizeErr
		}
		var loadErr error
		policy, loadErr = s.loadManageFileDownloadPolicy(ctx, tx, relation)
		return loadErr
	})
	if err != nil {
		return nil, err
	}
	return policy, nil
}

// FileDownloadPolicyState is a download access with its Member criteria.
type FileDownloadPolicyState struct {
	Access            DownloadAccess
	MemberCriteriaIDs []string
}

// UpdateFileDownloadPolicyCommand sets the download policy of the attachment
// that still holds ExpectedFileID. Observed is the policy the caller read; a
// concurrent change to a field the caller did not change is kept.
type UpdateFileDownloadPolicyCommand struct {
	Target         FileDownloadPolicyTarget
	ExpectedFileID string
	Policy         FileDownloadPolicyState
	// Observed is required.
	Observed *FileDownloadPolicyState
}

// fileDownloadPolicyChange is a validated UpdateFileDownloadPolicyCommand.
type fileDownloadPolicyChange struct {
	selector            fileDownloadPolicySelector
	expectedFileID      string
	access              mediaasset.FileDownloadAccess
	criteriaIDs         []string
	observedAccess      mediaasset.FileDownloadAccess
	observedCriteriaIDs []string
}

func validateFileDownloadPolicyChange(command UpdateFileDownloadPolicyCommand) (fileDownloadPolicyChange, error) {
	selector, err := command.Target.selector()
	if err != nil {
		return fileDownloadPolicyChange{}, err
	}
	parsedExpectedFileID, parseErr := uuid.Parse(strings.TrimSpace(command.ExpectedFileID))
	if parseErr != nil {
		return fileDownloadPolicyChange{}, domainerr.InvalidField("expected_file_id", "must be a valid UUID")
	}
	access, ok := storedFileDownloadAccess(command.Policy.Access)
	if !ok {
		return fileDownloadPolicyChange{}, domainerr.InvalidField("access", "unsupported file download access")
	}
	criteriaIDs, err := uniqueUUIDs(command.Policy.MemberCriteriaIDs, "member_criteria_ids")
	if err != nil {
		return fileDownloadPolicyChange{}, err
	}
	if command.Observed == nil {
		return fileDownloadPolicyChange{}, domainerr.InvalidField("observed_policy", "is required")
	}
	observedAccess, ok := storedFileDownloadAccess(command.Observed.Access)
	if !ok {
		return fileDownloadPolicyChange{}, domainerr.InvalidField("observed_policy.access", "unsupported file download access")
	}
	observedCriteriaIDs, err := uniqueUUIDs(command.Observed.MemberCriteriaIDs, "observed_policy.member_criteria_ids")
	if err != nil {
		return fileDownloadPolicyChange{}, err
	}
	if len(observedCriteriaIDs) > maxFileDownloadMemberCriteria {
		return fileDownloadPolicyChange{}, tooManyFileDownloadMemberCriteria("observed_policy.member_criteria_ids")
	}
	if len(criteriaIDs) > maxFileDownloadMemberCriteria {
		return fileDownloadPolicyChange{}, tooManyFileDownloadMemberCriteria("member_criteria_ids")
	}
	if access != mediaasset.FileDownloadAccessRestricted && len(criteriaIDs) > 0 {
		return fileDownloadPolicyChange{}, domainerr.InvalidField("member_criteria_ids", "Member criteria require restricted access")
	}
	slices.Sort(criteriaIDs)
	return fileDownloadPolicyChange{
		selector: selector, expectedFileID: parsedExpectedFileID.String(),
		access: access, criteriaIDs: criteriaIDs,
		observedAccess: observedAccess, observedCriteriaIDs: observedCriteriaIDs,
	}, nil
}

func tooManyFileDownloadMemberCriteria(field string) error {
	return domainerr.InvalidField(field, fmt.Sprintf("at most %d Member criteria are allowed", maxFileDownloadMemberCriteria))
}

func (s *Service) UpdateFileDownloadPolicy(ctx context.Context, command UpdateFileDownloadPolicyCommand) (*DownloadPolicy, error) {
	change, err := validateFileDownloadPolicyChange(command)
	if err != nil {
		return nil, err
	}
	if err := requireFileDownloadPolicyActor(ctx); err != nil {
		return nil, err
	}
	// Authorize the exact owner before validating Criteria IDs so an unrelated
	// caller cannot probe Access state. The locked check is repeated inside
	// the mutation transaction after Criteria locks are acquired.
	var initial fileDownloadPolicyRelation
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		initial, err = s.resolveFileDownloadPolicyRelation(ctx, store, change.selector, false)
		return err
	}); err != nil {
		return nil, err
	}
	if err := s.units.ReadCurrent(ctx, func(tx Store) error {
		return s.authorizeFileDownloadPolicyOwner(ctx, tx, initial, filePolicyOwnerEdit)
	}); err != nil {
		return nil, err
	}
	var resolved fileDownloadPolicyRelation
	update, err := mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[fileDownloadPolicyUpdate, Store]{
		Lock: func(ctx context.Context, tx Store) error {
			// Access Criteria rows are the first database lock in policy and
			// archive flows. This stable order prevents archive-versus-policy
			// deadlocks.
			if err := tx.ValidatePolicyCriteriaSet(ctx, change.criteriaIDs); err != nil {
				return err
			}
			var err error
			resolved, err = s.resolveFileDownloadPolicyRelation(ctx, tx, change.selector, false)
			return err
		},
		Access: mutation.Checked(func(ctx context.Context, tx Store) error {
			return s.authorizeFileDownloadPolicyOwner(ctx, tx, resolved, filePolicyOwnerEdit)
		}),
		Change: func(ctx context.Context, tx Store) (fileDownloadPolicyUpdate, error) {
			return s.applyFileDownloadPolicyChange(ctx, tx, change)
		},
		Audit: func(update fileDownloadPolicyUpdate) mutation.Audit { return update.audit },
		Outbox: func(ctx context.Context, tx Store, update fileDownloadPolicyUpdate) error {
			if !update.changed {
				return nil
			}
			return s.publishFileDownloadPolicyChanged(ctx, tx, update.relation)
		},
	})
	if err != nil {
		return nil, err
	}
	return update.policy, nil
}

// fileDownloadPolicyUpdate is the policy an update leaves, and when it
// changed, the attachment it changed and the record of the change.
type fileDownloadPolicyUpdate struct {
	policy   *DownloadPolicy
	changed  bool
	relation fileDownloadPolicyRelation
	audit    mutation.Audit
}

func (s *Service) applyFileDownloadPolicyChange(ctx context.Context, tx Store, change fileDownloadPolicyChange) (fileDownloadPolicyUpdate, error) {
	locked, err := s.resolveFileDownloadPolicyRelation(ctx, tx, change.selector, true)
	if err != nil {
		return fileDownloadPolicyUpdate{}, err
	}
	if locked.FileID != change.expectedFileID {
		return fileDownloadPolicyUpdate{}, domainerr.Precondition("File attachment changed; reload before saving download policy")
	}
	previousCriteriaIDs, err := loadFileDownloadPolicyCriteriaIDs(ctx, tx, locked)
	if err != nil {
		return fileDownloadPolicyUpdate{}, err
	}
	nextAccess, nextCriteriaIDs := mergeObservedFileDownloadPolicy(
		locked.Access, change.observedAccess, change.access, previousCriteriaIDs, change.observedCriteriaIDs, change.criteriaIDs,
	)
	if len(nextCriteriaIDs) > maxFileDownloadMemberCriteria {
		return fileDownloadPolicyUpdate{}, tooManyFileDownloadMemberCriteria("member_criteria_ids")
	}
	if locked.Access == nextAccess && slices.Equal(previousCriteriaIDs, nextCriteriaIDs) {
		policy, err := s.loadManageFileDownloadPolicy(ctx, tx, locked)
		return fileDownloadPolicyUpdate{policy: policy, audit: mutation.Unchanged()}, err
	}
	audit, err := relationFileDownloadPolicyAudit(locked, locked.Access, nextAccess, previousCriteriaIDs, nextCriteriaIDs)
	if err != nil {
		return fileDownloadPolicyUpdate{}, err
	}
	if err := updateFileDownloadPolicyRelation(ctx, tx, locked, nextAccess, nextCriteriaIDs); err != nil {
		return fileDownloadPolicyUpdate{}, err
	}
	signaled := locked
	locked.Access = nextAccess
	policy, err := s.loadManageFileDownloadPolicy(ctx, tx, locked)
	return fileDownloadPolicyUpdate{policy: policy, changed: true, relation: signaled, audit: audit}, err
}

// Policy is attachment metadata. Track policies belong to their Release, not
// to a Track room. PublishInTransaction retains the owner's current document
// revision and stamps the browser origin; no document or locale state advances.
func (s *Service) publishFileDownloadPolicyChanged(ctx context.Context, store Store, relation fileDownloadPolicyRelation) error {
	return store.PublishPolicyChange(ctx, domain.PolicyChangeSignal{ResourceType: relation.ResourceType, ResourceID: relation.ResourceID, TimestampMS: time.Now().UnixMilli()})
}

func mergeObservedFileDownloadPolicy(
	currentAccess, observedAccess, desiredAccess mediaasset.FileDownloadAccess,
	currentCriteriaIDs, observedCriteriaIDs, desiredCriteriaIDs []string,
) (mediaasset.FileDownloadAccess, []string) {
	mergedAccess := currentAccess
	if observedAccess != desiredAccess {
		mergedAccess = desiredAccess
	}
	mergedCriteriaIDs := mergeObservedFileDownloadCriteriaIDs(currentCriteriaIDs, observedCriteriaIDs, desiredCriteriaIDs)
	if mergedAccess != mediaasset.FileDownloadAccessRestricted {
		// Non-restricted access values cannot retain criteria relations.
		mergedCriteriaIDs = nil
	}
	return mergedAccess, mergedCriteriaIDs
}

func mergeObservedFileDownloadCriteriaIDs(current, observed, desired []string) []string {
	result := make(map[string]struct{}, len(current)+len(desired))
	observedSet := make(map[string]struct{}, len(observed))
	desiredSet := make(map[string]struct{}, len(desired))
	for _, id := range current {
		result[id] = struct{}{}
	}
	for _, id := range observed {
		observedSet[id] = struct{}{}
	}
	for _, id := range desired {
		desiredSet[id] = struct{}{}
	}
	for id := range observedSet {
		if _, shouldKeep := desiredSet[id]; !shouldKeep {
			delete(result, id)
		}
	}
	for id := range desiredSet {
		if _, existed := observedSet[id]; !existed {
			result[id] = struct{}{}
		}
	}
	merged := make([]string, 0, len(result))
	for id := range result {
		merged = append(merged, id)
	}
	sort.Strings(merged)
	return merged
}

func validateFileDownloadPolicySelector(
	entityType EntityType,
	entityID string,
	blockID *string,
	referencePath *string,
) (fileDownloadPolicySelector, error) {
	entityID = strings.TrimSpace(entityID)
	if entityType == EntityUnspecified {
		return fileDownloadPolicySelector{}, domainerr.Required("entity_type")
	}
	parsedEntityID, err := uuid.Parse(entityID)
	if err != nil {
		return fileDownloadPolicySelector{}, domainerr.InvalidField("entity_id", "must be a valid UUID")
	}
	entityID = parsedEntityID.String()
	selector := fileDownloadPolicySelector{EntityType: entityType, EntityID: entityID}
	if entityType == EntityTrack {
		if blockID != nil || referencePath != nil {
			return fileDownloadPolicySelector{}, domainerr.InvalidField("block_id", "Track policy does not accept a Content Block selector")
		}
		return selector, nil
	}
	if _, ok := contentBlockFilePolicyOwner(entityType); !ok {
		return fileDownloadPolicySelector{}, domainerr.InvalidField("entity_type", "unsupported file download policy owner")
	}
	if blockID == nil {
		return fileDownloadPolicySelector{}, domainerr.Required("block_id")
	}
	if referencePath == nil {
		return fileDownloadPolicySelector{}, domainerr.Required("reference_path")
	}
	normalizedBlockID := strings.TrimSpace(*blockID)
	parsedBlockID, err := uuid.Parse(normalizedBlockID)
	if err != nil {
		return fileDownloadPolicySelector{}, domainerr.InvalidField("block_id", "must be a valid UUID")
	}
	normalizedBlockID = parsedBlockID.String()
	normalizedReferencePath := strings.TrimSpace(*referencePath)
	if normalizedReferencePath != "file" {
		return fileDownloadPolicySelector{}, domainerr.InvalidField("reference_path", "File Block policy requires the canonical file selector")
	}
	selector.BlockID = &normalizedBlockID
	selector.ReferencePath = &normalizedReferencePath
	return selector, nil
}

func (s *Service) resolveFileDownloadPolicyRelation(ctx context.Context, store Store, selector fileDownloadPolicySelector, lock bool) (fileDownloadPolicyRelation, error) {
	row, err := store.ResolveDownloadPolicyRelation(ctx, selector.storage(), lock)
	if errors.Is(err, domain.ErrPolicyRelationNotFound) {
		if selector.EntityType == EntityTrack {
			return fileDownloadPolicyRelation{}, domainerr.ResourceNotFound("Track original audio", selector.EntityID)
		}
		return fileDownloadPolicyRelation{}, domainerr.ResourceNotFound("File Block attachment", *selector.BlockID)
	}
	return fileDownloadPolicyRelation{Selector: selector, FileID: row.FileID, Access: row.Access, ResourceType: row.ResourceType, ResourceID: row.ResourceID}, err
}
func (s fileDownloadPolicySelector) storage() domain.PolicySelector {
	kind := "track"
	if resource, ok := contentBlockFilePolicyOwner(s.EntityType); ok {
		kind = resource
	}
	return domain.PolicySelector{OwnerKind: kind, EntityID: s.EntityID, BlockID: s.BlockID, ReferencePath: s.ReferencePath}
}

func (s *Service) authorizeFileDownloadPolicyOwner(ctx context.Context, store Store, relation fileDownloadPolicyRelation, action filePolicyOwnerAction) error {
	return store.RequirePolicyOwner(ctx, relation.ResourceType, relation.ResourceID, action != filePolicyOwnerView)
}

func requireFileDownloadPolicyActor(ctx context.Context) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return domainerr.AuthenticationRequired()
	}
	if user.Banned {
		return domainerr.AccountBanned()
	}
	return nil
}

func contentBlockFilePolicyOwner(entityType EntityType) (string, bool) {
	switch entityType {
	case EntityPost:
		return "post", true
	case EntityPage:
		return "page", true
	case EntityWork:
		return "work", true
	case EntityProgramEvent:
		return "program_event", true
	default:
		return "", false
	}
}

func loadFileDownloadPolicyCriteriaIDs(ctx context.Context, store Store, relation fileDownloadPolicyRelation) ([]string, error) {
	return store.DownloadPolicyCriteriaIDs(ctx, relation.Selector.storage())
}
func updateFileDownloadPolicyRelation(ctx context.Context, store Store, relation fileDownloadPolicyRelation, access mediaasset.FileDownloadAccess, ids []string) error {
	return store.SetDownloadPolicy(ctx, relation.Selector.storage(), access, ids, time.Now().UTC())
}

func (s *Service) loadManageFileDownloadPolicy(
	ctx context.Context,
	store Store,
	relation fileDownloadPolicyRelation,
) (*DownloadPolicy, error) {
	accessValue, ok := validatedStoredFileDownloadAccess(relation.Access)
	if !ok {
		return nil, errors.New("attachment has invalid download access")
	}
	criteriaIDs, err := loadFileDownloadPolicyCriteriaIDs(ctx, store, relation)
	if err != nil {
		return nil, err
	}
	var criteriaSet []domain.PolicyCriteria
	if len(criteriaIDs) > 0 {
		criteriaSet, err = store.ActivePolicyCriteriaSet(ctx, criteriaIDs)
		if err != nil {
			return nil, err
		}
	}

	summaries := make([]*domain.PolicyCriteria, 0, len(criteriaSet))
	for i := range criteriaSet {
		if !criteriaSet[i].Eligible {
			return nil, errors.New("attachment references an ineligible Member criteria")
		}
		summaries = append(summaries, &criteriaSet[i])
	}
	return &DownloadPolicy{
		EntityType:     int32(relation.Selector.EntityType),
		EntityID:       relation.Selector.EntityID,
		BlockID:        relation.Selector.BlockID,
		ReferencePath:  relation.Selector.ReferencePath,
		FileID:         relation.FileID,
		Access:         accessValue,
		MemberCriteria: summaries,
	}, nil
}

// relationFileDownloadPolicyAudit names the owner's record of a download
// policy change.
func relationFileDownloadPolicyAudit(
	relation fileDownloadPolicyRelation,
	previousAccess, access mediaasset.FileDownloadAccess,
	previousCriteriaIDs, criteriaIDs []string,
) (mutation.Audit, error) {
	var action sharedtelemetry.AuditAction
	var build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)
	previous := sharedtelemetry.AuditState(previousAccess)
	next := sharedtelemetry.AuditState(access)
	switch relation.ResourceType {
	case "post":
		action = sharedtelemetry.AuditPostUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousCriteriaIDs, criteriaIDs)
		}
	case "page":
		action = sharedtelemetry.AuditPageUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousCriteriaIDs, criteriaIDs)
		}
	case "work":
		action = sharedtelemetry.AuditWorkUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousCriteriaIDs, criteriaIDs)
		}
	case "program_event":
		action = sharedtelemetry.AuditProgramEventUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousCriteriaIDs, criteriaIDs)
		}
	case "release":
		action = sharedtelemetry.AuditReleaseUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseTrackDownloadPolicyAuditRecord(m, relation.ResourceID, relation.Selector.EntityID, relation.FileID, previous, next, previousCriteriaIDs, criteriaIDs)
		}
	default:
		return mutation.Audit{}, errors.New("unsupported File download policy audit owner")
	}
	return mutation.RequestAudit(action, build), nil
}

func requireFileDownloadAuthor(ctx context.Context, spiceDB authz.AuthorizationDecisionChecker) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return domainerr.AuthenticationRequired()
	}
	if user.Banned {
		return domainerr.AccountBanned()
	}
	can, err := policyv1.File.List()
	if err != nil {
		return domainerr.AuthorRequired().WithCause(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.AuthorRequired()
	}
	return nil
}

func uniqueUUIDs(values []string, field string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, rawValue := range values {
		value := strings.TrimSpace(rawValue)
		parsed, err := uuid.Parse(value)
		if err != nil {
			return nil, domainerr.InvalidField(field, fmt.Sprintf("invalid UUID: %s", value))
		}
		value = parsed.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func storedFileDownloadAccess(access DownloadAccess) (mediaasset.FileDownloadAccess, bool) {
	switch access {
	case DownloadAccessDisabled:
		return mediaasset.FileDownloadAccessDisabled, true
	case DownloadAccessPublic:
		return mediaasset.FileDownloadAccessPublic, true
	case DownloadAccessAuthenticated:
		return mediaasset.FileDownloadAccessAuthenticated, true
	case DownloadAccessRestricted:
		return mediaasset.FileDownloadAccessRestricted, true
	default:
		return "", false
	}
}

func validatedStoredFileDownloadAccess(access mediaasset.FileDownloadAccess) (mediaasset.FileDownloadAccess, bool) {
	switch access {
	case mediaasset.FileDownloadAccessDisabled, mediaasset.FileDownloadAccessPublic, mediaasset.FileDownloadAccessAuthenticated, mediaasset.FileDownloadAccessRestricted:
		return access, true
	default:
		return "", false
	}
}
