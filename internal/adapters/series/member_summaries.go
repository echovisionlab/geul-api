package series

import (
	"context"

	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"

	"gorm.io/gorm"

	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
)

// MemberSummaries adapts Member-owned summaries to Series manager projections.
type MemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewMemberSummaries(db *gorm.DB, cdnDomain string) *MemberSummaries {
	if db == nil {
		panic("series member summaries: db is required")
	}
	return &MemberSummaries{db: db, cdnDomain: cdnDomain}
}

func (a *MemberSummaries) LoadSeriesManagers(
	ctx context.Context,
	memberIDs []string,
) (map[string]seriesdomain.ManagerSummary, error) {
	summaries, err := memberpostgres.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]seriesdomain.ManagerSummary, len(summaries))
	for memberID, summary := range summaries {
		if summary == nil {
			continue
		}
		var avatar *seriesdomain.Asset
		if source := summary.Avatar; source != nil {
			avatar = &seriesdomain.Asset{ID: source.ID, URL: source.URL, Extension: source.Extension, MIMEType: source.MIMEType, FileSize: source.FileSize, SHA256: source.SHA256, Disposition: seriesdomain.AssetDisposition(source.Disposition), DownloadFilename: source.DownloadFilename}
		}
		result[memberID] = seriesdomain.ManagerSummary{
			MemberID: memberID,
			Nickname: summary.Nickname,
			Avatar:   avatar,
		}
	}
	return result, nil
}
