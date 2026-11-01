package label

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Authorizer is the SpiceDB port of the Label use cases: the fresh checks
// and relationship writes of the mutation pipeline, a participant's
// effective authority and the relationship snapshot a deletion removes.
// *auth.SpiceDBClient implements it.
type Authorizer interface {
	mutation.Authorizer
	entityparticipant.ActorChecker
	SnapshotResourceRelationshipDescriptors(ctx context.Context, plan policyv1.RelationshipSnapshotPlan) ([]auth.RelationshipSnapshot, auth.ZedToken, error)
}

// MemberProjection supplies the Member-owned summaries needed by Label
// participant reads and mutation responses.
type MemberProjection = entityparticipant.Members

// Deps are the collaborators of the Label use cases; all are required.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // labelpostgres.NewUnits
	SpiceDB Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
	Members MemberProjection
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Members": d.Members == nil,
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
	units        mutation.UnitOfWork[Store]
	spiceDB      Authorizer
	members      MemberProjection
	mutations    *mutation.StoreRunner[Store]
	participants entityparticipant.Mutations[Store]
}

// NewService validates deps and returns the Label use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, members: deps.Members, mutations: mutations,
		participants: entityparticipant.NewMutations(labelEntity, mutations, deps.SpiceDB),
	}, nil
}

// GetLabelEditorDataQuery reads one Label for its editor.
type GetLabelEditorDataQuery struct {
	ID string
}

// Action is a Label action the caller may take.
type Action uint8

const (
	ActionEdit Action = iota + 1
	ActionManageShareLinks
	ActionDelete
	ActionManageParticipants
	ActionPublish
	ActionUnpublish
	ActionRemoveOwner
)

// EditorData is a Label with the actions the caller may take on it.
type EditorData struct {
	Label          View
	AllowedActions []Action
}

// GetLabelEditorData returns a Label the caller may view. A caller without
// View learns nothing about whether the Label exists.
func (s *Service) GetLabelEditorData(ctx context.Context, query GetLabelEditorDataQuery) (_ EditorData, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", query.ID))

	var label Label
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		label, err = store.FindLabel(ctx, query.ID)
		return err
	}); err != nil {
		if errors.Is(err, ErrLabelNotFound) {
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

	logos := s.getLabelImageAssets(ctx, label.ID)
	view, err := s.labelWithReadyOg(ctx, label, document, logos)
	if err != nil {
		return EditorData{}, err
	}
	return EditorData{Label: view, AllowedActions: actions}, nil
}

// ListLabelsQuery pages through Labels. A zero Limit is the default page.
type ListLabelsQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// ListPage is the page of Labels a Store lists: only published Labels when
// PublishedOnly is set, filtered, sorted and paged.
type ListPage struct {
	PublishedOnly bool
	Filters       []*commonv1.FilterSpec
	Sorts         []*commonv1.SortSpec
	Limit         int32
	Offset        int32
}

func (q ListLabelsQuery) page(publishedOnly bool) ListPage {
	limit := int32(20)
	if q.Limit > 0 {
		limit = q.Limit
	}
	return ListPage{PublishedOnly: publishedOnly, Filters: q.Filters, Sorts: q.Sorts, Limit: limit, Offset: q.Offset}
}

// Pagination is where a page sits among every matching Label.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// LabelPage is one page of Labels.
type LabelPage struct {
	Labels     []View
	Pagination Pagination
}

// ListLabels returns one page of published Labels. This selector surface is
// published-only: draft access belongs to exact resource reads with an
// authorization check or to the administrator list.
func (s *Service) ListLabels(ctx context.Context, query ListLabelsQuery) (LabelPage, error) {
	labels, documents, pagination, err := s.listLabelRows(ctx, query.page(true))
	if err != nil {
		return LabelPage{}, err
	}
	projected, err := s.projectLabels(ctx, labels, documents)
	if err != nil {
		return LabelPage{}, err
	}
	return LabelPage{Labels: projected, Pagination: pagination}, nil
}

// AdminView is a Label with its relation counts.
type AdminView struct {
	Label        View
	ArtistCount  int32
	ReleaseCount int32
}

// AdminLabelPage is one page of Labels with their relation counts.
type AdminLabelPage struct {
	Labels     []AdminView
	Pagination Pagination
}

// ListLabelsAdmin returns one page of every Label to a caller with the
// platform-wide list action.
func (s *Service) ListLabelsAdmin(ctx context.Context, query ListLabelsQuery) (AdminLabelPage, error) {
	if err := labelEntity.RequireList(ctx, s.spiceDB); err != nil {
		return AdminLabelPage{}, err
	}
	labels, documents, pagination, err := s.listLabelRows(ctx, query.page(false))
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
	var artistCounts, releaseCounts map[string]int32
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		artistCounts, err = store.CountArtistsByLabel(ctx, labelIDs)
		return err
	}); err != nil {
		return AdminLabelPage{}, fmt.Errorf("failed to fetch artist counts: %w", err)
	}
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		releaseCounts, err = store.CountReleasesByLabel(ctx, labelIDs)
		return err
	}); err != nil {
		return AdminLabelPage{}, fmt.Errorf("failed to fetch release counts: %w", err)
	}
	withStats := make([]AdminView, len(labels))
	for i, label := range labels {
		withStats[i] = AdminView{
			Label:        projected[i],
			ArtistCount:  artistCounts[label.ID],
			ReleaseCount: releaseCounts[label.ID],
		}
	}
	return AdminLabelPage{Labels: withStats, Pagination: pagination}, nil
}

// listLabelRows lists one page and overlays each Label's source-locale
// document.
func (s *Service) listLabelRows(ctx context.Context, page ListPage) ([]Label, []labelDocument, Pagination, error) {
	var labels []Label
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		labels, total, err = store.ListLabels(ctx, page)
		return err
	}); err != nil {
		return nil, nil, Pagination{}, err
	}
	documents, err := s.overlayLabelSourceLocaleDocuments(ctx, labels)
	if err != nil {
		return nil, nil, Pagination{}, err
	}
	return labels, documents, Pagination{
		Total: int32(total), Limit: page.Limit, Offset: page.Offset, HasMore: int64(page.Offset)+int64(page.Limit) < total,
	}, nil
}

// projectLabels renders labels with their ready logos and OG images.
func (s *Service) projectLabels(ctx context.Context, labels []Label, documents []labelDocument) ([]View, error) {
	readyOgAssets, err := s.loadReadyLabelOgAssets(ctx, labels)
	if err != nil {
		return nil, err
	}
	logoAssets, err := s.loadReadyLabelLogoAssets(ctx, labels)
	if err != nil {
		return nil, err
	}
	projected := make([]View, len(labels))
	for i, label := range labels {
		logos := labelImageAssetsFromReadyMap(&label, logoAssets)
		projected[i] = labelView(label, documents[i], logos, manageOgAssetFromReadyMap(readyOgAssets, label.OgAssetID))
	}
	return projected, nil
}
