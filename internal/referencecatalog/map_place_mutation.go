package referencecatalog

import (
	"context"
	"reflect"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *MapPlaceService) CreateMapPlace(
	ctx context.Context,
	req *connect.Request[managev1.CreateMapPlaceRequest],
) (*connect.Response[managev1.MapPlace], error) {
	if req.Msg.Name == "" {
		return nil, errs.Required("name")
	}
	if req.Msg.Address == "" {
		return nil, errs.Required("address")
	}
	if req.Msg.Lat < -90 || req.Msg.Lat > 90 {
		return nil, errs.InvalidArgument("lat", "must be between -90 and 90")
	}
	if req.Msg.Lng < -180 || req.Msg.Lng > 180 {
		return nil, errs.InvalidArgument("lng", "must be between -180 and 180")
	}

	place := model.MapPlace{
		Name:          req.Msg.Name,
		Address:       req.Msg.Address,
		Lat:           req.Msg.Lat,
		Lng:           req.Msg.Lng,
		GooglePlaceID: normalizeGooglePlaceID(req.Msg.GooglePlaceId),
	}

	if req.Msg.AddressComponents != nil {
		place.AddressComponents = &model.AddressComponents{
			Street:     req.Msg.AddressComponents.Street,
			City:       req.Msg.AddressComponents.City,
			Region:     req.Msg.AddressComponents.Region,
			Country:    req.Msg.AddressComponents.Country,
			PostalCode: req.Msg.AddressComponents.PostalCode,
		}
	}

	if req.Msg.ImageFileId != nil && *req.Msg.ImageFileId != "" {
		place.ImageFileID = req.Msg.ImageFileId
	}

	createCan, err := policyv1.MapPlace.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		memberID, err := requireLockedMapPlaceAuthority(ctx, tx, createCan, false, s.spiceDB)
		if err != nil {
			return err
		}
		place.CreatedByMemberID = &memberID
		place.UpdatedByMemberID = &memberID
		if place.ImageFileID != nil {
			if err := s.assets.LockForAttachment(ctx, tx, []string{*place.ImageFileID}); err != nil {
				return err
			}
		}
		if err := tx.Clauses(clause.Returning{}).Create(&place).Error; err != nil {
			return err
		}
		policyTouch, err := policyv1.MapPlace.TouchPolicy(place.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.MapPlace.DeletePolicy(place.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{policyTouch},
			[]policyv1.RelationshipMutation{policyDelete},
		); err != nil {
			return err
		}
		if place.ImageFileID != nil {
			if _, err := s.assets.BindReady(ctx, tx, AssetBinding{
				SourceFileID: *place.ImageFileID,
				Owner:        AssetOwner{Type: "map_place", ID: place.ID}, Key: "image", Kind: "map_image",
			}); err != nil {
				return err
			}
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditMapPlaceCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMapPlaceCreatedAuditRecord(metadata, place.ID)
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	users, err := s.resolveMembersForPlaces(ctx, []model.MapPlace{place})
	if err != nil {
		return nil, errs.Internal(err)
	}

	proto, err := s.toProtoWithMembers(ctx, &place, users)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

// UpdateMapPlace updates an existing map place (author+)
func (s *MapPlaceService) UpdateMapPlace(
	ctx context.Context,
	req *connect.Request[managev1.UpdateMapPlaceRequest],
) (*connect.Response[managev1.MapPlace], error) {
	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}
	editCan, err := policyv1.MapPlace.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	var place model.MapPlace
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockMapPlaceForUpdate(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		memberID, err := requireLockedMapPlaceAuthority(ctx, tx, editCan, false, s.spiceDB)
		if err != nil {
			return err
		}
		updated, err := s.updateLockedMapPlaceWithDB(ctx, tx, req.Msg, memberID, locked)
		place = updated
		return err
	}); err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("map place", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	users, err := s.resolveMembersForPlaces(ctx, []model.MapPlace{place})
	if err != nil {
		return nil, errs.Internal(err)
	}

	proto, err := s.toProtoWithMembers(ctx, &place, users)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func (s *MapPlaceService) updateLockedMapPlaceWithDB(
	ctx context.Context,
	tx *gorm.DB,
	request *managev1.UpdateMapPlaceRequest,
	memberID string,
	place model.MapPlace,
) (model.MapPlace, error) {
	update, err := planMapPlaceUpdate(place, request, memberID)
	if err != nil {
		return model.MapPlace{}, err
	}
	if !update.changed() {
		return place, nil
	}
	if update.imageChanged {
		if err := s.assets.LockForAttachment(ctx, tx, []string{mapPlaceOptionalString(place.ImageFileID), mapPlaceOptionalString(update.place.ImageFileID)}); err != nil {
			return model.MapPlace{}, err
		}
	}
	if err := tx.Save(&update.place).Error; err != nil {
		return model.MapPlace{}, err
	}
	if err := s.updateMapPlaceImageBinding(ctx, tx, &update); err != nil {
		return model.MapPlace{}, err
	}
	if len(update.metadataChangedFields) > 0 {
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditMapPlaceUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMapPlaceMetadataUpdatedAuditRecord(metadata, update.place.ID, update.metadataChangedFields)
		}); err != nil {
			return model.MapPlace{}, err
		}
	}
	if update.imageChanged {
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditMapPlaceUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMapPlaceImageUpdatedAuditRecord(metadata, update.place.ID, update.imageOperation, update.imageFileID)
		}); err != nil {
			return model.MapPlace{}, err
		}
	}
	return update.place, nil
}

type mapPlaceUpdatePlan struct {
	place                 model.MapPlace
	metadataChangedFields []string
	imageChanged          bool
	imageOperation        sharedtelemetry.AuditCollectionOperation
	imageFileID           string
}

func (update mapPlaceUpdatePlan) changed() bool {
	return len(update.metadataChangedFields) > 0 || update.imageChanged
}

func planMapPlaceUpdate(
	current model.MapPlace,
	request *managev1.UpdateMapPlaceRequest,
	memberID string,
) (mapPlaceUpdatePlan, error) {
	update := mapPlaceUpdatePlan{place: current}
	if request.Name != nil {
		if strings.TrimSpace(*request.Name) == "" {
			return mapPlaceUpdatePlan{}, errs.Required("name")
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
			return mapPlaceUpdatePlan{}, errs.InvalidArgument("lat", "must be between -90 and 90")
		}
		if *request.Lat != current.Lat {
			update.place.Lat = *request.Lat
			update.metadataChangedFields = append(update.metadataChangedFields, "lat")
		}
	}
	if request.Lng != nil {
		if *request.Lng < -180 || *request.Lng > 180 {
			return mapPlaceUpdatePlan{}, errs.InvalidArgument("lng", "must be between -180 and 180")
		}
		if *request.Lng != current.Lng {
			update.place.Lng = *request.Lng
			update.metadataChangedFields = append(update.metadataChangedFields, "lng")
		}
	}
	if request.GooglePlaceId != nil {
		next := normalizeGooglePlaceID(request.GooglePlaceId)
		if !sameMapPlaceOptionalString(next, current.GooglePlaceID) {
			update.place.GooglePlaceID = next
			update.metadataChangedFields = append(update.metadataChangedFields, "google_place_id")
		}
	}
	if request.AddressComponents != nil {
		next := &model.AddressComponents{
			Street: request.AddressComponents.Street, City: request.AddressComponents.City,
			Region: request.AddressComponents.Region, Country: request.AddressComponents.Country,
			PostalCode: request.AddressComponents.PostalCode,
		}
		if !reflect.DeepEqual(next, current.AddressComponents) {
			update.place.AddressComponents = next
			update.metadataChangedFields = append(update.metadataChangedFields, "address_components")
		}
	}
	if request.ClearImage {
		if current.ImageFileID != nil {
			update.place.ImageFileID = nil
			update.imageChanged = true
			update.imageOperation = sharedtelemetry.AuditCollectionOperationRemoved
			update.imageFileID = *current.ImageFileID
		}
	} else if request.ImageFileId != nil && strings.TrimSpace(*request.ImageFileId) != "" && !sameMapPlaceOptionalString(request.ImageFileId, current.ImageFileID) {
		update.place.ImageFileID = request.ImageFileId
		update.imageChanged = true
		update.imageOperation = sharedtelemetry.AuditCollectionOperationAdded
		update.imageFileID = *request.ImageFileId
	}
	if update.changed() {
		update.place.UpdatedByMemberID = &memberID
	}
	return update, nil
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
func (s *MapPlaceService) updateMapPlaceImageBinding(
	ctx context.Context,
	tx *gorm.DB,
	update *mapPlaceUpdatePlan,
) error {
	if !update.imageChanged {
		return nil
	}
	if update.imageOperation == sharedtelemetry.AuditCollectionOperationRemoved {
		return s.assets.Release(ctx, tx, AssetRelease{
			Owner: AssetOwner{Type: "map_place", ID: update.place.ID}, BindingPrefix: "image",
		})
	}
	_, err := s.assets.BindReady(ctx, tx, AssetBinding{
		SourceFileID: update.imageFileID,
		Owner:        AssetOwner{Type: "map_place", ID: update.place.ID},
		Key:          "image",
		Kind:         "map_image",
	})
	return err
}

// DeleteMapPlace deletes a map place (admin)
func (s *MapPlaceService) DeleteMapPlace(
	ctx context.Context,
	req *connect.Request[managev1.DeleteMapPlaceRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}
	deleteCan, err := policyv1.MapPlace.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}

	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		place, err := lockMapPlaceForUpdate(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if _, err := requireLockedMapPlaceAuthority(ctx, tx, deleteCan, true, s.spiceDB); err != nil {
			return err
		}
		if err := requireMapPlaceUnused(ctx, tx, place.ID); err != nil {
			return err
		}
		if err := tx.Delete(&place).Error; err != nil {
			return err
		}
		policyDelete, err := policyv1.MapPlace.DeletePolicy(place.ID)
		if err != nil {
			return err
		}
		policyTouch, err := policyv1.MapPlace.TouchPolicy(place.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{policyDelete},
			[]policyv1.RelationshipMutation{policyTouch},
		); err != nil {
			return err
		}
		if err := s.assets.Release(ctx, tx, AssetRelease{
			Owner: AssetOwner{Type: "map_place", ID: place.ID}, BindingPrefix: "image",
		}); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditMapPlaceDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMapPlaceDeletedAuditRecord(metadata, place.ID)
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("map place", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}

func requireMapPlaceUnused(ctx context.Context, tx *gorm.DB, placeID string) error {
	for _, table := range []string{"post", "work", "program_event"} {
		var count int64
		if err := tx.WithContext(ctx).Table(table).Where("map_place_id = ?", placeID).Limit(1).Count(&count).Error; err != nil {
			return errs.Internal(err)
		}
		if count > 0 {
			return errs.FailedPrecondition("map place is still referenced")
		}
	}
	return nil
}
