package label

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Deps are the collaborators of the Label use cases; all are required.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	Audit         domainaudit.Appender
	Publisher     contentsignal.Publisher
	ContentBlocks *contentblock.Store
	Checkpoints   persistencecheckpoint.ContributorFence
	Translation   Translation
	Members       MemberProjection
	Runtime       Runtime
	Artists       ArtistRelations
	Releases      ReleaseRelations
	ProgramEvents ProgramEventRelations
	ShareLinks    ShareLinks
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil, "Publisher": d.Publisher == nil,
		"ContentBlocks": d.ContentBlocks == nil, "Checkpoints": d.Checkpoints == nil,
		"Translation": d.Translation == nil, "Members": d.Members == nil, "Runtime": d.Runtime == nil,
		"Artists": d.Artists == nil, "Releases": d.Releases == nil, "ProgramEvents": d.ProgramEvents == nil,
		"ShareLinks": d.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("label dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Label use cases of the manage API, the collaboration
// service, AI documents and translation delivery. Connect, MCP and AI
// document adapters translate their requests into its commands and queries.
type Service struct {
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	publisher     contentsignal.Publisher
	contentBlocks *contentblock.Store
	checkpoints   persistencecheckpoint.ContributorFence
	translation   Translation
	members       MemberProjection
	runtime       Runtime
	artists       ArtistRelations
	releases      ReleaseRelations
	programEvents ProgramEventRelations
	shareLinks    ShareLinks
	mutations     *mutation.Runner
	participants  entityparticipant.Mutations
}

// NewService validates deps and returns the Label use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, publisher: deps.Publisher, contentBlocks: deps.ContentBlocks,
		checkpoints: deps.Checkpoints, translation: deps.Translation, members: deps.Members, runtime: deps.Runtime,
		artists: deps.Artists, releases: deps.Releases, programEvents: deps.ProgramEvents, shareLinks: deps.ShareLinks,
		mutations: mutations, participants: labelEntity.Mutations(mutations, deps.SpiceDB),
	}, nil
}

// labelSortConfig defines allowed sort fields for labels
var labelSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":         LabelSourceTitleSQL("label"),
		"slug":         "slug",
		"status":       "status",
		"country_code": "country_code",
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
	},
	DefaultSort: LabelSourceTitleSQL("label") + " ASC",
}

// GetLabelEditorDataQuery reads one Label for its editor.
type GetLabelEditorDataQuery struct {
	ID string
}

// EditorData is a Label with the actions the caller may take on it.
type EditorData struct {
	Label          *managev1.Label
	AllowedActions []managev1.LabelAction
}

// GetLabelEditorData returns a Label the caller may view. A caller without
// View learns nothing about whether the Label exists.
func (s *Service) GetLabelEditorData(ctx context.Context, query GetLabelEditorDataQuery) (_ EditorData, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", query.ID))

	var label model.Label
	if err := s.db.WithContext(ctx).
		Where("id = ?", query.ID).
		First(&label).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return EditorData{}, domainerr.ResourceNotFound("label", query.ID)
		}
		return EditorData{}, err
	}

	if err := requireLabelView(ctx, s.spiceDB, label.ID); err != nil {
		return EditorData{}, err
	}
	actions, err := labelAllowedActions(ctx, s.spiceDB, label.ID, label.Status)
	if err != nil {
		return EditorData{}, err
	}
	document, err := s.overlayLabelSourceLocaleDocument(ctx, &label)
	if err != nil {
		return EditorData{}, err
	}

	imageAssets := s.getLabelImageAssets(ctx, label.ID)
	response, err := s.labelWithReadyOg(ctx, &label, document, imageAssets)
	if err != nil {
		return EditorData{}, err
	}
	return EditorData{Label: response, AllowedActions: actions}, nil
}

// ListLabelsQuery pages through Labels. A zero Limit is the default page.
type ListLabelsQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

func (q ListLabelsQuery) pagination() queryutil.Pagination {
	limit := int32(20)
	if q.Limit > 0 {
		limit = q.Limit
	}
	return queryutil.Pagination{Offset: q.Offset, Limit: limit}
}

// LabelPage is one page of Labels.
type LabelPage struct {
	Labels     []*managev1.Label
	Pagination *commonv1.PaginationResponse
}

// ListLabels returns one page of published Labels. This selector surface is
// published-only: draft access belongs to exact resource reads with an
// authorization check or to the administrator list.
func (s *Service) ListLabels(ctx context.Context, query ListLabelsQuery) (LabelPage, error) {
	labels, documents, pagination, err := s.listLabelRows(ctx, s.db.WithContext(ctx).
		Model(&model.Label{}).
		Where("status = ?", model.LabelStatusPublished), query)
	if err != nil {
		return LabelPage{}, err
	}
	projected, err := s.projectLabels(ctx, labels, documents)
	if err != nil {
		return LabelPage{}, err
	}
	return LabelPage{Labels: projected, Pagination: pagination}, nil
}

// AdminLabelPage is one page of Labels with their relation counts.
type AdminLabelPage struct {
	Labels     []*managev1.LabelWithStats
	Pagination *commonv1.PaginationResponse
}

// ListLabelsAdmin returns one page of every Label to a caller with the
// platform-wide list action.
func (s *Service) ListLabelsAdmin(ctx context.Context, query ListLabelsQuery) (AdminLabelPage, error) {
	if err := labelEntity.RequireList(ctx, s.spiceDB); err != nil {
		return AdminLabelPage{}, err
	}
	labels, documents, pagination, err := s.listLabelRows(ctx, s.db.WithContext(ctx).Model(&model.Label{}), query)
	if err != nil {
		return AdminLabelPage{}, err
	}
	projected, err := s.projectLabels(ctx, labels, documents)
	if err != nil {
		return AdminLabelPage{}, err
	}
	labelIDs := make([]string, len(labels))
	for i, l := range labels {
		labelIDs[i] = l.ID
	}
	artistCounts, err := s.artists.CountArtistsByLabel(ctx, s.db, labelIDs)
	if err != nil {
		return AdminLabelPage{}, fmt.Errorf("failed to fetch artist counts: %w", err)
	}
	releaseCounts, err := s.releases.CountReleasesByLabel(ctx, s.db, labelIDs)
	if err != nil {
		return AdminLabelPage{}, fmt.Errorf("failed to fetch release counts: %w", err)
	}
	withStats := make([]*managev1.LabelWithStats, len(labels))
	for i, label := range labels {
		withStats[i] = &managev1.LabelWithStats{
			Label:        projected[i],
			ArtistCount:  artistCounts[label.ID],
			ReleaseCount: releaseCounts[label.ID],
		}
	}
	return AdminLabelPage{Labels: withStats, Pagination: pagination}, nil
}

// listLabelRows filters, counts, sorts and pages base, and overlays each
// Label's source-locale document.
func (s *Service) listLabelRows(ctx context.Context, base *gorm.DB, query ListLabelsQuery) ([]model.Label, []labelDocument, *commonv1.PaginationResponse, error) {
	filtered, err := LabelFilterConfig.ApplyFilters(base, query.Filters)
	if err != nil {
		return nil, nil, nil, err
	}
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return nil, nil, nil, err
	}
	page := query.pagination()
	sorted, err := labelSortConfig.ApplySort(filtered, query.Sorts)
	if err != nil {
		return nil, nil, nil, err
	}
	var labels []model.Label
	if err := sorted.Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&labels).Error; err != nil {
		return nil, nil, nil, err
	}
	documents, err := s.overlayLabelSourceLocaleDocuments(ctx, labels)
	if err != nil {
		return nil, nil, nil, err
	}
	return labels, documents, &commonv1.PaginationResponse{
		Total: int32(total), Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore(total),
	}, nil
}

// projectLabels renders labels with their ready logos and OG images.
func (s *Service) projectLabels(ctx context.Context, labels []model.Label, documents []labelDocument) ([]*managev1.Label, error) {
	readyOgAssets, err := s.loadReadyLabelOgAssets(ctx, labels)
	if err != nil {
		return nil, err
	}
	logoAssets, err := s.loadReadyLabelLogoAssets(ctx, labels)
	if err != nil {
		return nil, err
	}
	projected := make([]*managev1.Label, len(labels))
	for i, label := range labels {
		imageAssets := labelImageAssetsFromReadyMap(&label, logoAssets)
		projected[i] = toProtoLabel(&label, documents[i], imageAssets, manageOgAssetFromReadyMap(readyOgAssets, label.OgAssetID))
	}
	return projected, nil
}
