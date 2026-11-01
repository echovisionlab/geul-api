package work

import (
	"context"

	"gorm.io/gorm"

	referencecatalogpostgres "github.com/echovisionlab/geul-api/internal/referencecatalog/postgres"
)

// ClientWorks counts the Work relations of Reference Catalog Clients.
type ClientWorks struct{}

var _ referencecatalogpostgres.ClientWorks = ClientWorks{}

func (ClientWorks) CountByClient(ctx context.Context, db *gorm.DB, clientIDs []string) (map[string]int32, error) {
	var rows []struct {
		ClientID string
		Count    int32
	}
	if err := db.WithContext(ctx).
		Table("work_client").
		Select("client_id, COUNT(*) as count").
		Where("client_id IN ?", clientIDs).
		Group("client_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int32, len(rows))
	for _, row := range rows {
		counts[row.ClientID] = row.Count
	}
	return counts, nil
}
