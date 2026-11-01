package referencecatalogpostgres

import (
	"context"
	"fmt"

	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/structured"
)

var clientFilterConfig = nameSearchFilterConfig()

// clientSortConfig defines allowed sort fields for clients
var clientSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"id":         "id",
		"name":       "name",
		"website":    "website",
		"created_at": "created_at",
	},
	DefaultSort: "name ASC, id ASC",
}

// clientLogoColumn is the column of a logo slot.
func clientLogoColumn(slot referencecatalog.ClientLogoSlot) (string, error) {
	switch slot {
	case referencecatalog.ClientLogoLight:
		return "logo_light_file_id", nil
	case referencecatalog.ClientLogoDark:
		return "logo_dark_file_id", nil
	default:
		return "", fmt.Errorf("unknown client logo slot %q", slot)
	}
}

func clientFromRow(row model.Client) referencecatalog.Client {
	return referencecatalog.Client{
		ID: row.ID, Name: row.Name, Website: row.Website,
		LogoLightFileID: row.LogoLightFileID, LogoDarkFileID: row.LogoDarkFileID, CreatedAt: row.CreatedAt,
	}
}

func clientsFromRows(rows []model.Client) []referencecatalog.Client {
	clients := make([]referencecatalog.Client, len(rows))
	for i := range rows {
		clients[i] = clientFromRow(rows[i])
	}
	return clients
}

func (s store) Client(ctx context.Context, id string) (referencecatalog.Client, error) {
	var client model.Client
	if err := s.tx.WithContext(ctx).First(&client, "id = ?", id).Error; err != nil {
		return referencecatalog.Client{}, notFound(err, referencecatalog.ErrClientNotFound)
	}
	return clientFromRow(client), nil
}

func (s store) ListClients(ctx context.Context, page referencecatalog.ListPage) ([]referencecatalog.Client, int64, error) {
	scoped, err := clientFilterConfig.ApplyFilters(s.tx.WithContext(ctx).Model(&model.Client{}), page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := scoped.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	scoped, err = clientSortConfig.ApplySort(scoped, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var clients []model.Client
	if err := scoped.Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&clients).Error; err != nil {
		return nil, 0, err
	}
	return clientsFromRows(clients), total, nil
}

func (s store) SearchClients(ctx context.Context, query string, limit int) ([]referencecatalog.Client, error) {
	var clients []model.Client
	scoped := s.tx.WithContext(ctx).Model(&model.Client{})
	if query != "" {
		scoped = scoped.Where("name ILIKE ?", "%"+query+"%")
	}
	if err := scoped.Order("name ASC").Limit(limit).Find(&clients).Error; err != nil {
		return nil, err
	}
	return clientsFromRows(clients), nil
}

func (s store) InsertClient(_ context.Context, name string, website *string) (referencecatalog.Client, error) {
	client := &model.Client{Name: name, Website: website}
	if err := s.tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}}}).Create(client).Error; err != nil {
		return referencecatalog.Client{}, clientNameTaken(err)
	}
	return clientFromRow(*client), nil
}

func (s store) LockClient(ctx context.Context, id string) (referencecatalog.Client, error) {
	var client model.Client
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&client, "id = ?", id).Error; err != nil {
		return referencecatalog.Client{}, notFound(err, referencecatalog.ErrClientNotFound)
	}
	return clientFromRow(client), nil
}

// UpdateClient stores changes and rereads the row.
func (s store) UpdateClient(ctx context.Context, id string, changes referencecatalog.ClientChanges) (referencecatalog.Client, error) {
	updates := structured.Fields{}
	if changes.Name != nil {
		updates["name"] = *changes.Name
	}
	if changes.Website != nil {
		updates["website"] = *changes.Website
	}
	client := &model.Client{ID: id}
	if err := s.tx.WithContext(ctx).Model(client).Updates(updates).Error; err != nil {
		return referencecatalog.Client{}, clientNameTaken(err)
	}
	if err := s.tx.First(client, "id = ?", client.ID).Error; err != nil {
		return referencecatalog.Client{}, notFound(err, referencecatalog.ErrClientNotFound)
	}
	return clientFromRow(*client), nil
}

// ClientInUse relies on the caller's root row lock, as ReferenceInUse does.
func (s store) ClientInUse(_ context.Context, id string) (bool, error) {
	for _, relation := range []struct{ table string }{{"work_client"}, {"program_event_client"}} {
		var count int64
		if err := s.tx.Table(relation.table).Where("client_id = ?", id).Limit(1).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s store) DeleteClient(_ context.Context, id string) error {
	return s.tx.Delete(&model.Client{ID: id}).Error
}

func (s store) ClientLogoFiles(ctx context.Context, id string) (light, dark *string, err error) {
	var row struct {
		LightFileID *string `gorm:"column:logo_light_file_id"`
		DarkFileID  *string `gorm:"column:logo_dark_file_id"`
	}
	if err := s.tx.WithContext(ctx).Table("client").
		Select("logo_light_file_id, logo_dark_file_id").
		Where("id = ?", id).
		Take(&row).Error; err != nil {
		return nil, nil, notFound(err, referencecatalog.ErrClientNotFound)
	}
	return row.LightFileID, row.DarkFileID, nil
}

func (s store) LockClientLogo(ctx context.Context, id string, slot referencecatalog.ClientLogoSlot) (*string, error) {
	column, err := clientLogoColumn(slot)
	if err != nil {
		return nil, err
	}
	var current struct {
		SelectedFileID *string `gorm:"column:selected_file_id"`
	}
	if err := s.tx.WithContext(ctx).Table("client").
		Select(column+" AS selected_file_id").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", id).
		Take(&current).Error; err != nil {
		return nil, notFound(err, referencecatalog.ErrClientNotFound)
	}
	return current.SelectedFileID, nil
}

func (s store) SetClientLogo(_ context.Context, id string, slot referencecatalog.ClientLogoSlot, fileID *string) error {
	column, err := clientLogoColumn(slot)
	if err != nil {
		return err
	}
	var value any
	if fileID != nil {
		value = *fileID
	}
	result := s.tx.Table("client").Where("id = ?", id).Updates(structured.Fields{column: value})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return referencecatalog.ErrClientNotFound
	}
	return nil
}
