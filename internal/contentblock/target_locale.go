package contentblock

import (
	"encoding/json"
	"sort"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

// ValidateTargetLocaleBatch admits a target-locale batch against the state its
// caller locked. A target locale owns only its locale values: the shared Block
// graph and File ownership belong to the source room, and a value is cleared
// by writing an explicit empty value unless the caller is an authoritative
// whole-locale replacement (allowLocaleDeletes). entity names the owner in
// client messages, such as "Work".
func ValidateTargetLocaleBatch(
	entity string,
	batch Batch,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	locale string,
	allowLocaleDeletes bool,
) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedRevision {
		return errs.InvalidArgument("batch", entity+" target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			entity+" target locale cannot mutate the shared Block graph or File ownership",
		)
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
			entity+" target locale contains multiple locale groups",
		)
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				entity+" target mutation must match the authorized locale",
			)
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
				entity+" target patch cannot unset locale Blocks; write explicit empty values instead",
			)
		}
	}
	return nil
}

// CloneBatch copies one mutation batch without sharing caller-owned mutable
// slices, JSON payloads, or validated File-reference provenance.
func CloneBatch(batch Batch) Batch {
	cloned := batch
	cloned.Upserts = append([]BaseBlock(nil), batch.Upserts...)
	for index := range cloned.Upserts {
		cloned.Upserts[index].ParentID = cloneUUIDPointer(batch.Upserts[index].ParentID)
		cloned.Upserts[index].SharedData = append(json.RawMessage(nil), batch.Upserts[index].SharedData...)
	}
	cloned.Deletes = append([]uuid.UUID(nil), batch.Deletes...)
	cloned.Reorders = append([]Reorder(nil), batch.Reorders...)
	for index := range cloned.Reorders {
		cloned.Reorders[index].ParentID = cloneUUIDPointer(batch.Reorders[index].ParentID)
	}
	cloned.LocaleGroups = append([]LocaleMutationGroup(nil), batch.LocaleGroups...)
	for index := range cloned.LocaleGroups {
		cloned.LocaleGroups[index].Upserts = append(
			[]LocaleBlockUpdate(nil),
			batch.LocaleGroups[index].Upserts...,
		)
		for updateIndex := range cloned.LocaleGroups[index].Upserts {
			cloned.LocaleGroups[index].Upserts[updateIndex].LocalizedData = append(
				json.RawMessage(nil),
				batch.LocaleGroups[index].Upserts[updateIndex].LocalizedData...,
			)
		}
		cloned.LocaleGroups[index].Deletes = append(
			[]uuid.UUID(nil),
			batch.LocaleGroups[index].Deletes...,
		)
	}
	cloned.ContributorMemberIDs = append([]uuid.UUID(nil), batch.ContributorMemberIDs...)
	if batch.validatedBaseReferences != nil {
		cloned.validatedBaseReferences = make(map[uuid.UUID][]FileReference, len(batch.validatedBaseReferences))
		for blockID, references := range batch.validatedBaseReferences {
			clonedReferences := append([]FileReference(nil), references...)
			for index := range clonedReferences {
				clonedReferences[index].AllowedMIMETypes = append(
					[]string(nil),
					references[index].AllowedMIMETypes...,
				)
				clonedReferences[index].AllowedMIMEPrefixes = append(
					[]string(nil),
					references[index].AllowedMIMEPrefixes...,
				)
			}
			cloned.validatedBaseReferences[blockID] = clonedReferences
		}
	}
	return cloned
}

// SeedTargetLocaleBatch creates an absent target overlay from the current
// source overlay, then applies the requested target upserts and deletes. The
// shared Block graph is never changed, and the returned target order is stable.
func SeedTargetLocaleBatch(
	batch Batch,
	snapshot Snapshot,
	sourceLocale string,
	targetLocale string,
) (Batch, error) {
	seededBatch := CloneBatch(batch)
	kinds := make(map[uuid.UUID]string, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		kinds[block.ID] = block.Kind
	}
	seeded := make(map[uuid.UUID]LocaleBlockUpdate, len(snapshot.Blocks))
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale != sourceLocale {
			continue
		}
		for _, block := range overlay.Blocks {
			kind := kinds[block.BlockID]
			if kind == "" {
				return Batch{}, errs.FailedPrecondition("source locale contains an unknown Block")
			}
			seeded[block.BlockID] = LocaleBlockUpdate{
				BlockID:       block.BlockID,
				ExpectedKind:  kind,
				LocalizedData: append(json.RawMessage(nil), block.LocalizedData...),
			}
		}
		break
	}
	for _, group := range seededBatch.LocaleGroups {
		for _, upsert := range group.Upserts {
			seeded[upsert.BlockID] = upsert
		}
		for _, blockID := range group.Deletes {
			delete(seeded, blockID)
		}
	}
	ids := make([]uuid.UUID, 0, len(seeded))
	for blockID := range seeded {
		ids = append(ids, blockID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	seededBatch.LocaleGroups = nil
	if len(ids) != 0 {
		group := LocaleMutationGroup{Locale: targetLocale, Upserts: make([]LocaleBlockUpdate, 0, len(ids))}
		for _, blockID := range ids {
			group.Upserts = append(group.Upserts, seeded[blockID])
		}
		seededBatch.LocaleGroups = []LocaleMutationGroup{group}
	}
	return seededBatch, nil
}
