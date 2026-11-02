package sitesettingspostgres

import (
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"gorm.io/gorm"
)

// NewAssetReader resolves optional settings assets through the connection pool.
func NewAssetReader(db *gorm.DB, assets Assets) sitesettings.AssetReader {
	if db == nil || assets == nil {
		panic("site settings asset reader dependencies are required")
	}
	if _, inTransaction := db.Statement.ConnPool.(gorm.TxCommitter); inTransaction {
		panic("site settings asset reader requires a connection pool")
	}
	return store{tx: db, ports: Ports{Assets: assets}}
}
