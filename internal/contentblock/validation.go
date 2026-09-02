package contentblock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

var (
	containerSlotPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	blockKindPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
)

func normalizeBlock(
	contract Contract,
	profile string,
	block FullBlock,
) (FullBlock, error) {
	if block.ID == uuid.Nil {
		return FullBlock{}, fmt.Errorf("%w: Block ID must be a UUID", ErrInvalidMutation)
	}
	if block.ParentID != nil && *block.ParentID == uuid.Nil {
		return FullBlock{}, fmt.Errorf("%w: parent Block ID must be a UUID", ErrInvalidMutation)
	}
	block.Kind = strings.TrimSpace(block.Kind)
	block.ContainerSlot = strings.TrimSpace(block.ContainerSlot)
	if !blockKindPattern.MatchString(block.Kind) {
		return FullBlock{}, fmt.Errorf("%w: invalid Block kind", ErrInvalidMutation)
	}
	if !containerSlotPattern.MatchString(block.ContainerSlot) {
		return FullBlock{}, fmt.Errorf("%w: invalid container slot", ErrInvalidMutation)
	}
	if block.Position < 0 {
		return FullBlock{}, fmt.Errorf("%w: Block position must not be negative", ErrInvalidMutation)
	}
	validated, err := contract.ValidateBlock(profile, block)
	if err != nil {
		return FullBlock{}, fmt.Errorf("%w: validate Block %s: %v", ErrInvalidMutation, block.ID, err)
	}
	block.SharedData, err = canonicalObject(validated.SharedData)
	if err != nil {
		return FullBlock{}, fmt.Errorf("%w: Block %s shared data: %v", ErrInvalidMutation, block.ID, err)
	}
	block.LocalizedData, err = canonicalObject(validated.LocalizedData)
	if err != nil {
		return FullBlock{}, fmt.Errorf("%w: Block %s localized data: %v", ErrInvalidMutation, block.ID, err)
	}
	block.FileReferences = append([]FileReference(nil), validated.FileReferences...)
	seenPaths := make(map[string]struct{}, len(block.FileReferences))
	for index := range block.FileReferences {
		reference := &block.FileReferences[index]
		reference.BlockID = block.ID
		reference.ReferencePath = strings.TrimSpace(reference.ReferencePath)
		if reference.ReferencePath == "" || len(reference.ReferencePath) > 512 ||
			strings.IndexFunc(reference.ReferencePath, unicode.IsControl) >= 0 {
			return FullBlock{}, fmt.Errorf("%w: invalid File reference path", ErrInvalidMutation)
		}
		if _, exists := seenPaths[reference.ReferencePath]; exists {
			return FullBlock{}, fmt.Errorf("%w: duplicate File reference path %q", ErrInvalidMutation, reference.ReferencePath)
		}
		seenPaths[reference.ReferencePath] = struct{}{}
		if reference.FileID == uuid.Nil {
			return FullBlock{}, fmt.Errorf("%w: File reference must contain a UUID", ErrInvalidMutation)
		}
		if reference.Missing && reference.MissingMediaKind == "" {
			return FullBlock{}, fmt.Errorf("%w: missing attachment media kind is required", ErrInvalidMutation)
		}
		if !reference.Missing && reference.MissingMediaKind != "" {
			return FullBlock{}, fmt.Errorf("%w: active attachment cannot contain a missing media kind", ErrInvalidMutation)
		}
		sort.Strings(reference.AllowedMIMETypes)
		sort.Strings(reference.AllowedMIMEPrefixes)
	}
	sort.Slice(block.FileReferences, func(i, j int) bool {
		return block.FileReferences[i].ReferencePath < block.FileReferences[j].ReferencePath
	})
	return block, nil
}

func validateAggregate(contract Contract, state *aggregate, sourceLocale string) error {
	limits, err := validateAggregateEnvelope(contract, state, sourceLocale)
	if err != nil {
		return err
	}
	for id := range state.blocks {
		if err := validateAggregateBlockPayload(contract, state, sourceLocale, id); err != nil {
			return err
		}
	}
	return validateAggregateStructure(contract, state, limits)
}

func validateAggregateEnvelope(contract Contract, state *aggregate, sourceLocale string) (Limits, error) {
	if err := validateLocale(sourceLocale); err != nil {
		return Limits{}, fmt.Errorf("%w: source locale: %v", ErrInvalidMutation, err)
	}
	limits, err := contract.Limits(state.document.Profile)
	if err != nil {
		return Limits{}, fmt.Errorf("%w: validate document profile: %v", ErrInvalidMutation, err)
	}
	if limits.MaxBlocks <= 0 || limits.MaxDepth <= 0 {
		return Limits{}, fmt.Errorf("%w: document profile has invalid limits", ErrInvalidMutation)
	}
	if len(state.blocks) > limits.MaxBlocks {
		return Limits{}, fmt.Errorf("%w: document exceeds %d Blocks", ErrInvalidMutation, limits.MaxBlocks)
	}
	return limits, nil
}

func validateAggregateBlockPayload(
	contract Contract,
	state *aggregate,
	sourceLocale string,
	id uuid.UUID,
) error {
	block, exists := state.blocks[id]
	if !exists {
		return fmt.Errorf("%w: Block %s does not exist", ErrInvalidMutation, id)
	}
	locales := state.locales[id]
	if _, exists := locales[sourceLocale]; !exists {
		return fmt.Errorf("%w: Block %s has no source locale overlay", ErrInvalidMutation, id)
	}
	var canonicalReferences []FileReference
	var canonicalShared json.RawMessage
	initialized := false
	for _, localized := range locales {
		candidate := cloneBlock(block)
		candidate.LocalizedData = localized
		normalized, err := normalizeBlock(contract, state.document.Profile, candidate)
		if err != nil {
			return err
		}
		if !initialized {
			initialized = true
			canonicalReferences = normalized.FileReferences
			canonicalShared = normalized.SharedData
		} else if !bytes.Equal(canonicalShared, normalized.SharedData) {
			return fmt.Errorf("%w: locale changed shared data for Block %s", ErrInvalidMutation, id)
		} else if !sameFileReferences(canonicalReferences, normalized.FileReferences) {
			return fmt.Errorf("%w: locale changed File references for Block %s", ErrInvalidMutation, id)
		}
	}
	if !sameFileReferences(block.FileReferences, canonicalReferences) {
		return fmt.Errorf("%w: stored File references differ from Block %s payload", ErrInvalidMutation, id)
	}
	block.SharedData = canonicalShared
	block.FileReferences = canonicalReferences
	state.blocks[id] = block
	return nil
}

func validateAggregateStructure(contract Contract, state *aggregate, limits Limits) error {
	positions := make(map[string]uuid.UUID, len(state.blocks))
	siblingPositions := make(map[string][]int, len(state.blocks))
	depths := make(map[uuid.UUID]int, len(state.blocks))
	visiting := make(map[uuid.UUID]bool, len(state.blocks))
	var depth func(uuid.UUID) (int, error)
	depth = func(blockID uuid.UUID) (int, error) {
		if known, exists := depths[blockID]; exists {
			return known, nil
		}
		if visiting[blockID] {
			return 0, fmt.Errorf("%w: Block parent cycle at %s", ErrInvalidMutation, blockID)
		}
		visiting[blockID] = true
		block := state.blocks[blockID]
		result := 1
		if block.ParentID != nil {
			parent, exists := state.blocks[*block.ParentID]
			if !exists {
				return 0, fmt.Errorf("%w: Block %s parent %s does not exist", ErrInvalidMutation, blockID, *block.ParentID)
			}
			if err := contract.ValidateParent(state.document.Profile, &parent, block); err != nil {
				return 0, fmt.Errorf("%w: invalid parent for Block %s: %v", ErrInvalidMutation, blockID, err)
			}
			parentDepth, err := depth(*block.ParentID)
			if err != nil {
				return 0, err
			}
			result = parentDepth + 1
		} else if err := contract.ValidateParent(state.document.Profile, nil, block); err != nil {
			return 0, fmt.Errorf("%w: invalid root Block %s: %v", ErrInvalidMutation, blockID, err)
		}
		visiting[blockID] = false
		if result > limits.MaxDepth {
			return 0, fmt.Errorf("%w: document exceeds Block depth %d", ErrInvalidMutation, limits.MaxDepth)
		}
		depths[blockID] = result
		return result, nil
	}

	for id, block := range state.blocks {
		if _, err := depth(id); err != nil {
			return err
		}
		parent := "root"
		if block.ParentID != nil {
			parent = block.ParentID.String()
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", parent, block.ContainerSlot, block.Position)
		if existing, exists := positions[key]; exists {
			return fmt.Errorf("%w: Blocks %s and %s share one position", ErrInvalidMutation, existing, id)
		}
		positions[key] = id
		siblingKey := fmt.Sprintf("%s\x00%s", parent, block.ContainerSlot)
		siblingPositions[siblingKey] = append(siblingPositions[siblingKey], block.Position)
	}
	for siblingKey, group := range siblingPositions {
		sort.Ints(group)
		for expected, actual := range group {
			if actual != expected {
				return fmt.Errorf(
					"%w: sibling group %q positions must be dense from zero",
					ErrInvalidMutation,
					siblingKey,
				)
			}
		}
	}
	return nil
}

func sameBlockShared(left, right FullBlock) bool {
	return left.ID == right.ID &&
		equalUUIDPointers(left.ParentID, right.ParentID) &&
		left.ContainerSlot == right.ContainerSlot &&
		left.Position == right.Position &&
		left.Kind == right.Kind &&
		bytes.Equal(left.SharedData, right.SharedData) &&
		sameFileReferences(left.FileReferences, right.FileReferences)
}

func sameFileReferences(left, right []FileReference) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]FileReference(nil), left...)
	right = append([]FileReference(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i].ReferencePath < left[j].ReferencePath })
	sort.Slice(right, func(i, j int) bool { return right[i].ReferencePath < right[j].ReferencePath })
	for index := range left {
		if left[index].BlockID != right[index].BlockID ||
			left[index].ReferencePath != right[index].ReferencePath ||
			left[index].Missing != right[index].Missing {
			return false
		}
		if left[index].Missing {
			if left[index].MissingMediaKind != right[index].MissingMediaKind {
				return false
			}
		} else if left[index].FileID != right[index].FileID {
			return false
		}
	}
	return true
}

func equalUUIDPointers(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateLocale(locale string) error {
	if locale != strings.TrimSpace(locale) || locale == "" || len(locale) > 35 {
		return fmt.Errorf("%w: invalid locale", ErrInvalidMutation)
	}
	return nil
}

func validateMIME(reference FileReference, mimeType string) bool {
	if len(reference.AllowedMIMETypes) == 0 && len(reference.AllowedMIMEPrefixes) == 0 {
		return true
	}
	for _, allowed := range reference.AllowedMIMETypes {
		if mimeType == allowed {
			return true
		}
	}
	for _, prefix := range reference.AllowedMIMEPrefixes {
		if strings.HasPrefix(mimeType, prefix) {
			return true
		}
	}
	return false
}
