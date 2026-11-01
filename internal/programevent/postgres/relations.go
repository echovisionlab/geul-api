package programeventpostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/programevent"
)

// relationTables is the junction model and related ID column of each
// relation kind.
var relationTables = map[programevent.RelationKind]struct {
	model    func() any
	idColumn string
}{
	programevent.ArtistRelation: {func() any { return &model.ProgramEventArtist{} }, "artist_id"},
	programevent.LabelRelation:  {func() any { return &model.ProgramEventLabel{} }, "label_id"},
	programevent.ClientRelation: {func() any { return &model.ProgramEventClient{} }, "client_id"},
}

func (s *store) Relations(ctx context.Context, kind programevent.RelationKind, eventID string) ([]programevent.Relation, error) {
	order := "sort_order ASC, " + relationTables[kind].idColumn + " ASC"
	query := s.tx.WithContext(ctx).Where("event_id = ?", eventID).Order(order)
	switch kind {
	case programevent.ArtistRelation:
		var rows []model.ProgramEventArtist
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		relations := make([]programevent.Relation, 0, len(rows))
		for _, row := range rows {
			relations = append(relations, programevent.Relation{ID: row.ArtistID, Role: row.Role, SortOrder: row.SortOrder})
		}
		return relations, nil
	case programevent.LabelRelation:
		var rows []model.ProgramEventLabel
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		relations := make([]programevent.Relation, 0, len(rows))
		for _, row := range rows {
			relations = append(relations, programevent.Relation{ID: row.LabelID, Role: row.Role, SortOrder: row.SortOrder})
		}
		return relations, nil
	default:
		var rows []model.ProgramEventClient
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		relations := make([]programevent.Relation, 0, len(rows))
		for _, row := range rows {
			relations = append(relations, programevent.Relation{ID: row.ClientID, Role: row.Role, SortOrder: row.SortOrder})
		}
		return relations, nil
	}
}

// relationRow is the junction row of one relation.
func relationRow(kind programevent.RelationKind, eventID string, relation programevent.Relation, createdAt time.Time) any {
	switch kind {
	case programevent.ArtistRelation:
		return model.ProgramEventArtist{EventID: eventID, ArtistID: relation.ID, Role: relation.Role, SortOrder: relation.SortOrder, CreatedAt: createdAt}
	case programevent.LabelRelation:
		return model.ProgramEventLabel{EventID: eventID, LabelID: relation.ID, Role: relation.Role, SortOrder: relation.SortOrder, CreatedAt: createdAt}
	default:
		return model.ProgramEventClient{EventID: eventID, ClientID: relation.ID, Role: relation.Role, SortOrder: relation.SortOrder, CreatedAt: createdAt}
	}
}

func (s *store) DeleteRelations(ctx context.Context, kind programevent.RelationKind, eventID string) error {
	return s.tx.WithContext(ctx).Delete(relationTables[kind].model(), "event_id = ?", eventID).Error
}

func (s *store) InsertRelations(ctx context.Context, kind programevent.RelationKind, eventID string, relations []programevent.Relation, createdAt time.Time) error {
	switch kind {
	case programevent.ArtistRelation:
		rows := make([]model.ProgramEventArtist, 0, len(relations))
		for _, relation := range relations {
			rows = append(rows, relationRow(kind, eventID, relation, createdAt).(model.ProgramEventArtist))
		}
		return s.tx.WithContext(ctx).Create(&rows).Error
	case programevent.LabelRelation:
		rows := make([]model.ProgramEventLabel, 0, len(relations))
		for _, relation := range relations {
			rows = append(rows, relationRow(kind, eventID, relation, createdAt).(model.ProgramEventLabel))
		}
		return s.tx.WithContext(ctx).Create(&rows).Error
	default:
		rows := make([]model.ProgramEventClient, 0, len(relations))
		for _, relation := range relations {
			rows = append(rows, relationRow(kind, eventID, relation, createdAt).(model.ProgramEventClient))
		}
		return s.tx.WithContext(ctx).Create(&rows).Error
	}
}

func (s *store) InsertRelation(ctx context.Context, kind programevent.RelationKind, eventID string, relation programevent.Relation) error {
	// A zero created_at is the time gorm stamps on create.
	switch row := relationRow(kind, eventID, relation, time.Time{}).(type) {
	case model.ProgramEventArtist:
		return s.tx.WithContext(ctx).Create(&row).Error
	case model.ProgramEventLabel:
		return s.tx.WithContext(ctx).Create(&row).Error
	case model.ProgramEventClient:
		return s.tx.WithContext(ctx).Create(&row).Error
	default:
		return nil
	}
}

func (s *store) DeleteRelation(ctx context.Context, kind programevent.RelationKind, eventID, relatedID string) (int64, error) {
	result := s.tx.WithContext(ctx).Where("event_id = ? AND "+relationTables[kind].idColumn+" = ?", eventID, relatedID).Delete(relationTables[kind].model())
	return result.RowsAffected, result.Error
}

func (s *store) SetRelationRole(ctx context.Context, kind programevent.RelationKind, eventID, relatedID string, role *string) (int64, error) {
	result := s.tx.WithContext(ctx).Model(relationTables[kind].model()).Where("event_id = ? AND "+relationTables[kind].idColumn+" = ?", eventID, relatedID).Updates(map[string]any{"role": role})
	return result.RowsAffected, result.Error
}
