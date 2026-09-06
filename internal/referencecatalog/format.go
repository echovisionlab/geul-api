package referencecatalog

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// FormatService implements the FormatService Connect handler
type FormatService struct {
	managev1connect.UnimplementedFormatServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
}

func NewAuditedFormatService(db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient) *FormatService {
	if auditWriter == nil {
		panic("format audit writer is required")
	}
	service := NewFormatService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

// NewFormatService creates a new FormatService
func NewFormatService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *FormatService {
	if db == nil {
		panic("db is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	return &FormatService{db: db, spiceDB: spiceDB}
}

// formatSortConfig defines allowed sort fields for formats
var formatSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name": "name",
		"slug": "slug",
	},
	DefaultSort: "name ASC, id ASC",
}

var formatListCRUD = referenceCRUD[model.Format]{filters: formatFilterConfig, sorts: &formatSortConfig}

// GetFormat retrieves a format by ID
// GetFormatBySlug retrieves a format by slug
// ListFormats returns a paginated list of formats
func (s *FormatService) ListFormats(
	ctx context.Context,
	req *connect.Request[managev1.ListFormatsRequest],
) (*connect.Response[managev1.ListFormatsResponse], error) {
	formats, total, page, err := formatListCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	protoFormats := make([]*managev1.Format, len(formats))
	for i := range formats {
		protoFormats[i] = toProtoFormat(&formats[i])
	}

	return connect.NewResponse(&managev1.ListFormatsResponse{
		Formats:    protoFormats,
		Pagination: page.BuildResponse(total),
	}), nil
}

// ListFormatsAdmin returns a paginated list of formats with stats
func (s *FormatService) ListFormatsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListFormatsAdminRequest],
) (*connect.Response[managev1.ListFormatsAdminResponse], error) {
	can, err := policyv1.Format.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}

	formats, total, page, err := formatListCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	formatIDs := make([]string, len(formats))
	for index := range formats {
		formatIDs[index] = formats[index].ID
	}
	releaseCounts, err := loadReferenceCounts(ctx, s.db, "release_format", "format_id", formatIDs)
	if err != nil {
		return nil, err
	}
	formatsWithStats := make([]*managev1.FormatWithStats, len(formats))
	for i := range formats {
		formatsWithStats[i] = &managev1.FormatWithStats{
			Format:       toProtoFormat(&formats[i]),
			ReleaseCount: releaseCounts[formats[i].ID],
		}
	}

	return connect.NewResponse(&managev1.ListFormatsAdminResponse{
		Formats:    formatsWithStats,
		Pagination: page.BuildResponse(total),
	}), nil
}

// CreateFormat creates a new format (admin only)
func (s *FormatService) CreateFormat(
	ctx context.Context,
	req *connect.Request[managev1.CreateFormatRequest],
) (*connect.Response[managev1.Format], error) {
	can, err := policyv1.Format.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	// Validate required fields
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, errs.Required("name")
	}

	// Generate slug from name if not provided
	slug := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "/", "-"), " ", "-"))
	if req.Msg.Slug != nil && *req.Msg.Slug != "" {
		slug = *req.Msg.Slug
	}
	if err := validateSlugWithoutSlash(slug); err != nil {
		return nil, err
	}

	format := &model.Format{
		Name: name,
		Slug: slug,
	}

	// Rely on database unique constraint for slug uniqueness (prevents TOCTOU race condition)
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(format).Error; err != nil {
			if strings.Contains(err.Error(), "duplicate key") {
				return errs.SlugAlreadyExists("format", slug)
			}
			return errs.Internal(err)
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditFormatCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormatCreatedAuditRecord(metadata, format.ID)
		}); err != nil {
			return err
		}
		policyTouch, err := policyv1.Format.TouchPolicy(format.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.Format.DeletePolicy(format.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete})
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(toProtoFormat(format)), nil
}

// UpdateFormat updates an existing format (admin only)
func (s *FormatService) UpdateFormat(
	ctx context.Context,
	req *connect.Request[managev1.UpdateFormatRequest],
) (*connect.Response[managev1.Format], error) {
	can, err := policyv1.Format.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var format model.Format
	updates := structured.Fields{}

	if req.Msg.Slug != nil {
		if err := validateSlugWithoutSlash(*req.Msg.Slug); err != nil {
			return nil, err
		}
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&format, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("format", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		changedFields := make([]string, 0, 2)
		if req.Msg.Name != nil && *req.Msg.Name != format.Name {
			updates["name"] = *req.Msg.Name
			changedFields = append(changedFields, "name")
		}
		if req.Msg.Slug != nil && *req.Msg.Slug != format.Slug {
			updates["slug"] = *req.Msg.Slug
			changedFields = append(changedFields, "slug")
		}
		if len(updates) == 0 {
			return nil
		}
		if err := s.applyFormatUpdates(ctx, tx, &format, updates, req.Msg.Slug); err != nil {
			return err
		}
		if err := tx.First(&format, "id = ?", req.Msg.Id).Error; err != nil {
			return errs.Internal(err)
		}
		return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditFormatUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormatMetadataUpdatedAuditRecord(metadata, format.ID, changedFields)
		})
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(toProtoFormat(&format)), nil
}

func (s *FormatService) applyFormatUpdates(
	ctx context.Context,
	db *gorm.DB,
	format *model.Format,
	updates structured.Fields,
	slug *string,
) error {
	if len(updates) == 0 {
		return nil
	}
	if err := db.WithContext(ctx).Model(format).Updates(updates).Error; err != nil {
		if isUniqueViolation(err) {
			return errs.SlugAlreadyExists("format", optionalStringValue(slug))
		}
		return errs.Internal(err)
	}
	return nil
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// DeleteFormat deletes a format (admin only)
func (s *FormatService) DeleteFormat(
	ctx context.Context,
	req *connect.Request[managev1.DeleteFormatRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	can, err := policyv1.Format.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var format model.Format
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&format, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("format", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var count int64
		if err := tx.Table("release_format").Where("format_id = ?", format.ID).Limit(1).Count(&count).Error; err != nil {
			return errs.Internal(err)
		}
		if count != 0 {
			return errs.FailedPrecondition("format is still referenced")
		}
		if err := tx.Delete(&format).Error; err != nil {
			return errs.Internal(err)
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditFormatDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormatDeletedAuditRecord(metadata, format.ID)
		}); err != nil {
			return err
		}
		policyDelete, err := policyv1.Format.DeletePolicy(format.ID)
		if err != nil {
			return err
		}
		policyTouch, err := policyv1.Format.TouchPolicy(format.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyDelete}, []policyv1.RelationshipMutation{policyTouch})
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// Helper methods

// toProtoFormat converts a model.Format to protobuf Format
func toProtoFormat(f *model.Format) *managev1.Format {
	return &managev1.Format{
		Id:   f.ID,
		Name: f.Name,
		Slug: f.Slug,
	}
}
