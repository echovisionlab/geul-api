package contentblock

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type siblingContainer struct {
	parentID *uuid.UUID
	slot     string
}

func isReorderOnlyBatch(batch Batch) bool {
	return len(batch.Reorders) > 0 && len(batch.Upserts) == 0 &&
		len(batch.Deletes) == 0 && len(batch.LocaleGroups) == 0
}

// applyReorderBatchPostgres validates only moved Blocks, their old/new
// sibling containers, and the proposed ancestor chains. It deliberately does
// not load or canonicalize the rest of the document.
func (s *Store) applyReorderBatchPostgres(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	domain DomainContext,
) (Result, error) {
	return s.applyReorderCTEPostgres(ctx, tx, batch, domain)
}

func cloneUUIDPointer(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func affectedSiblingContainers(current map[uuid.UUID]FullBlock, reorders []Reorder) []siblingContainer {
	seen := make(map[string]struct{}, len(reorders)*2)
	result := make([]siblingContainer, 0, len(reorders)*2)
	appendContainer := func(parentID *uuid.UUID, slot string) {
		key := slot + "\x00root"
		if parentID != nil {
			key = slot + "\x00" + parentID.String()
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result = append(result, siblingContainer{parentID: cloneUUIDPointer(parentID), slot: slot})
	}
	for _, reorder := range reorders {
		before := current[reorder.BlockID]
		appendContainer(before.ParentID, before.ContainerSlot)
		appendContainer(reorder.ParentID, strings.TrimSpace(reorder.ContainerSlot))
	}
	return result
}

func affectedAncestryIDs(blocks map[uuid.UUID]FullBlock, reorders []Reorder, maxDepth int) ([]uuid.UUID, error) {
	seen := make(map[uuid.UUID]struct{})
	for _, reorder := range reorders {
		path := make(map[uuid.UUID]struct{})
		currentID := reorder.BlockID
		for depth := 1; ; depth++ {
			if depth > maxDepth {
				return nil, fmt.Errorf("%w: document exceeds Block depth %d", ErrInvalidMutation, maxDepth)
			}
			if _, cycle := path[currentID]; cycle {
				return nil, fmt.Errorf("%w: Block parent cycle at %s", ErrInvalidMutation, currentID)
			}
			path[currentID] = struct{}{}
			seen[currentID] = struct{}{}
			block, exists := blocks[currentID]
			if !exists {
				return nil, fmt.Errorf("%w: affected Block %s does not exist", ErrInvalidMutation, currentID)
			}
			if block.ParentID == nil {
				break
			}
			currentID = *block.ParentID
		}
	}
	result := make([]uuid.UUID, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result, nil
}

func (s *Store) validateAffectedParents(
	profile string,
	blocks map[uuid.UUID]FullBlock,
	blockIDs []uuid.UUID,
) error {
	for _, blockID := range blockIDs {
		block := blocks[blockID]
		var parent *FullBlock
		if block.ParentID != nil {
			value, exists := blocks[*block.ParentID]
			if !exists {
				return fmt.Errorf("%w: Block %s parent %s does not exist", ErrInvalidMutation, blockID, *block.ParentID)
			}
			parent = &value
		}
		if err := s.contract.ValidateParent(profile, parent, block); err != nil {
			return fmt.Errorf("%w: invalid parent for Block %s: %v", ErrInvalidMutation, blockID, err)
		}
	}
	return nil
}

func validateAffectedSiblingDensity(blocks map[uuid.UUID]FullBlock, containers []siblingContainer) error {
	for _, container := range containers {
		positions := make([]int, 0)
		for _, block := range blocks {
			if equalUUIDPointers(block.ParentID, container.parentID) && block.ContainerSlot == container.slot {
				positions = append(positions, block.Position)
			}
		}
		sort.Ints(positions)
		for expected, actual := range positions {
			if actual != expected {
				return fmt.Errorf("%w: affected sibling positions must be dense from zero", ErrInvalidMutation)
			}
		}
	}
	return nil
}
