package referencecatalog

import (
	"context"
	"log/slog"
	"reflect"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CreateMapPlaceCommand creates a Map Place. ImageFileID binds a ready map
// image when set.
type CreateMapPlaceCommand struct {
	Name              string
	Address           string
	Lat               float64
	Lng               float64
	AddressComponents *AddressComponents
	ImageFileID       *string
	GooglePlaceID     *string
}

// UpdateMapPlaceCommand changes the supplied fields of Map Place ID.
// ClearImage removes the image and takes precedence over ImageFileID.
type UpdateMapPlaceCommand struct {
	ID                string
	Name              *string
	Address           *string
	Lat               *float64
	Lng               *float64
	AddressComponents *AddressComponents
	ImageFileID       *string
	ClearImage        bool
	GooglePlaceID     *string
}

// DeleteMapPlaceCommand deletes Map Place ID when no content references it.
type DeleteMapPlaceCommand struct {
	ID string
}

// CreateMapPlace creates a new map place (author+)
func (s *Service) CreateMapPlace(ctx context.Context, command CreateMapPlaceCommand) (MapPlaceView, error) {
	if command.Name == "" {
		return MapPlaceView{}, domainerr.Required("name")
	}
	if command.Address == "" {
		return MapPlaceView{}, domainerr.Required("address")
	}
	if command.Lat < -90 || command.Lat > 90 {
		return MapPlaceView{}, domainerr.InvalidField("lat", "must be between -90 and 90")
	}
	if command.Lng < -180 || command.Lng > 180 {
		return MapPlaceView{}, domainerr.InvalidField("lng", "must be between -180 and 180")
	}
	createCan, err := policyv1.MapPlace.Create()
	if err != nil {
		return MapPlaceView{}, err
	}

	place, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[MapPlace, Store]{
		Access: s.mapPlaceAccess(createCan, false),
		Change: func(ctx context.Context, store Store) (MapPlace, error) {
			return insertMapPlace(ctx, store, command)
		},
		Audit: func(place MapPlace) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMapPlaceCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMapPlaceCreatedAuditRecord(metadata, place.ID)
			})
		},
		Relationships: func(place MapPlace) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return referencePolicyRelationships(policyv1.MapPlace, place.ID, true)
		},
	})
	if err != nil {
		return MapPlaceView{}, err
	}
	return s.committedMapPlaceView(ctx, place)
}

// insertMapPlace stores the place created by the authorized principal and
// binds its image.
func insertMapPlace(ctx context.Context, store Store, command CreateMapPlaceCommand) (MapPlace, error) {
	memberID := auth.GetUser(ctx).MemberID.String()
	place := MapPlace{
		Name: command.Name, Address: command.Address, Lat: command.Lat, Lng: command.Lng,
		AddressComponents: command.AddressComponents, GooglePlaceID: normalizeGooglePlaceID(command.GooglePlaceID),
		CreatedByMemberID: &memberID, UpdatedByMemberID: &memberID,
	}
	if command.ImageFileID != nil && *command.ImageFileID != "" {
		place.ImageFileID = command.ImageFileID
		if err := store.LockFilesForAttachment(ctx, []string{*place.ImageFileID}); err != nil {
			return MapPlace{}, err
		}
	}
	place, err := store.InsertMapPlace(ctx, place)
	if err != nil {
		return MapPlace{}, err
	}
	if place.ImageFileID != nil {
		if _, err := store.BindReadyAsset(ctx, AssetBinding{
			SourceFileID: *place.ImageFileID,
			Owner:        AssetOwner{Type: "map_place", ID: place.ID}, Key: "image", Kind: "map_image",
		}); err != nil {
			return MapPlace{}, err
		}
	}
	return place, nil
}

// UpdateMapPlace updates an existing map place (author+)
func (s *Service) UpdateMapPlace(ctx context.Context, command UpdateMapPlaceCommand) (_ MapPlaceView, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("map_place_id", command.ID))

	if command.ID == "" {
		return MapPlaceView{}, domainerr.Required("id")
	}
	editCan, err := policyv1.MapPlace.Edit(command.ID)
	if err != nil {
		return MapPlaceView{}, err
	}
	var locked MapPlace
	update, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[mapPlaceUpdatePlan, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			locked, err = lockMapPlace(ctx, store, command.ID)
			return err
		},
		Access: s.mapPlaceAccess(editCan, false),
		Change: func(ctx context.Context, store Store) (mapPlaceUpdatePlan, error) {
			return updateLockedMapPlace(ctx, store, command, auth.GetUser(ctx).MemberID.String(), locked)
		},
		Audit: mapPlaceUpdateAudit,
	})
	if err != nil {
		return MapPlaceView{}, err
	}
	return s.committedMapPlaceView(ctx, update.place)
}

// mapPlaceUpdateAudit records the metadata change and the image change of an
// update, one record each.
func mapPlaceUpdateAudit(update mapPlaceUpdatePlan) mutation.Audit {
	var audits []mutation.Audit
	if len(update.metadataChangedFields) > 0 {
		audits = append(audits, mutation.RequestAudit(sharedtelemetry.AuditMapPlaceUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMapPlaceMetadataUpdatedAuditRecord(metadata, update.place.ID, update.metadataChangedFields)
		}))
	}
	if update.imageChanged {
		audits = append(audits, mutation.RequestAudit(sharedtelemetry.AuditMapPlaceUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMapPlaceImageUpdatedAuditRecord(metadata, update.place.ID, update.imageOperation, update.imageFileID)
		}))
	}
	return mutation.Audits(audits...)
}

// updateLockedMapPlace applies command to the locked place and moves its
// image binding.
func updateLockedMapPlace(
	ctx context.Context,
	store Store,
	command UpdateMapPlaceCommand,
	memberID string,
	place MapPlace,
) (mapPlaceUpdatePlan, error) {
	update, err := planMapPlaceUpdate(place, command, memberID)
	if err != nil || !update.changed() {
		return update, err
	}
	if update.imageChanged {
		if err := store.LockFilesForAttachment(ctx, []string{mapPlaceOptionalString(place.ImageFileID), mapPlaceOptionalString(update.place.ImageFileID)}); err != nil {
			return mapPlaceUpdatePlan{}, err
		}
	}
	if update.place, err = store.SaveMapPlace(ctx, update.place); err != nil {
		return mapPlaceUpdatePlan{}, err
	}
	return update, updateMapPlaceImageBinding(ctx, store, &update)
}

type mapPlaceUpdatePlan struct {
	place                 MapPlace
	metadataChangedFields []string
	imageChanged          bool
	imageOperation        sharedtelemetry.AuditCollectionOperation
	imageFileID           string
}

func (update mapPlaceUpdatePlan) changed() bool {
	return len(update.metadataChangedFields) > 0 || update.imageChanged
}

func planMapPlaceUpdate(
	current MapPlace,
	request UpdateMapPlaceCommand,
	memberID string,
) (mapPlaceUpdatePlan, error) {
	update := mapPlaceUpdatePlan{place: current}
	if request.Name != nil {
		if strings.TrimSpace(*request.Name) == "" {
			return mapPlaceUpdatePlan{}, domainerr.Required("name")
		}
		if *request.Name != current.Name {
			update.place.Name = *request.Name
			update.metadataChangedFields = append(update.metadataChangedFields, "name")
		}
	}
	if request.Address != nil && *request.Address != "" && *request.Address != current.Address {
		update.place.Address = *request.Address
		update.metadataChangedFields = append(update.metadataChangedFields, "address")
	}
	if request.Lat != nil {
		if *request.Lat < -90 || *request.Lat > 90 {
			return mapPlaceUpdatePlan{}, domainerr.InvalidField("lat", "must be between -90 and 90")
		}
		if *request.Lat != current.Lat {
			update.place.Lat = *request.Lat
			update.metadataChangedFields = append(update.metadataChangedFields, "lat")
		}
	}
	if request.Lng != nil {
		if *request.Lng < -180 || *request.Lng > 180 {
			return mapPlaceUpdatePlan{}, domainerr.InvalidField("lng", "must be between -180 and 180")
		}
		if *request.Lng != current.Lng {
			update.place.Lng = *request.Lng
			update.metadataChangedFields = append(update.metadataChangedFields, "lng")
		}
	}
	if request.GooglePlaceID != nil {
		next := normalizeGooglePlaceID(request.GooglePlaceID)
		if !sameMapPlaceOptionalString(next, current.GooglePlaceID) {
			update.place.GooglePlaceID = next
			update.metadataChangedFields = append(update.metadataChangedFields, "google_place_id")
		}
	}
	if request.AddressComponents != nil {
		next := request.AddressComponents
		if !reflect.DeepEqual(next, current.AddressComponents) {
			update.place.AddressComponents = next
			update.metadataChangedFields = append(update.metadataChangedFields, "address_components")
		}
	}
	update.planImage(current, request)
	if update.changed() {
		update.place.UpdatedByMemberID = &memberID
	}
	return update, nil
}

// planImage removes the image on ClearImage, which takes precedence, or
// selects a different supplied one.
func (update *mapPlaceUpdatePlan) planImage(current MapPlace, request UpdateMapPlaceCommand) {
	if request.ClearImage {
		if current.ImageFileID != nil {
			update.place.ImageFileID = nil
			update.imageChanged = true
			update.imageOperation = sharedtelemetry.AuditCollectionOperationRemoved
			update.imageFileID = *current.ImageFileID
		}
	} else if request.ImageFileID != nil && strings.TrimSpace(*request.ImageFileID) != "" && !sameMapPlaceOptionalString(request.ImageFileID, current.ImageFileID) {
		update.place.ImageFileID = request.ImageFileID
		update.imageChanged = true
		update.imageOperation = sharedtelemetry.AuditCollectionOperationAdded
		update.imageFileID = *request.ImageFileID
	}
}

func mapPlaceOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sameMapPlaceOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func updateMapPlaceImageBinding(
	ctx context.Context,
	store Store,
	update *mapPlaceUpdatePlan,
) error {
	if !update.imageChanged {
		return nil
	}
	if update.imageOperation == sharedtelemetry.AuditCollectionOperationRemoved {
		return store.ReleaseAssets(ctx, AssetRelease{
			Owner: AssetOwner{Type: "map_place", ID: update.place.ID}, BindingPrefix: "image",
		})
	}
	_, err := store.BindReadyAsset(ctx, AssetBinding{
		SourceFileID: update.imageFileID,
		Owner:        AssetOwner{Type: "map_place", ID: update.place.ID},
		Key:          "image",
		Kind:         "map_image",
	})
	return err
}

// DeleteMapPlace deletes a map place (admin)
func (s *Service) DeleteMapPlace(ctx context.Context, command DeleteMapPlaceCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("map_place_id", command.ID))

	if command.ID == "" {
		return domainerr.Required("id")
	}
	deleteCan, err := policyv1.MapPlace.Delete(command.ID)
	if err != nil {
		return err
	}
	var place MapPlace
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[MapPlace, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			place, err = lockMapPlace(ctx, store, command.ID)
			return err
		},
		Access: s.mapPlaceAccess(deleteCan, true),
		Change: func(ctx context.Context, store Store) (MapPlace, error) {
			inUse, err := store.MapPlaceInUse(ctx, place.ID)
			if err != nil {
				return MapPlace{}, err
			}
			if inUse {
				return MapPlace{}, domainerr.Precondition("map place is still referenced")
			}
			if err := store.DeleteMapPlace(ctx, place.ID); err != nil {
				return MapPlace{}, err
			}
			return place, store.ReleaseAssets(ctx, AssetRelease{
				Owner: AssetOwner{Type: "map_place", ID: place.ID}, BindingPrefix: "image",
			})
		},
		Audit: func(place MapPlace) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMapPlaceDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMapPlaceDeletedAuditRecord(metadata, place.ID)
			})
		},
		Relationships: func(place MapPlace) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return referencePolicyRelationships(policyv1.MapPlace, place.ID, false)
		},
	})
	return err
}
