package sitesettings

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

// MCPServerTitleSource projects only the current human-facing Site title into
// MCP initialization metadata. The MCP package retains the stable protocol
// identifier and does not own Site Settings lifecycle.
type MCPServerTitleSource struct {
	db *gorm.DB
}

func NewMCPServerTitleSource(db *gorm.DB) *MCPServerTitleSource {
	if db == nil {
		panic("MCP server title source database is required")
	}
	return &MCPServerTitleSource{db: db}
}

func (source *MCPServerTitleSource) ServerTitle(ctx context.Context) (string, error) {
	var row struct {
		SiteTitle string `gorm:"column:site_title"`
	}
	if err := source.db.WithContext(ctx).Table("site_settings").Select("site_title").Where("id = ?", 1).Take(&row).Error; err != nil {
		return "", err
	}
	return strings.TrimSpace(row.SiteTitle), nil
}
