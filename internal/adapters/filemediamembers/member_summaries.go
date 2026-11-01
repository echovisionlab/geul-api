package filemediamembers

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"gorm.io/gorm"
)

type MemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewMemberSummaries(db *gorm.DB, cdnDomain string) *MemberSummaries {
	return &MemberSummaries{db, cdnDomain}
}
func (r *MemberSummaries) Load(ctx context.Context, ids []string) (map[string]domain.MemberSummary, error) {
	summaries, err := memberpostgres.ReadSummaries(ctx, r.db, r.cdnDomain, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[string]domain.MemberSummary, len(summaries))
	for id, value := range summaries {
		projected := domain.MemberSummary{ID: value.ID, Nickname: value.Nickname, Deleted: value.Deleted}
		if avatar := value.Avatar; avatar != nil {
			projected.Avatar = &mediaasset.Reference{AssetID: avatar.ID, URL: avatar.URL, Extension: avatar.Extension, MimeType: avatar.MIMEType, FileSize: avatar.FileSize, SHA256: append([]byte(nil), avatar.SHA256...), Disposition: mediaasset.Disposition(avatar.Disposition), DownloadFilename: avatar.DownloadFilename}
		}
		result[id] = projected
	}
	return result, nil
}
