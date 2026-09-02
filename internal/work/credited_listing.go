package work

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// workSortConfig defines allowed sort fields for works
type creditedWorkRow struct {
	WorkID            string    `gorm:"column:work_id"`
	Title             string    `gorm:"column:title"`
	Slug              *string   `gorm:"column:slug"`
	Type              string    `gorm:"column:type"`
	Status            string    `gorm:"column:status"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	CreditID          string    `gorm:"column:credit_id"`
	CreditRole        *string   `gorm:"column:credit_role"`
	ArtistID          *string   `gorm:"column:artist_id"`
	ArtistName        *string   `gorm:"column:artist_name"`
	ArtistImageFileID *string   `gorm:"column:artist_image_file_id"`
	MemberID          *string   `gorm:"column:member_id"`
}

func (s *WorkService) ListMyCreditedWorks(
	ctx context.Context,
	req *connect.Request[managev1.ListMyCreditedWorksRequest],
) (*connect.Response[managev1.ListMyCreditedWorksResponse], error) {
	principal := auth.GetUser(ctx)
	if principal == nil || principal.MemberID == "" {
		return nil, errs.AuthenticationRequired()
	}

	memberID := principal.MemberID.String()

	var artistIDs []string
	var err error
	if err := s.db.WithContext(ctx).Raw(`
		SELECT artist_id::text FROM artist_owner WHERE member_id = ?::uuid
		UNION
		SELECT artist_id::text FROM artist_manager WHERE member_id = ?::uuid
	`, memberID, memberID).Scan(&artistIDs).Error; err != nil {
		return nil, errs.Internal(err)
	}

	query := s.db.WithContext(ctx).
		Table("work_credit wc").
		Joins("JOIN work w ON w.id = wc.work_id").
		Joins("LEFT JOIN artist a ON a.id = wc.artist_id").
		Joins(`
			LEFT JOIN (
				SELECT DISTINCT ON (artist_id) artist_id, file_id
				FROM artist_file
				ORDER BY artist_id, sort_order ASC, created_at ASC
			) afi ON afi.artist_id = a.id
		`)

	if len(artistIDs) > 0 {
		query = query.Where("(wc.member_id = ? OR wc.artist_id IN ?)", memberID, artistIDs)
	} else {
		query = query.Where("wc.member_id = ?", memberID)
	}

	query, err = myCreditedWorkFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	pg := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)

	query, err = myCreditedWorkSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	var rows []creditedWorkRow
	err = pg.Apply(query).
		Select(`
			w.id AS work_id,
			` + WorkSourceTitleSQL("w") + ` AS title,
			w.slug AS slug,
			w.type AS type,
			w.status AS status,
			w.created_at AS created_at,
			wc.id AS credit_id,
			wc.credit_role AS credit_role,
			wc.artist_id AS artist_id,
			` + ArtistSourceTitleSQL("a") + ` AS artist_name,
			afi.file_id AS artist_image_file_id,
			wc.member_id AS member_id
		`).
		Scan(&rows).Error
	if err != nil {
		return nil, errs.Internal(err)
	}

	if s.members == nil {
		return nil, errs.InternalMsg("Work Member summary loader is not configured")
	}
	memberSummaries, err := s.members.LoadMemberSummaries(ctx, []string{memberID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoWorks := make([]*managev1.MyCreditedWork, 0, len(rows))
	for _, row := range rows {
		credited, err := s.projectCreditedWork(ctx, row, memberID, memberSummaries[memberID])
		if err != nil {
			return nil, err
		}
		protoWorks = append(protoWorks, credited)
	}

	return connect.NewResponse(&managev1.ListMyCreditedWorksResponse{
		Works:      protoWorks,
		Pagination: pg.BuildResponse(total),
	}), nil
}

func (s *WorkService) projectCreditedWork(
	ctx context.Context,
	row creditedWorkRow,
	memberID string,
	memberSummary *commonv1.MemberSummary,
) (*managev1.MyCreditedWork, error) {
	workType := managev1.WorkType(managev1.WorkType_value[row.Type])
	if workType == managev1.WorkType_WORK_TYPE_UNSPECIFIED {
		workType = managev1.WorkType_WORK_TYPE_MUSIC_PROJECT
	}
	workStatus := managev1.WorkStatus(managev1.WorkStatus_value[row.Status])
	if workStatus == managev1.WorkStatus_WORK_STATUS_UNSPECIFIED {
		workStatus = managev1.WorkStatus_WORK_STATUS_DRAFT
	}
	credited := &managev1.MyCreditedWork{
		WorkId:     row.WorkID,
		Title:      row.Title,
		Slug:       row.Slug,
		Type:       workType,
		Status:     workStatus,
		CreditId:   row.CreditID,
		CreditRole: row.CreditRole,
		CreatedAt:  timestamppb.New(row.CreatedAt),
	}
	if row.MemberID != nil && *row.MemberID == memberID {
		if memberSummary == nil {
			return nil, errs.InternalMsg("credited member is missing")
		}
		credited.CreditType = managev1.MyCreditedWorkCreditType_MY_CREDITED_WORK_CREDIT_TYPE_MEMBER
		credited.CreditedAs = memberSummary.GetNickname()
		credited.CreditedAsImageAsset = memberSummary.AvatarAsset
		return credited, nil
	}
	credited.CreditType = managev1.MyCreditedWorkCreditType_MY_CREDITED_WORK_CREDIT_TYPE_ARTIST
	credited.CreditedAs = "Unknown Artist"
	if row.ArtistName != nil && strings.TrimSpace(*row.ArtistName) != "" {
		credited.CreditedAs = *row.ArtistName
	}
	if row.ArtistImageFileID == nil {
		return credited, nil
	}
	asset, err := s.runtime.ReadyPublicAssetRefForSourceFile(
		ctx, s.db, *row.ArtistImageFileID, "image",
	)
	if err != nil {
		return nil, err
	}
	credited.CreditedAsImageAsset = asset
	return credited, nil
}

// =============================================================================
// Utility
// =============================================================================

// CheckWorkSlugAvailable checks if a slug is available (requires edit permission)
