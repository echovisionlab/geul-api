package programevent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *ProgramEventService) AddProgramEventCredit(
	ctx context.Context,
	req *connect.Request[managev1.AddProgramEventCreditRequest],
) (*connect.Response[managev1.ProgramEventCredit], error) {
	if strings.TrimSpace(req.Msg.EventId) == "" {
		return nil, errs.Required("event_id")
	}
	if countPresent(req.Msg.ArtistId, req.Msg.MemberId, req.Msg.DisplayName) != 1 {
		return nil, errs.InvalidArgument("credit", "exactly one of artist_id, user_id, display_name is required")
	}

	var credit model.ProgramEventCredit
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProgramEventForMutation(ctx, tx, req.Msg.EventId); err != nil {
			return err
		}
		if err := requireProgramEventMutationPermissionWithDB(ctx, tx, s.spiceDB, req.Msg.EventId, policyv1.ProgramEvent.Edit); err != nil {
			return err
		}
		if memberID := strings.TrimSpace(req.Msg.GetMemberId()); memberID != "" {
			if err := authorizationtarget.LockReferences(ctx, tx, []authorizationtarget.Reference{{
				MemberID: memberID,
				Field:    "member_id",
			}}); err != nil {
				return err
			}
		}
		sortOrder := req.Msg.GetSortOrder()
		if req.Msg.SortOrder == nil {
			next, err := nextProgramEventCreditSortOrder(ctx, tx, req.Msg.EventId)
			if err != nil {
				return err
			}
			sortOrder = next
		}
		credit = model.ProgramEventCredit{
			EventID:     req.Msg.EventId,
			ArtistID:    normalizedStringPtr(req.Msg.ArtistId),
			MemberID:    normalizedStringPtr(req.Msg.MemberId),
			DisplayName: normalizedStringPtr(req.Msg.DisplayName),
			CreditRole:  normalizedStringPtr(req.Msg.CreditRole),
			Description: normalizedStringPtr(req.Msg.Description),
			SortOrder:   sortOrder,
			CreatedAt:   time.Now().UTC(),
		}
		if err := tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&credit).Error; err != nil {
			return errs.Internal(err)
		}
		if err := touchProgramEvent(ctx, tx, req.Msg.EventId); err != nil {
			return err
		}
		return s.appendProgramEventChildAudit(ctx, tx, req.Msg.EventId, "credits", credit.ID, sharedtelemetry.AuditItemOperationCreated)
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(s.toProtoProgramEventCredit(ctx, &credit)), nil
}

func (s *ProgramEventService) UpdateProgramEventCredit(
	ctx context.Context,
	req *connect.Request[managev1.UpdateProgramEventCreditRequest],
) (*connect.Response[managev1.ProgramEventCredit], error) {
	if strings.TrimSpace(req.Msg.EventId) == "" {
		return nil, errs.Required("event_id")
	}
	if strings.TrimSpace(req.Msg.CreditId) == "" {
		return nil, errs.Required("credit_id")
	}

	var credit model.ProgramEventCredit
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProgramEventForMutation(ctx, tx, req.Msg.EventId); err != nil {
			return err
		}
		if err := requireProgramEventMutationPermissionWithDB(ctx, tx, s.spiceDB, req.Msg.EventId, policyv1.ProgramEvent.Edit); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).
			First(&credit, "event_id = ? AND id = ?", req.Msg.EventId, req.Msg.CreditId).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("program event credit", req.Msg.CreditId)
			}
			return errs.Internal(err)
		}
		updates := structured.Fields{}
		if req.Msg.CreditRole != nil {
			updates["credit_role"] = normalizedStringPtr(req.Msg.CreditRole)
		}
		if req.Msg.Description != nil {
			updates["description"] = normalizedStringPtr(req.Msg.Description)
		}
		if req.Msg.SortOrder != nil {
			updates["sort_order"] = req.Msg.GetSortOrder()
		}
		changed := programEventCreditChanged(credit, updates)
		if changed {
			if err := tx.WithContext(ctx).Model(&credit).Updates(updates).Error; err != nil {
				return errs.Internal(err)
			}
			if err := tx.WithContext(ctx).
				First(&credit, "event_id = ? AND id = ?", req.Msg.EventId, req.Msg.CreditId).Error; err != nil {
				return errs.Internal(err)
			}
		}
		if !changed {
			return nil
		}
		if err := touchProgramEvent(ctx, tx, req.Msg.EventId); err != nil {
			return err
		}
		return s.appendProgramEventChildAudit(ctx, tx, req.Msg.EventId, "credits", credit.ID, sharedtelemetry.AuditItemOperationUpdated)
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(s.toProtoProgramEventCredit(ctx, &credit)), nil
}

func (s *ProgramEventService) DeleteProgramEventCredit(
	ctx context.Context,
	req *connect.Request[managev1.DeleteProgramEventCreditRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if strings.TrimSpace(req.Msg.EventId) == "" {
		return nil, errs.Required("event_id")
	}
	if strings.TrimSpace(req.Msg.CreditId) == "" {
		return nil, errs.Required("credit_id")
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProgramEventForMutation(ctx, tx, req.Msg.EventId); err != nil {
			return err
		}
		if err := requireProgramEventMutationPermissionWithDB(ctx, tx, s.spiceDB, req.Msg.EventId, policyv1.ProgramEvent.Edit); err != nil {
			return err
		}
		var credit model.ProgramEventCredit
		if err := tx.WithContext(ctx).First(&credit, "event_id = ? AND id = ?", req.Msg.EventId, req.Msg.CreditId).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("program event credit", req.Msg.CreditId)
			}
			return errs.Internal(err)
		}
		result := tx.WithContext(ctx).
			Delete(&model.ProgramEventCredit{}, "event_id = ? AND id = ?", req.Msg.EventId, req.Msg.CreditId)
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected == 0 {
			return errs.NotFound("program event credit", req.Msg.CreditId)
		}
		if err := touchProgramEvent(ctx, tx, req.Msg.EventId); err != nil {
			return err
		}
		return s.appendProgramEventChildAudit(ctx, tx, req.Msg.EventId, "credits", credit.ID, sharedtelemetry.AuditItemOperationDeleted)
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func (s *ProgramEventService) ReorderProgramEventCredits(
	ctx context.Context,
	req *connect.Request[managev1.ReorderProgramEventCreditsRequest],
) (*connect.Response[managev1.ReorderProgramEventCreditsResponse], error) {
	if strings.TrimSpace(req.Msg.EventId) == "" {
		return nil, errs.Required("event_id")
	}
	var changed bool
	var eventUpdatedAt time.Time
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProgramEventForMutation(ctx, tx, req.Msg.EventId); err != nil {
			return err
		}
		if err := requireProgramEventMutationPermissionWithDB(ctx, tx, s.spiceDB, req.Msg.EventId, policyv1.ProgramEvent.Edit); err != nil {
			return err
		}
		seen := make(map[string]struct{}, len(req.Msg.CreditIds))
		for _, creditID := range req.Msg.CreditIds {
			if strings.TrimSpace(creditID) == "" {
				return errs.Required("credit_id")
			}
			if _, ok := seen[creditID]; ok {
				return errs.InvalidArgument("credit_ids", "duplicate credit id")
			}
			seen[creditID] = struct{}{}
		}
		if len(req.Msg.CreditIds) > 0 {
			var count int64
			if err := tx.WithContext(ctx).
				Model(&model.ProgramEventCredit{}).
				Where("event_id = ? AND id IN ?", req.Msg.EventId, req.Msg.CreditIds).
				Count(&count).Error; err != nil {
				return errs.Internal(err)
			}
			if count != int64(len(req.Msg.CreditIds)) {
				return errs.InvalidArgument("credit_ids", "all credits must belong to the event")
			}
		}
		var current []model.ProgramEventCredit
		if err := tx.WithContext(ctx).Where("event_id = ?", req.Msg.EventId).Order("sort_order ASC, created_at ASC").Find(&current).Error; err != nil {
			return errs.Internal(err)
		}
		if len(current) != len(req.Msg.CreditIds) {
			return errs.InvalidArgument("credit_ids", "credit order must include every item")
		}
		currentIDs := make([]string, len(current))
		for index := range current {
			currentIDs[index] = current[index].ID
		}
		if sameStringSlice(currentIDs, req.Msg.CreditIds) {
			var err error
			eventUpdatedAt, err = programEventRelationMutationUpdatedAt(ctx, tx, req.Msg.EventId, false)
			return err
		}
		changed = true
		for index, creditID := range req.Msg.CreditIds {
			if err := tx.WithContext(ctx).
				Model(&model.ProgramEventCredit{}).
				Where("event_id = ? AND id = ?", req.Msg.EventId, creditID).
				Update("sort_order", index).Error; err != nil {
				return errs.Internal(err)
			}
		}
		var err error
		eventUpdatedAt, err = programEventRelationMutationUpdatedAt(ctx, tx, req.Msg.EventId, true)
		if err != nil {
			return err
		}
		return s.appendProgramEventChildOrderAudit(ctx, tx, req.Msg.EventId, "credits", req.Msg.CreditIds)
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ReorderProgramEventCreditsResponse{
		EventId:   req.Msg.EventId,
		Changed:   changed,
		CreditIds: req.Msg.CreditIds,
		UpdatedAt: timestamppb.New(eventUpdatedAt),
	}), nil
}

func programEventCreditChanged(credit model.ProgramEventCredit, updates structured.Fields) bool {
	if value, ok := updates["credit_role"].(*string); ok && !sameNullableString(credit.CreditRole, value) {
		return true
	}
	if value, ok := updates["description"].(*string); ok && !sameNullableString(credit.Description, value) {
		return true
	}
	if value, ok := updates["sort_order"].(int32); ok && credit.SortOrder != value {
		return true
	}
	return false
}

func replaceProgramEventArtists(ctx context.Context, db *gorm.DB, eventID string, artists []*managev1.ProgramEventArtist) error {
	return replaceProgramEventRelations(ctx, db, eventID, artists, func(
		artist *managev1.ProgramEventArtist,
		now time.Time,
	) (model.ProgramEventArtist, error) {
		if artist.GetArtistId() == "" {
			return model.ProgramEventArtist{}, errs.Required("artist_id")
		}
		return model.ProgramEventArtist{
			EventID: eventID, ArtistID: artist.GetArtistId(), Role: artist.Role,
			SortOrder: artist.SortOrder, CreatedAt: now,
		}, nil
	})
}

func replaceProgramEventLabels(ctx context.Context, db *gorm.DB, eventID string, labels []*managev1.ProgramEventLabel) error {
	return replaceProgramEventRelations(ctx, db, eventID, labels, func(
		label *managev1.ProgramEventLabel,
		now time.Time,
	) (model.ProgramEventLabel, error) {
		if label.GetLabelId() == "" {
			return model.ProgramEventLabel{}, errs.Required("label_id")
		}
		return model.ProgramEventLabel{
			EventID: eventID, LabelID: label.GetLabelId(), Role: label.Role,
			SortOrder: label.SortOrder, CreatedAt: now,
		}, nil
	})
}

func replaceProgramEventClients(ctx context.Context, db *gorm.DB, eventID string, clients []*managev1.ProgramEventClient) error {
	return replaceProgramEventRelations(ctx, db, eventID, clients, func(
		client *managev1.ProgramEventClient,
		now time.Time,
	) (model.ProgramEventClient, error) {
		if client.GetClientId() == "" {
			return model.ProgramEventClient{}, errs.Required("client_id")
		}
		return model.ProgramEventClient{
			EventID: eventID, ClientID: client.GetClientId(), Role: client.Role,
			SortOrder: client.SortOrder, CreatedAt: now,
		}, nil
	})
}

func replaceProgramEventRelations[Input interface{}, Row interface{}](
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	inputs []*Input,
	build func(*Input, time.Time) (Row, error),
) error {
	if err := db.WithContext(ctx).Delete(new(Row), "event_id = ?", eventID).Error; err != nil {
		return errs.Internal(err)
	}
	rows := make([]Row, 0, len(inputs))
	now := time.Now().UTC()
	for _, input := range inputs {
		row, err := build(input, now)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	if err := db.WithContext(ctx).Create(&rows).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

type programEventCreditReplacement struct {
	changes      []programEventCreditChange
	orderedIDs   []string
	orderChanged bool
}

type programEventCreditChange struct {
	id        string
	operation sharedtelemetry.AuditItemOperation
}

func replaceProgramEventCredits(ctx context.Context, db *gorm.DB, eventID string, credits []*managev1.ProgramEventCredit) (programEventCreditReplacement, error) {
	result := programEventCreditReplacement{}
	references := make([]authorizationtarget.Reference, 0, len(credits))
	for i, credit := range credits {
		if countPresent(credit.ArtistId, credit.MemberId, credit.DisplayName) != 1 {
			return result, errs.InvalidArgument("credit", "exactly one of artist_id, user_id, display_name is required")
		}
		if credit.MemberId != nil {
			references = append(references, authorizationtarget.Reference{
				MemberID: *credit.MemberId,
				Field:    fmt.Sprintf("credit[%d].member_id", i),
			})
		}
	}
	if err := authorizationtarget.LockReferences(ctx, db, references); err != nil {
		return result, err
	}
	var existing []model.ProgramEventCredit
	if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("event_id = ?", eventID).Order("sort_order ASC, created_at ASC").Find(&existing).Error; err != nil {
		return result, errs.Internal(err)
	}
	previousIDs := make([]string, len(existing))
	byID := make(map[string]model.ProgramEventCredit, len(existing))
	for index := range existing {
		previousIDs[index] = existing[index].ID
		byID[existing[index].ID] = existing[index]
	}
	seen := make(map[string]struct{}, len(credits))
	for _, credit := range credits {
		if credit.GetId() == "" {
			row := model.ProgramEventCredit{EventID: eventID, ArtistID: normalizedStringPtr(credit.ArtistId), MemberID: normalizedStringPtr(credit.MemberId), DisplayName: normalizedStringPtr(credit.DisplayName), CreditRole: normalizedStringPtr(credit.CreditRole), Description: normalizedStringPtr(credit.Description), SortOrder: credit.SortOrder, CreatedAt: time.Now().UTC()}
			if err := db.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
				return result, errs.Internal(err)
			}
			result.changes = append(result.changes, programEventCreditChange{id: row.ID, operation: sharedtelemetry.AuditItemOperationCreated})
			continue
		}
		current, ok := byID[credit.GetId()]
		if !ok {
			return result, errs.InvalidArgument("credits", "credit id does not belong to program event")
		}
		if _, duplicate := seen[current.ID]; duplicate {
			return result, errs.InvalidArgument("credits", "duplicate credit id")
		}
		seen[current.ID] = struct{}{}
		updates := structured.Fields{"artist_id": normalizedStringPtr(credit.ArtistId), "member_id": normalizedStringPtr(credit.MemberId), "display_name": normalizedStringPtr(credit.DisplayName), "credit_role": normalizedStringPtr(credit.CreditRole), "description": normalizedStringPtr(credit.Description), "sort_order": credit.SortOrder}
		if programEventCreditChangedAll(current, updates) {
			if err := db.WithContext(ctx).Model(&current).Updates(updates).Error; err != nil {
				return result, errs.Internal(err)
			}
			result.changes = append(result.changes, programEventCreditChange{id: current.ID, operation: sharedtelemetry.AuditItemOperationUpdated})
		}
	}
	for _, current := range existing {
		if _, keep := seen[current.ID]; keep {
			continue
		}
		if err := db.WithContext(ctx).Delete(&current).Error; err != nil {
			return result, errs.Internal(err)
		}
		result.changes = append(result.changes, programEventCreditChange{id: current.ID, operation: sharedtelemetry.AuditItemOperationDeleted})
	}
	rows, err := loadProgramEventCreditRows(ctx, db, eventID)
	if err != nil {
		return result, err
	}
	result.orderedIDs = make([]string, len(rows))
	for index := range rows {
		result.orderedIDs[index] = rows[index].ID
	}
	result.orderChanged = !sameStringSlice(previousIDs, result.orderedIDs)
	return result, nil
}

func loadProgramEventCreditRows(ctx context.Context, db *gorm.DB, eventID string) ([]model.ProgramEventCredit, error) {
	var rows []model.ProgramEventCredit
	if err := db.WithContext(ctx).Where("event_id = ?", eventID).Order("sort_order ASC, created_at ASC").Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return rows, nil
}

func sameProgramEventCredits(left []model.ProgramEventCredit, right []*managev1.ProgramEventCredit) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if right[index].GetId() == "" || right[index].GetId() != left[index].ID || !sameNullableString(left[index].ArtistID, normalizedStringPtr(right[index].ArtistId)) || !sameNullableString(left[index].MemberID, normalizedStringPtr(right[index].MemberId)) || !sameNullableString(left[index].DisplayName, normalizedStringPtr(right[index].DisplayName)) || !sameNullableString(left[index].CreditRole, normalizedStringPtr(right[index].CreditRole)) || !sameNullableString(left[index].Description, normalizedStringPtr(right[index].Description)) || left[index].SortOrder != right[index].GetSortOrder() {
			return false
		}
	}
	return true
}

func programEventCreditChangedAll(credit model.ProgramEventCredit, updates structured.Fields) bool {
	return !sameNullableString(credit.ArtistID, updates["artist_id"].(*string)) || !sameNullableString(credit.MemberID, updates["member_id"].(*string)) || !sameNullableString(credit.DisplayName, updates["display_name"].(*string)) || programEventCreditChanged(credit, updates)
}

func loadProgramEventArtists(ctx context.Context, db *gorm.DB, eventID string) ([]*managev1.ProgramEventArtist, error) {
	return loadProgramEventRelations(
		ctx, db, eventID, "sort_order ASC, artist_id ASC",
		func(row model.ProgramEventArtist) *managev1.ProgramEventArtist {
			return &managev1.ProgramEventArtist{ArtistId: row.ArtistID, Role: row.Role, SortOrder: row.SortOrder}
		},
	)
}

func loadProgramEventLabels(ctx context.Context, db *gorm.DB, eventID string) ([]*managev1.ProgramEventLabel, error) {
	return loadProgramEventRelations(
		ctx, db, eventID, "sort_order ASC, label_id ASC",
		func(row model.ProgramEventLabel) *managev1.ProgramEventLabel {
			return &managev1.ProgramEventLabel{LabelId: row.LabelID, Role: row.Role, SortOrder: row.SortOrder}
		},
	)
}

func loadProgramEventClients(ctx context.Context, db *gorm.DB, eventID string) ([]*managev1.ProgramEventClient, error) {
	return loadProgramEventRelations(
		ctx, db, eventID, "sort_order ASC, client_id ASC",
		func(row model.ProgramEventClient) *managev1.ProgramEventClient {
			return &managev1.ProgramEventClient{ClientId: row.ClientID, Role: row.Role, SortOrder: row.SortOrder}
		},
	)
}

func loadProgramEventRelations[Row interface{}, Result interface{}](
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	order string,
	convert func(Row) *Result,
) ([]*Result, error) {
	var rows []Row
	if err := db.WithContext(ctx).Where("event_id = ?", eventID).Order(order).Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*Result, 0, len(rows))
	for _, row := range rows {
		result = append(result, convert(row))
	}
	return result, nil
}

func (s *ProgramEventService) loadProgramEventCredits(ctx context.Context, eventID string) ([]*managev1.ProgramEventCredit, error) {
	var rows []model.ProgramEventCredit
	if err := s.db.WithContext(ctx).Where("event_id = ?", eventID).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	memberIDs := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].MemberID != nil {
			memberIDs = append(memberIDs, *rows[i].MemberID)
		}
	}
	summaries, err := s.creditMembers.LoadCreditMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*managev1.ProgramEventCredit, 0, len(rows))
	for i := range rows {
		result = append(result, toProtoProgramEventCredit(ctx, s.db, s.runtime, &rows[i], summaries))
	}
	return result, nil
}

func (s *ProgramEventService) toProtoProgramEventCredit(ctx context.Context, row *model.ProgramEventCredit) *managev1.ProgramEventCredit {
	memberIDs := []string(nil)
	if row != nil && row.MemberID != nil {
		memberIDs = append(memberIDs, *row.MemberID)
	}
	summaries, _ := s.creditMembers.LoadCreditMemberSummaries(ctx, memberIDs)
	return toProtoProgramEventCredit(ctx, s.db, s.runtime, row, summaries)
}

func toProtoProgramEventCredit(ctx context.Context, db *gorm.DB, mediaAssets MediaAssets, row *model.ProgramEventCredit, summaries map[string]*commonv1.MemberSummary) *managev1.ProgramEventCredit {
	credit := &managev1.ProgramEventCredit{
		Id:          row.ID,
		ArtistId:    row.ArtistID,
		MemberId:    row.MemberID,
		DisplayName: row.DisplayName,
		CreditRole:  row.CreditRole,
		Description: row.Description,
		SortOrder:   row.SortOrder,
	}

	if row.ArtistID != nil {
		var artist struct {
			ID   string  `gorm:"column:id"`
			Name string  `gorm:"column:name"`
			Slug *string `gorm:"column:slug"`
		}
		if err := db.WithContext(ctx).
			Table("artist").
			Select("artist.id, "+artistSourceTitleSQL("artist")+" AS name, artist.slug").
			Where("artist.id = ?", *row.ArtistID).
			Scan(&artist).Error; err == nil && artist.ID != "" {
			credit.Artist = &managev1.ProgramEventCreditArtist{
				Id:   artist.ID,
				Name: artist.Name,
				Slug: artist.Slug,
			}
			if imageAsset := loadProgramEventArtistImageAsset(ctx, db, mediaAssets, artist.ID); imageAsset != nil {
				credit.Artist.ImageAsset = imageAsset
			}
		}
	}

	if row.MemberID != nil {
		credit.Member = summaries[*row.MemberID]
	}

	return credit
}

func artistSourceTitleSQL(tableAlias string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "artist"
	}
	return "COALESCE((SELECT translation.title FROM artist_translation AS translation WHERE translation.entity_id = " + alias + ".id AND translation.locale = " + alias + ".source_locale LIMIT 1), '')"
}

func loadProgramEventArtistImageAsset(ctx context.Context, db *gorm.DB, mediaAssets MediaAssets, artistID string) *commonv1.AssetRef {
	var result struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := db.WithContext(ctx).
		Table("artist_file").
		Select("file_id").
		Where("artist_file.artist_id = ?", artistID).
		Order("artist_file.sort_order ASC").
		Limit(1).
		Scan(&result).Error; err != nil || result.FileID == "" {
		return nil
	}
	asset, err := mediaAssets.ReadyPublicAssetRefForSourceFile(ctx, db, result.FileID, "image")
	if err != nil {
		return nil
	}
	return asset
}
