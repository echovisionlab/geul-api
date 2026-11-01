package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// AddProgramEventMediaCommand adds FileID to the event under Role, or
// updates the alt text, caption and primary flag of the existing relation;
// omitted alt and caption values clear them.
type AddProgramEventMediaCommand struct {
	EventID     string
	FileID      string
	Role        string
	Alt         *string
	Caption     *string
	MakePrimary bool
}

// MediaChange is the outcome of a media relation edit.
type MediaChange struct {
	EventID string
	Changed bool
	// Media is the added or updated relation.
	Media     *Media
	UpdatedAt time.Time
}

// AddProgramEventMedia adds or updates one media relation of the event.
func (s *Service) AddProgramEventMedia(ctx context.Context, command AddProgramEventMediaCommand) (MediaChange, error) {
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	var audit *mutation.Audit
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[MediaChange, Store]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, store Store) (MediaChange, error) {
			change, record, err := addMedia(ctx, store, command)
			audit = record
			return change, err
		},
		Audit: func(MediaChange) mutation.Audit {
			if audit == nil {
				return mutation.Unchanged()
			}
			return *audit
		},
	})
}

func addMedia(ctx context.Context, store Store, command AddProgramEventMediaCommand) (MediaChange, *mutation.Audit, error) {
	role, err := normalizeProgramEventMediaRole(command.Role)
	if err != nil {
		return MediaChange{}, nil, err
	}
	before, existed, err := store.MediaOfFile(ctx, command.EventID, role, command.FileID)
	if err != nil {
		return MediaChange{}, nil, err
	}
	created := !existed
	primaryBefore, err := store.PrimaryMediaFileID(ctx, command.EventID, role)
	if err != nil {
		return MediaChange{}, nil, err
	}
	if err := addProgramEventMedia(
		ctx, store, command.EventID, command.FileID, command.Role, command.Alt, command.Caption, command.MakePrimary,
	); err != nil {
		return MediaChange{}, nil, err
	}
	after, found, err := store.MediaOfFile(ctx, command.EventID, role, command.FileID)
	if err != nil {
		return MediaChange{}, nil, err
	}
	if !found {
		return MediaChange{}, nil, ErrMediaNotFound
	}
	primaryAfter, err := store.PrimaryMediaFileID(ctx, command.EventID, role)
	if err != nil {
		return MediaChange{}, nil, err
	}
	changed := created || !sameProgramEventMedia(before, after)
	updatedAt, err := programEventRelationMutationUpdatedAt(ctx, store, command.EventID, changed)
	if err != nil {
		return MediaChange{}, nil, err
	}
	change := MediaChange{EventID: command.EventID, Changed: changed, Media: &after, UpdatedAt: updatedAt}
	switch {
	case role == "poster" && primaryBefore != primaryAfter:
		return change, posterAudit(command.EventID, primaryBefore, primaryAfter), nil
	case created:
		audit := childAudit(command.EventID, "media", after.ID, sharedtelemetry.AuditItemOperationCreated)
		return change, &audit, nil
	case changed:
		audit := childAudit(command.EventID, "media", after.ID, sharedtelemetry.AuditItemOperationUpdated)
		return change, &audit, nil
	}
	return change, nil, nil
}

// DeleteProgramEventMediaCommand names the media relation to delete.
type DeleteProgramEventMediaCommand struct {
	EventID string
	MediaID string
}

// DeleteProgramEventMedia deletes one media relation of the event and
// promotes the next relation of its role when it was primary.
func (s *Service) DeleteProgramEventMedia(ctx context.Context, command DeleteProgramEventMediaCommand) (MediaChange, error) {
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	var audit mutation.Audit
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[MediaChange, Store]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, store Store) (MediaChange, error) {
			before, err := store.MediaByID(ctx, command.EventID, command.MediaID)
			if err != nil {
				if errors.Is(err, ErrMediaNotFound) {
					return MediaChange{}, domainerr.ResourceNotFound("program event media", command.MediaID)
				}
				return MediaChange{}, err
			}
			primaryBefore, err := store.PrimaryMediaFileID(ctx, command.EventID, before.Role)
			if err != nil {
				return MediaChange{}, err
			}
			updatedAt, err := programEventRelationMutationUpdatedAt(ctx, store, command.EventID, true)
			if err != nil {
				return MediaChange{}, err
			}
			if _, err := deleteProgramEventMedia(ctx, store, command.EventID, command.MediaID); err != nil {
				return MediaChange{}, err
			}
			primaryAfter, err := store.PrimaryMediaFileID(ctx, command.EventID, before.Role)
			if err != nil {
				return MediaChange{}, err
			}
			audit = childAudit(command.EventID, "media", before.ID, sharedtelemetry.AuditItemOperationDeleted)
			if poster := posterAudit(command.EventID, primaryBefore, primaryAfter); before.Role == "poster" && poster != nil {
				audit = *poster
			}
			return MediaChange{EventID: command.EventID, Changed: true, UpdatedAt: updatedAt}, nil
		},
		Audit: func(MediaChange) mutation.Audit { return audit },
	})
}

// ReorderProgramEventMediaCommand orders every media relation of Role; the
// first becomes primary.
type ReorderProgramEventMediaCommand struct {
	EventID  string
	Role     string
	MediaIDs []string
}

// MediaOrder is the outcome of a media reorder.
type MediaOrder struct {
	Changed   bool
	Role      string
	UpdatedAt time.Time
}

// ReorderProgramEventMedia orders the media relations of one role.
func (s *Service) ReorderProgramEventMedia(ctx context.Context, command ReorderProgramEventMediaCommand) (MediaOrder, error) {
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[MediaOrder, Store]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, store Store) (MediaOrder, error) {
			role, err := normalizeProgramEventMediaRole(command.Role)
			if err != nil {
				return MediaOrder{}, err
			}
			current, err := store.RoleMedia(ctx, command.EventID, role)
			if err != nil {
				return MediaOrder{}, err
			}
			before := mediaIDs(current)
			if err := reorderProgramEventMedia(ctx, store, command.EventID, role, command.MediaIDs); err != nil {
				return MediaOrder{}, err
			}
			changed := !sameStringSlice(before, command.MediaIDs)
			updatedAt, err := programEventRelationMutationUpdatedAt(ctx, store, command.EventID, changed)
			return MediaOrder{Changed: changed, Role: role, UpdatedAt: updatedAt}, err
		},
		Audit: func(order MediaOrder) mutation.Audit {
			if !order.Changed {
				return mutation.Unchanged()
			}
			return childOrderAudit(command.EventID, "media", command.MediaIDs)
		},
	})
}

func programEventRelationMutationUpdatedAt(ctx context.Context, store Store, eventID string, changed bool) (time.Time, error) {
	if changed {
		now := dbtime.Now()
		if err := store.TouchEvent(ctx, eventID, now); err != nil {
			return time.Time{}, err
		}
		return now, nil
	}
	return store.EventUpdatedAt(ctx, eventID)
}

func addProgramEventMedia(
	ctx context.Context,
	store Store,
	eventID string,
	fileID string,
	role string,
	alt *string,
	caption *string,
	makePrimary bool,
) error {
	eventID = strings.TrimSpace(eventID)
	fileID = strings.TrimSpace(fileID)
	if eventID == "" {
		return domainerr.Required("event_id")
	}
	if fileID == "" {
		return domainerr.Required("file_id")
	}
	normalizedRole, err := normalizeProgramEventMediaRole(role)
	if err != nil {
		return err
	}
	if err := lockProgramEventForMutation(ctx, store, eventID); err != nil {
		return err
	}
	if err := store.LockAttachableFiles(ctx, []string{fileID}); err != nil {
		return err
	}

	existing, exists, err := store.MediaOfFile(ctx, eventID, normalizedRole, fileID)
	if err != nil {
		return err
	}

	shouldBePrimary := makePrimary
	if !shouldBePrimary {
		hasPrimary, err := store.HasPrimaryMedia(ctx, eventID, normalizedRole)
		if err != nil {
			return err
		}
		shouldBePrimary = !hasPrimary
	}
	if exists && existing.IsPrimary == shouldBePrimary && sameNullableString(existing.Alt, nullableString(alt)) && sameNullableString(existing.Caption, nullableString(caption)) {
		return nil
	}
	if shouldBePrimary {
		if err := store.ClearPrimaryMedia(ctx, eventID, normalizedRole, time.Now().UTC()); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	if exists {
		if err := store.UpdateMedia(ctx, existing.ID, nullableString(alt), nullableString(caption), shouldBePrimary, now); err != nil {
			return err
		}
		return bindProgramEventMediaPublicAsset(ctx, store, existing)
	}

	next, err := store.NextMediaSortOrder(ctx, eventID, normalizedRole)
	if err != nil {
		return err
	}
	media := &Media{
		EventID:   eventID,
		FileID:    fileID,
		Role:      normalizedRole,
		SortOrder: next,
		IsPrimary: shouldBePrimary,
		Alt:       nullableString(alt),
		Caption:   nullableString(caption),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.InsertMedia(ctx, media); err != nil {
		return err
	}
	return bindProgramEventMediaPublicAsset(ctx, store, *media)
}

// bindProgramEventMediaPublicAsset binds the public asset of a poster
// relation. Program Event's inventory grants public-asset ownership only to
// poster media: that one asset is the shared Open Graph image. Other role
// relations preserve their File references without guessing a derivative
// kind.
func bindProgramEventMediaPublicAsset(ctx context.Context, store Store, media Media) error {
	if media.Role != "poster" {
		return nil
	}
	return store.BindPosterMedia(ctx, media.EventID, media.ID, media.FileID)
}

func deleteProgramEventMedia(ctx context.Context, store Store, eventID string, mediaID string) (string, error) {
	eventID = strings.TrimSpace(eventID)
	mediaID = strings.TrimSpace(mediaID)
	if eventID == "" {
		return "", domainerr.Required("event_id")
	}
	if mediaID == "" {
		return "", domainerr.Required("media_id")
	}

	media, err := store.MediaByID(ctx, eventID, mediaID)
	if err != nil {
		if errors.Is(err, ErrMediaNotFound) {
			return "", domainerr.ResourceNotFound("program event media", mediaID)
		}
		return "", err
	}
	deleted, err := store.DeleteMedia(ctx, eventID, mediaID)
	if err != nil {
		return "", err
	}
	if !deleted {
		return "", domainerr.ResourceNotFound("program event media", mediaID)
	}
	if media.IsPrimary {
		if err := promoteProgramEventMediaPrimary(ctx, store, media.EventID, media.Role); err != nil {
			return "", err
		}
	}
	if err := store.ReleaseMediaBinding(ctx, eventID, media.ID); err != nil {
		return "", err
	}
	return media.FileID, nil
}

func reorderProgramEventMedia(
	ctx context.Context,
	store Store,
	eventID string,
	role string,
	requestedIDs []string,
) error {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return domainerr.Required("event_id")
	}
	normalizedRole, err := normalizeProgramEventMediaRole(role)
	if err != nil {
		return err
	}

	orderedIDs := make([]string, 0, len(requestedIDs))
	seen := make(map[string]struct{}, len(requestedIDs))
	for _, mediaID := range requestedIDs {
		mediaID = strings.TrimSpace(mediaID)
		if mediaID == "" {
			return domainerr.Required("media_id")
		}
		if _, ok := seen[mediaID]; ok {
			return domainerr.InvalidField("media_ids", "duplicate program event media id")
		}
		seen[mediaID] = struct{}{}
		orderedIDs = append(orderedIDs, mediaID)
	}

	existing, err := store.RoleMedia(ctx, eventID, normalizedRole)
	if err != nil {
		return err
	}
	if len(existing) != len(orderedIDs) {
		return domainerr.InvalidField("media_ids", "program event media order must include every item")
	}
	existingIDs := make(map[string]struct{}, len(existing))
	for _, media := range existing {
		existingIDs[media.ID] = struct{}{}
	}
	for _, mediaID := range orderedIDs {
		if _, ok := existingIDs[mediaID]; !ok {
			return domainerr.InvalidField("media_ids", "program event media id does not belong to event")
		}
	}
	if len(orderedIDs) == 0 {
		return nil
	}
	if sameStringSlice(mediaIDs(existing), orderedIDs) {
		return nil
	}

	now := time.Now().UTC()
	if err := store.ClearPrimaryMedia(ctx, eventID, normalizedRole, now); err != nil {
		return err
	}
	for index, mediaID := range orderedIDs {
		placed, err := store.PlaceMedia(ctx, eventID, normalizedRole, mediaID, int32(index), now)
		if err != nil {
			return err
		}
		if !placed {
			return domainerr.InvalidField("media_ids", "program event media id does not belong to event")
		}
	}
	return nil
}

func mediaIDs(media []Media) []string {
	ids := make([]string, len(media))
	for index := range media {
		ids[index] = media[index].ID
	}
	return ids
}

func sameProgramEventMedia(left, right Media) bool {
	return left.FileID == right.FileID && left.Role == right.Role && left.IsPrimary == right.IsPrimary && left.SortOrder == right.SortOrder && sameNullableString(left.Alt, right.Alt) && sameNullableString(left.Caption, right.Caption)
}

func deleteDefaultProgramEventPosterMedia(ctx context.Context, store Store, eventID string) error {
	if strings.TrimSpace(eventID) == "" {
		return domainerr.Required("event_id")
	}
	media, err := store.DeleteRoleMedia(ctx, eventID, "poster")
	if err != nil {
		return err
	}
	for _, row := range media {
		if err := store.ReleaseMediaBinding(ctx, eventID, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func normalizeProgramEventMediaRole(role string) (string, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		return "poster", nil
	}
	switch role {
	case "poster", "gallery", "lineup", "sponsor", "social", "venue":
		return role, nil
	default:
		return "", domainerr.InvalidField("role", "unsupported program event media role")
	}
}

func promoteProgramEventMediaPrimary(ctx context.Context, store Store, eventID string, role string) error {
	replacement, found, err := store.FirstMedia(ctx, eventID, role)
	if err != nil || !found {
		return err
	}
	return store.MarkMediaPrimary(ctx, replacement.ID, time.Now().UTC())
}
