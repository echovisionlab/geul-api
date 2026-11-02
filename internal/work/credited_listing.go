package work

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"

	queryutil "github.com/echovisionlab/geul-api/internal/query"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// ListMyCreditedWorksQuery lists the credits of the caller and of the
// Artists the caller owns or manages.
type ListMyCreditedWorksQuery struct {
	Page    Page
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// CreditType names whom a credited Work credits.
type CreditType uint8

const (
	CreditTypeArtist CreditType = iota + 1
	CreditTypeMember
)

// CreditedWork is one credit of the caller or of an Artist the caller owns
// or manages, with its Work.
type CreditedWork struct {
	WorkID       string
	SourceLocale string
	Title        string
	Slug         *string
	Type         string
	Status       Status
	CreditID     string
	CreditRole   *string
	CreditType   CreditType
	CreditedAs   string
	Image        *mediaasset.Reference
	CreatedAt    time.Time
}

// CreditedWorkPage is one page of credited Works.
type CreditedWorkPage struct {
	Works      []CreditedWork
	Pagination Pagination
}

func (s *Service) ListMyCreditedWorks(ctx context.Context, query ListMyCreditedWorksQuery) (CreditedWorkPage, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || principal.MemberID == "" {
		return CreditedWorkPage{}, domainerr.AuthenticationRequired()
	}

	memberID := principal.MemberID.String()

	var artistIDs []string
	if err := s.read(ctx, func(store Store) error {
		var err error
		artistIDs, err = store.CreditingArtistIDs(ctx, memberID)
		return err
	}); err != nil {
		return CreditedWorkPage{}, err
	}

	pg := queryutil.ExtractPaginationRaw(query.Page.Limit, query.Page.Offset, queryutil.DefaultPaginationConfig)
	var rows []CreditedRow
	var total int64
	if err := s.read(ctx, func(store Store) error {
		var err error
		rows, total, err = store.ListCreditedWorks(ctx, CreditedPage{
			MemberID: memberID, ArtistIDs: artistIDs, Filters: query.Filters, Sorts: query.Sorts,
			Limit: pg.Limit, Offset: pg.Offset,
		})
		return err
	}); err != nil {
		return CreditedWorkPage{}, err
	}

	memberSummaries, err := s.members.LoadMemberSummaries(ctx, []string{memberID})
	if err != nil {
		return CreditedWorkPage{}, err
	}
	works := make([]CreditedWork, 0, len(rows))
	for _, row := range rows {
		credited, err := s.projectCreditedWork(ctx, row, memberID, memberSummaries[memberID])
		if err != nil {
			return CreditedWorkPage{}, err
		}
		works = append(works, credited)
	}

	return CreditedWorkPage{Works: works, Pagination: paginationOf(pg, total)}, nil
}

func (s *Service) projectCreditedWork(
	ctx context.Context,
	row CreditedRow,
	memberID string,
	memberSummary *memberref.Summary,
) (CreditedWork, error) {
	credited := CreditedWork{
		WorkID:       row.WorkID,
		SourceLocale: row.SourceLocale,
		Title:        row.Title,
		Slug:         row.Slug,
		Type:         row.Type,
		Status:       Status(row.Status),
		CreditID:     row.CreditID,
		CreditRole:   row.CreditRole,
		CreatedAt:    row.CreatedAt,
	}
	if row.MemberID != nil && *row.MemberID == memberID {
		if memberSummary == nil {
			return CreditedWork{}, errors.New("credited member is missing")
		}
		credited.CreditType = CreditTypeMember
		credited.CreditedAs = memberSummary.Nickname
		credited.Image = memberSummary.Avatar.Reference()
		return credited, nil
	}
	credited.CreditType = CreditTypeArtist
	credited.CreditedAs = "Unknown Artist"
	if row.ArtistName != nil && strings.TrimSpace(*row.ArtistName) != "" {
		credited.CreditedAs = *row.ArtistName
	}
	if row.ArtistImageFileID == nil {
		return credited, nil
	}
	asset, err := s.readyFileAsset(ctx, *row.ArtistImageFileID, "image")
	if err != nil {
		return CreditedWork{}, err
	}
	credited.Image = asset
	return credited, nil
}
