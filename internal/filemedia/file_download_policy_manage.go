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

const maxFileDownloadAudienceSegments = 20

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
	Audience     mediaasset.FileDownloadAudience
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

// FileDownloadPolicyState is a download audience with its segments.
type FileDownloadPolicyState struct {
	Audience           DownloadAudience
	AudienceSegmentIDs []string
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
	selector           fileDownloadPolicySelector
	expectedFileID     string
	audience           mediaasset.FileDownloadAudience
	segmentIDs         []string
	observedAudience   mediaasset.FileDownloadAudience
	observedSegmentIDs []string
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
	audience, ok := storedFileDownloadAudience(command.Policy.Audience)
	if !ok {
		return fileDownloadPolicyChange{}, domainerr.InvalidField("audience", "unsupported file download audience")
	}
	segmentIDs, err := uniqueUUIDs(command.Policy.AudienceSegmentIDs, "audience_segment_ids")
	if err != nil {
		return fileDownloadPolicyChange{}, err
	}
	if command.Observed == nil {
		return fileDownloadPolicyChange{}, domainerr.InvalidField("observed_policy", "is required")
	}
	observedAudience, ok := storedFileDownloadAudience(command.Observed.Audience)
	if !ok {
		return fileDownloadPolicyChange{}, domainerr.InvalidField("observed_policy.audience", "unsupported file download audience")
	}
	observedSegmentIDs, err := uniqueUUIDs(command.Observed.AudienceSegmentIDs, "observed_policy.audience_segment_ids")
	if err != nil {
		return fileDownloadPolicyChange{}, err
	}
	if len(observedSegmentIDs) > maxFileDownloadAudienceSegments {
		return fileDownloadPolicyChange{}, tooManyFileDownloadAudienceSegments("observed_policy.audience_segment_ids")
	}
	if len(segmentIDs) > maxFileDownloadAudienceSegments {
		return fileDownloadPolicyChange{}, tooManyFileDownloadAudienceSegments("audience_segment_ids")
	}
	if audience != mediaasset.FileDownloadAudienceRestricted && len(segmentIDs) > 0 {
		return fileDownloadPolicyChange{}, domainerr.InvalidField("audience_segment_ids", "audience segments require restricted audience")
	}
	slices.Sort(segmentIDs)
	return fileDownloadPolicyChange{
		selector: selector, expectedFileID: parsedExpectedFileID.String(),
		audience: audience, segmentIDs: segmentIDs,
		observedAudience: observedAudience, observedSegmentIDs: observedSegmentIDs,
	}, nil
}

func tooManyFileDownloadAudienceSegments(field string) error {
	return domainerr.InvalidField(field, fmt.Sprintf("at most %d audience segments are allowed", maxFileDownloadAudienceSegments))
}

func (s *Service) UpdateFileDownloadPolicy(ctx context.Context, command UpdateFileDownloadPolicyCommand) (*DownloadPolicy, error) {
	change, err := validateFileDownloadPolicyChange(command)
	if err != nil {
		return nil, err
	}
	if err := requireFileDownloadPolicyActor(ctx); err != nil {
		return nil, err
	}
	// Authorize the exact owner before validating Segment IDs so an unrelated
	// caller cannot probe Audience state. The locked check is repeated inside
	// the mutation transaction after Segment locks are acquired.
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
			// Audience Segment rows are the first database lock in policy and
			// archive flows. This stable order prevents archive-versus-policy
			// deadlocks.
			if err := tx.ValidatePolicySegments(ctx, change.segmentIDs); err != nil {
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
	previousSegmentIDs, err := loadFileDownloadPolicySegmentIDs(ctx, tx, locked)
	if err != nil {
		return fileDownloadPolicyUpdate{}, err
	}
	nextAudience, nextSegmentIDs := mergeObservedFileDownloadPolicy(
		locked.Audience, change.observedAudience, change.audience, previousSegmentIDs, change.observedSegmentIDs, change.segmentIDs,
	)
	if len(nextSegmentIDs) > maxFileDownloadAudienceSegments {
		return fileDownloadPolicyUpdate{}, tooManyFileDownloadAudienceSegments("audience_segment_ids")
	}
	if locked.Audience == nextAudience && slices.Equal(previousSegmentIDs, nextSegmentIDs) {
		policy, err := s.loadManageFileDownloadPolicy(ctx, tx, locked)
		return fileDownloadPolicyUpdate{policy: policy, audit: mutation.Unchanged()}, err
	}
	audit, err := relationFileDownloadPolicyAudit(locked, locked.Audience, nextAudience, previousSegmentIDs, nextSegmentIDs)
	if err != nil {
		return fileDownloadPolicyUpdate{}, err
	}
	if err := updateFileDownloadPolicyRelation(ctx, tx, locked, nextAudience, nextSegmentIDs); err != nil {
		return fileDownloadPolicyUpdate{}, err
	}
	signaled := locked
	locked.Audience = nextAudience
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
	currentAudience, observedAudience, desiredAudience mediaasset.FileDownloadAudience,
	currentSegmentIDs, observedSegmentIDs, desiredSegmentIDs []string,
) (mediaasset.FileDownloadAudience, []string) {
	mergedAudience := currentAudience
	if observedAudience != desiredAudience {
		mergedAudience = desiredAudience
	}
	mergedSegmentIDs := mergeObservedFileDownloadSegmentIDs(currentSegmentIDs, observedSegmentIDs, desiredSegmentIDs)
	if mergedAudience != mediaasset.FileDownloadAudienceRestricted {
		// Non-restricted audiences cannot retain segment relations.
		mergedSegmentIDs = nil
	}
	return mergedAudience, mergedSegmentIDs
}

func mergeObservedFileDownloadSegmentIDs(current, observed, desired []string) []string {
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
	return fileDownloadPolicyRelation{Selector: selector, FileID: row.FileID, Audience: row.Audience, ResourceType: row.ResourceType, ResourceID: row.ResourceID}, err
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

func loadFileDownloadPolicySegmentIDs(ctx context.Context, store Store, relation fileDownloadPolicyRelation) ([]string, error) {
	return store.DownloadPolicySegmentIDs(ctx, relation.Selector.storage())
}
func updateFileDownloadPolicyRelation(ctx context.Context, store Store, relation fileDownloadPolicyRelation, audience mediaasset.FileDownloadAudience, ids []string) error {
	return store.SetDownloadPolicy(ctx, relation.Selector.storage(), audience, ids, time.Now().UTC())
}

func (s *Service) loadManageFileDownloadPolicy(
	ctx context.Context,
	store Store,
	relation fileDownloadPolicyRelation,
) (*DownloadPolicy, error) {
	audienceValue, ok := validatedStoredFileDownloadAudience(relation.Audience)
	if !ok {
		return nil, errors.New("attachment has invalid download audience")
	}
	segmentIDs, err := loadFileDownloadPolicySegmentIDs(ctx, store, relation)
	if err != nil {
		return nil, err
	}
	var segments []domain.PolicySegment
	if len(segmentIDs) > 0 {
		segments, err = store.ActivePolicySegments(ctx, segmentIDs)
		if err != nil {
			return nil, err
		}
	}

	summaries := make([]*domain.PolicySegment, 0, len(segments))
	for i := range segments {
		if !segments[i].Eligible {
			return nil, errors.New("attachment references an ineligible audience segment")
		}
		summaries = append(summaries, &segments[i])
	}
	return &DownloadPolicy{
		EntityType:       int32(relation.Selector.EntityType),
		EntityID:         relation.Selector.EntityID,
		BlockID:          relation.Selector.BlockID,
		ReferencePath:    relation.Selector.ReferencePath,
		FileID:           relation.FileID,
		Audience:         audienceValue,
		AudienceSegments: summaries,
	}, nil
}

// relationFileDownloadPolicyAudit names the owner's record of a download
// policy change.
func relationFileDownloadPolicyAudit(
	relation fileDownloadPolicyRelation,
	previousAudience, audience mediaasset.FileDownloadAudience,
	previousSegmentIDs, segmentIDs []string,
) (mutation.Audit, error) {
	var action sharedtelemetry.AuditAction
	var build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)
	previous := sharedtelemetry.AuditState(previousAudience)
	next := sharedtelemetry.AuditState(audience)
	switch relation.ResourceType {
	case "post":
		action = sharedtelemetry.AuditPostUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
		}
	case "page":
		action = sharedtelemetry.AuditPageUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
		}
	case "work":
		action = sharedtelemetry.AuditWorkUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
		}
	case "program_event":
		action = sharedtelemetry.AuditProgramEventUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventFileBlockDownloadPolicyAuditRecord(m, relation.ResourceID, *relation.Selector.BlockID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
		}
	case "release":
		action = sharedtelemetry.AuditReleaseUpdated
		build = func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseTrackDownloadPolicyAuditRecord(m, relation.ResourceID, relation.Selector.EntityID, relation.FileID, previous, next, previousSegmentIDs, segmentIDs)
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

func storedFileDownloadAudience(audience DownloadAudience) (mediaasset.FileDownloadAudience, bool) {
	switch audience {
	case DownloadAudienceDisabled:
		return mediaasset.FileDownloadAudienceDisabled, true
	case DownloadAudiencePublic:
		return mediaasset.FileDownloadAudiencePublic, true
	case DownloadAudienceAuthenticated:
		return mediaasset.FileDownloadAudienceAuthenticated, true
	case DownloadAudienceRestricted:
		return mediaasset.FileDownloadAudienceRestricted, true
	default:
		return "", false
	}
}

func validatedStoredFileDownloadAudience(audience mediaasset.FileDownloadAudience) (mediaasset.FileDownloadAudience, bool) {
	switch audience {
	case mediaasset.FileDownloadAudienceDisabled, mediaasset.FileDownloadAudiencePublic, mediaasset.FileDownloadAudienceAuthenticated, mediaasset.FileDownloadAudienceRestricted:
		return audience, true
	default:
		return "", false
	}
}
