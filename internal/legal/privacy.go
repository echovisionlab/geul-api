package legal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PrivacyService implements the PrivacyService Connect handler
type PrivacyService struct {
	managev1connect.UnimplementedPrivacyServiceHandler
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	baseURL       string
	notice        NoticeDelivery
	auditWriter   domainaudit.Appender
	contentBlocks *contentblock.Store
	legalOG       OG
}

type PrivacyServiceOption func(*PrivacyService)

func WithPrivacyContentBlockStore(store *contentblock.Store) PrivacyServiceOption {
	return func(service *PrivacyService) { service.contentBlocks = store }
}

// NewAuditedPrivacyService makes every authoritative Privacy mutation append
// its typed Domain Audit in the same transaction.
func NewAuditedPrivacyService(
	db *gorm.DB,
	baseURL string,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	dependencies Dependencies,
	options ...PrivacyServiceOption,
) *PrivacyService {
	if auditWriter == nil {
		panic("privacy audit writer is required")
	}
	service := NewPrivacyService(
		db, baseURL, spiceDB, dependencies, options...,
	)
	service.auditWriter = auditWriter
	return service
}

// NewPrivacyService creates a new PrivacyService
func NewPrivacyService(
	db *gorm.DB,
	baseURL string,
	spiceDB *auth.SpiceDBClient,
	dependencies Dependencies,
	options ...PrivacyServiceOption,
) *PrivacyService {
	dependencycheck.MustNotNil(db, "db")
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	dependencycheck.MustNotNil(dependencies.OG, "legal OG")
	dependencycheck.MustNotNil(dependencies.Notice, "legal notice delivery")
	service := &PrivacyService{
		db:      db,
		spiceDB: spiceDB,
		baseURL: baseURL,
		notice:  dependencies.Notice,
		legalOG: dependencies.OG,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// =============================================================================
// Public Endpoints
// =============================================================================

// GetActivePrivacy returns the currently active privacy policy
// GetScheduledPrivacy returns the next scheduled privacy policy
// =============================================================================
// Admin Endpoints
// =============================================================================

// GetPrivacyVersion returns one history row with its typed Block document.
func (s *PrivacyService) GetPrivacyVersion(
	ctx context.Context,
	req *connect.Request[managev1.GetPrivacyVersionRequest],
) (*connect.Response[managev1.Privacy], error) {
	var response *connect.Response[managev1.Privacy]
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var privacy model.Privacy
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&privacy, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("privacy policy", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if err := requireLegalVersionViewOrNotFound(ctx, tx, s.spiceDB, "privacy", privacy.ID, privacy.Status); err != nil {
			return err
		}
		scoped := *s
		scoped.db = tx
		var err error
		response, err = scoped.privacyResponse(ctx, &privacy)
		return err
	})
	return response, err
}

// ListPrivacyVersions returns all privacy policy versions
func (s *PrivacyService) ListPrivacyVersions(
	ctx context.Context,
	req *connect.Request[managev1.ListPrivacyVersionsRequest],
) (*connect.Response[managev1.ListPrivacyVersionsResponse], error) {
	can, err := policyv1.PrivacyHistory.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	var versions []model.Privacy
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Privacy{})

	// Apply status filter
	if req.Msg.Status != nil && *req.Msg.Status != managev1.PrivacyStatus_PRIVACY_STATUS_UNSPECIFIED {
		query = query.Where("status = ?", req.Msg.Status.String())
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	if err := query.
		Order("version DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&versions).Error; err != nil {
		return nil, errs.Internal(err)
	}

	protoVersions := make([]*managev1.Privacy, len(versions))
	for i, v := range versions {
		proto, err := s.toProtoPrivacyWithDocument(ctx, &v)
		if err != nil {
			return nil, err
		}
		protoVersions[i] = proto
	}

	return connect.NewResponse(&managev1.ListPrivacyVersionsResponse{
		Versions: protoVersions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreatePrivacyVersion creates a new privacy policy version
func (s *PrivacyService) CreatePrivacyVersion(
	ctx context.Context,
	req *connect.Request[managev1.CreatePrivacyVersionRequest],
) (*connect.Response[managev1.Privacy], error) {
	privacyCan, err := policyv1.PrivacyHistory.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	// Use transaction with advisory lock to prevent race condition on version assignment.
	var privacy model.Privacy
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		// Advisory lock keyed on table name hash to serialize version assignment
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext('privacy_history_version'))").Error; err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, privacyCan); err != nil {
			return err
		}
		if s.contentBlocks == nil {
			return errs.InternalMsg("privacy content Block store is not configured")
		}
		if req.Msg.Document == nil {
			return errs.Required("document")
		}
		title := "Privacy Policy"
		if req.Msg.Title != nil && *req.Msg.Title != "" {
			title = *req.Msg.Title
		}
		now := time.Now().UTC()
		sourceLocale := resolveInitialSourceLocale(ctx, tx, req.Header().Get("Accept-Language"))
		if req.Msg.Document.GetSourceLocale() != sourceLocale {
			return errs.InvalidArgument("document.source_locale", "must match the server-selected source locale")
		}
		created, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
			Profile: legalContentDocumentProfile, SourceLocale: sourceLocale,
		})
		if err != nil {
			return normalizeLegalContentBlockError("privacy", err)
		}
		contentDocumentID := created.Document.ID.String()
		if err := tx.Raw(`
			INSERT INTO privacy_history (title, content, status, version, created_at, updated_at, content_document_id, source_locale)
			VALUES (?, '', ?, (SELECT COALESCE(MAX(version), 0) + 1 FROM privacy_history), ?, ?, ?, ?)
			RETURNING id, version, title, content, status, created_at, updated_at, content_document_id
		`, title, managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String(), now, now, contentDocumentID, sourceLocale).Scan(&privacy).Error; err != nil {
			return err
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(
			created.Document.ID, created.Document.Revision, req.Msg.Document,
		)
		if err != nil {
			return normalizeLegalContentBlockError("privacy", err)
		}
		_, err = s.contentBlocks.ReplaceSnapshot(
			ctx, tx, replacement, legalDocumentOwnershipFence("privacy", privacy.ID),
		)
		if err != nil {
			return normalizeLegalContentBlockError("privacy", err)
		}
		finalSnapshot, err := s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, created.Document.ID, sourceLocale,
		)
		if err != nil {
			return normalizeLegalContentBlockError("privacy", err)
		}
		if err := (internalLegalDocumentService{db: tx, kind: "privacy", contentBlocks: s.contentBlocks, legalOG: s.legalOG}).
			refreshDerivedContentProjectionsWithDB(ctx, tx, privacy.ID, finalSnapshot, sourceLocale, now); err != nil {
			return err
		}
		if err := appendLegalPolicyIdentityAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLegalPolicyCreated, "privacy", privacy.ID, privacy.Version); err != nil {
			return err
		}
		touchPolicy, err := policyv1.PrivacyHistory.TouchPolicy(privacy.ID)
		if err != nil {
			return err
		}
		deletePolicy, err := policyv1.PrivacyHistory.DeletePolicy(privacy.ID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{touchPolicy},
			[]policyv1.RelationshipMutation{deletePolicy},
		)
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return s.privacyResponse(ctx, &privacy)
}

// SchedulePrivacy schedules a privacy policy for a future date
func (s *PrivacyService) SchedulePrivacy(
	ctx context.Context,
	req *connect.Request[managev1.SchedulePrivacyRequest],
) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error) {
	if req.Msg.EffectiveFrom == nil {
		return nil, errs.Required("effective_from")
	}
	effectiveFrom := req.Msg.EffectiveFrom.AsTime()
	now := time.Now().UTC()
	if effectiveFrom.Before(now) {
		return nil, errs.InvalidArgument("effective_from", "must be in the future")
	}

	var privacy model.Privacy
	var run *model.CampaignDeliveryRun
	previewURL, err := newAutomaticLegalNoticePreviewURL(s.baseURL)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.legalOG.LockActivation(ctx, tx, "privacy"); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&privacy, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("privacy policy", req.Msg.Id)
			}
			return err
		}
		policy, err := legalDocumentPolicyForType("privacy")
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "publish", false); err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, s.spiceDB, "privacy", privacy.ID,
			legalMutationAction(policy, privacy.Status, legalActionPublish),
		); err != nil {
			return err
		}
		if err := verifyLegalExpectedRevisionWithDB(
			ctx, tx, s.contentBlocks, "privacy", privacy.ID, req.Msg.ExpectedRevision,
		); err != nil {
			return err
		}
		if privacy.Status != managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String() {
			return errs.FailedPrecondition("can only schedule draft privacy policies")
		}
		if err := EnsureLegalNoticeScheduleSlotWithDB(
			ctx,
			tx,
			EmailDeliveryReferenceTypePrivacy,
			privacy.ID,
		); err != nil {
			return err
		}
		mutationNow := time.Now()
		if err := tx.Model(&privacy).Updates(structured.Fields{
			"status":         managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			"effective_from": effectiveFrom, "updated_at": mutationNow,
		}).Error; err != nil {
			return err
		}
		privacy.Status = managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String()
		privacy.EffectiveFrom = &effectiveFrom
		privacy.UpdatedAt = mutationNow
		if err := s.legalOG.RequestSaved(
			ctx, tx, "privacy", req.Msg.Id, "", true, "privacy_scheduled",
		); err != nil {
			return err
		}
		run, err = s.notice.CreateRun(
			ctx,
			tx,
			EmailDeliveryReferenceTypePrivacy,
			privacy.ID,
			email.EventPrivacyUpdate.String(),
			map[string]string{
				"policy_title":   privacy.Title,
				"effective_date": effectiveFrom.Format("2006-01-02"),
				"preview_url":    previewURL,
			},
			legalNoticeDispatchAt(now, effectiveFrom),
		)
		if err != nil {
			return err
		}
		return appendLegalPolicyLifecycleAudit(
			ctx, tx, s.auditWriter, "privacy", privacy.ID, privacy.Version,
			[]string{"effective_at", "status"}, sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStateScheduled,
			&effectiveFrom, nil,
		)
	}); err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	dispatchCreatedLegalNoticeRun(ctx, s.notice, run)

	return connect.NewResponse(privacyLifecycleMutationResponse(&privacy, req.Msg.ExpectedRevision, true)), nil
}

// CancelPrivacySchedule cancels a scheduled privacy policy
func (s *PrivacyService) CancelPrivacySchedule(
	ctx context.Context,
	req *connect.Request[managev1.CancelPrivacyScheduleRequest],
) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error) {
	var privacy model.Privacy
	var documentRevision string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.legalOG.LockActivation(ctx, tx, "privacy"); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&privacy, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("privacy policy", req.Msg.Id)
			}
			return err
		}
		policy, err := legalDocumentPolicyForType("privacy")
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "publish", false); err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, s.spiceDB, "privacy", privacy.ID,
			legalMutationAction(policy, privacy.Status, legalActionPublish),
		); err != nil {
			return err
		}
		if privacy.Status != managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String() {
			return errs.FailedPrecondition("can only cancel scheduled privacy policies")
		}
		if privacy.ContentDocumentID == nil {
			return errs.FailedPrecondition("privacy content document has not been populated")
		}
		documentID, err := parseLegalContentUUID("content_document_id", *privacy.ContentDocumentID)
		if err != nil {
			return err
		}
		revision, err := loadLegalDocumentRevisionWithDB(ctx, tx, documentID)
		if err != nil {
			return normalizeLegalContentBlockError("privacy", err)
		}
		documentRevision = revision.String()
		if err := cancelActiveLegalNoticeDeliveryRuns(
			ctx,
			tx,
			EmailDeliveryReferenceTypePrivacy,
			privacy.ID,
		); err != nil {
			return err
		}
		if err := deleteAutomaticLegalNoticePreviewShareLinks(
			ctx,
			tx,
			EmailDeliveryReferenceTypePrivacy,
			privacy.ID,
		); err != nil {
			return err
		}
		currentBefore, err := s.legalOG.CurrentForRoute(ctx, tx, "privacy")
		if err != nil {
			return err
		}
		var cancelledEffectiveAt *time.Time
		if privacy.EffectiveFrom != nil {
			value := privacy.EffectiveFrom.UTC()
			cancelledEffectiveAt = &value
		}
		mutationNow := time.Now()
		if err := tx.Model(&privacy).Updates(structured.Fields{
			"status":         managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String(),
			"effective_from": nil,
			"updated_at":     mutationNow,
		}).Error; err != nil {
			return err
		}
		privacy.Status = managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String()
		privacy.EffectiveFrom = nil
		privacy.UpdatedAt = mutationNow
		if err := appendLegalPolicyLifecycleAudit(
			ctx, tx, s.auditWriter, "privacy", privacy.ID, privacy.Version,
			[]string{"effective_at", "status"}, sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateDraft,
			cancelledEffectiveAt, nil,
		); err != nil {
			return err
		}
		if currentBefore == nil || currentBefore.ID != privacy.ID {
			return nil
		}
		current, currentErr := s.legalOG.CurrentForRoute(ctx, tx, "privacy")
		if currentErr != nil {
			return currentErr
		}
		if current != nil {
			requestErr := s.legalOG.RequestSaved(
				ctx,
				tx,
				"privacy",
				current.ID,
				"",
				true,
				"privacy_schedule_cancelled",
			)
			return requestErr
		}
		return s.legalOG.CancelAndRelease(
			ctx, tx, "privacy", s.legalOG.RouteID("privacy"),
		)
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(privacyLifecycleMutationResponse(&privacy, documentRevision, true)), nil
}

// ActivatePrivacyNow activates a privacy policy immediately
func (s *PrivacyService) ActivatePrivacyNow(
	ctx context.Context,
	req *connect.Request[managev1.ActivatePrivacyNowRequest],
) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error) {
	now := time.Now()
	var privacy model.Privacy
	var effectiveRun *model.CampaignDeliveryRun
	privacyURL := fmt.Sprintf("%s/privacy", s.baseURL)

	// Policy state, the required sealed effective notice, and canonical OG
	// planning commit together. Provider dispatch remains post-commit.
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.legalOG.LockActivation(ctx, tx, "privacy"); err != nil {
			return err
		}
		root, err := loadLegalContentDocumentRoot(ctx, tx, "privacy", req.Msg.Id, true)
		if err != nil {
			return err
		}
		policy, err := legalDocumentPolicyForType("privacy")
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "publish", false); err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, s.spiceDB, "privacy", root.ID,
			legalMutationAction(policy, root.Status, legalActionPublish),
		); err != nil {
			return err
		}
		if err := verifyLegalExpectedRevisionWithDB(
			ctx, tx, s.contentBlocks, "privacy", req.Msg.Id, req.Msg.ExpectedRevision,
		); err != nil {
			return err
		}
		activated, run, err := ActivateAuditedLegalNoticeDocumentWithEffectiveRunWithDB(
			ctx,
			tx,
			s.auditWriter,
			s.legalOG,
			s.notice,
			EmailDeliveryReferenceTypePrivacy,
			req.Msg.Id,
			LegalNoticeActivationImmediate,
			email.EventPrivacyEffective.String(),
			map[string]string{"privacy_url": privacyURL},
			now,
		)
		if err != nil {
			return err
		}
		if !activated {
			return gorm.ErrRecordNotFound
		}
		if err := s.legalOG.RequestSaved(
			ctx, tx, "privacy", req.Msg.Id, "", true, "privacy_activated",
		); err != nil {
			return err
		}
		if err := tx.First(&privacy, "id = ?", req.Msg.Id).Error; err != nil {
			return err
		}
		effectiveRun = run
		return nil
	})

	if err != nil {
		// Check if error is already a Connect error (for NotFound or FailedPrecondition)
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}

	DispatchCommittedLegalEffectiveNoticeAfterActivation(
		ctx,
		s.db,
		s.notice,
		EmailDeliveryReferenceTypePrivacy,
		privacy.ID,
		now,
		effectiveRun,
	)

	return connect.NewResponse(privacyLifecycleMutationResponse(&privacy, req.Msg.ExpectedRevision, true)), nil
}

// DeletePrivacy deletes a privacy policy
func (s *PrivacyService) DeletePrivacy(
	ctx context.Context,
	req *connect.Request[managev1.DeletePrivacyRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	var privacy model.Privacy
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&privacy, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("privacy policy", req.Msg.Id)
			}
			return err
		}
		policy, err := legalDocumentPolicyForType("privacy")
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "delete", false); err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, s.spiceDB, "privacy", privacy.ID,
			legalMutationAction(policy, privacy.Status, legalActionDelete),
		); err != nil {
			return err
		}
		if err := verifyLegalExpectedRevisionWithDB(
			ctx, tx, s.contentBlocks, "privacy", privacy.ID, req.Msg.ExpectedRevision,
		); err != nil {
			return err
		}
		if privacy.Status != managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String() {
			return errs.FailedPrecondition("can only delete draft privacy policies")
		}
		var runCount int64
		if err := tx.Model(&model.CampaignDeliveryRun{}).
			Where("privacy_id = ?", privacy.ID).
			Count(&runCount).Error; err != nil {
			return err
		}
		if runCount > 0 {
			return errs.FailedPrecondition(
				"privacy delivery history must be preserved",
			)
		}
		if privacy.ContentDocumentID == nil {
			return errs.FailedPrecondition("privacy content document has not been populated")
		}
		documentID, err := parseLegalContentUUID("content_document_id", *privacy.ContentDocumentID)
		if err != nil {
			return err
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx, tx, documentID, legalDocumentOwnershipFence("privacy", privacy.ID),
		); err != nil {
			return normalizeLegalContentBlockError("privacy", err)
		}
		if err := tx.
			Where("entity_type = ? AND entity_id = ?", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(), privacy.ID).
			Delete(&model.ShareLink{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&privacy).Error; err != nil {
			if dberrors.IsForeignKeyViolation(err) {
				return errs.FailedPrecondition(
					"privacy delivery history or another durable reference exists",
				)
			}
			return err
		}
		if err := appendLegalPolicyIdentityAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLegalPolicyDeleted, "privacy", privacy.ID, privacy.Version); err != nil {
			return err
		}
		deletePolicy, err := policyv1.PrivacyHistory.DeletePolicy(privacy.ID)
		if err != nil {
			return err
		}
		touchPolicy, err := policyv1.PrivacyHistory.TouchPolicy(privacy.ID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{deletePolicy},
			[]policyv1.RelationshipMutation{touchPolicy},
		)
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	if err := s.legalOG.ReleaseAssets(ctx, s.db, "privacy", privacy.ID); err != nil {
		slog.Warn("failed to release legal OG assets", "entity_type", "privacy", "entity_id", privacy.ID, "error", err)
	}

	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}

// RegeneratePrivacyDerivedContent rebuilds HTML/text from the authoritative typed Block document.
func (s *PrivacyService) RegeneratePrivacyDerivedContent(
	ctx context.Context,
	req *connect.Request[managev1.RegeneratePrivacyDerivedContentRequest],
) (*connect.Response[managev1.RegeneratePrivacyDerivedContentResponse], error) {
	snapshotDigest, err := regenerateLegalDerivedContent(
		ctx, s.db, s.contentBlocks, s.spiceDB, s.legalOG,
		"privacy", req.Msg.Id, req.Msg.ExpectedSnapshotDigest,
	)
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.RegeneratePrivacyDerivedContentResponse{
		Regenerated: true, SnapshotDigest: snapshotDigest,
	}), nil
}

// =============================================================================
// Helper Methods
// =============================================================================

func privacyLifecycleMutationResponse(
	privacy *model.Privacy,
	documentRevision string,
	changed bool,
) *managev1.PrivacyLifecycleMutationResponse {
	response := &managev1.PrivacyLifecycleMutationResponse{
		Id:               privacy.ID,
		Changed:          changed,
		DocumentRevision: documentRevision,
		Status:           managev1.PrivacyStatus(managev1.PrivacyStatus_value[privacy.Status]),
		UpdatedAt:        timestamppb.New(privacy.UpdatedAt),
	}
	if privacy.EffectiveFrom != nil {
		response.EffectiveFrom = timestamppb.New(*privacy.EffectiveFrom)
	}
	if privacy.EffectiveUntil != nil {
		response.EffectiveUntil = timestamppb.New(*privacy.EffectiveUntil)
	}
	return response
}

func (s *PrivacyService) privacyResponse(
	ctx context.Context,
	privacy *model.Privacy,
) (*connect.Response[managev1.Privacy], error) {
	proto, err := s.toProtoPrivacyWithDocument(ctx, privacy)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func (s *PrivacyService) toProtoPrivacyWithDocument(
	ctx context.Context,
	p *model.Privacy,
) (*managev1.Privacy, error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("privacy content Block store is not configured")
	}
	var persisted model.Privacy
	var sourceLocale string
	var snapshot contentblock.Snapshot
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			First(&persisted, "id = ?", p.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("privacy", p.ID)
			}
			return err
		}
		if persisted.ContentDocumentID == nil {
			return errs.FailedPrecondition("privacy content document has not been populated")
		}
		documentID, err := parseLegalContentUUID("content_document_id", *persisted.ContentDocumentID)
		if err != nil {
			return err
		}
		var root legalContentDocumentRoot
		root, err = loadLegalContentDocumentRoot(ctx, tx, "privacy", p.ID, false)
		if err != nil {
			return err
		}
		sourceLocale = root.SourceLocale
		snapshot, err = s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, sourceLocale)
		return err
	})
	if err != nil {
		return nil, normalizeLegalContentBlockError("privacy", err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return nil, normalizeLegalContentBlockError("privacy", err)
	}
	p = &persisted
	privacy := &managev1.Privacy{
		Id: p.ID, Version: int32(p.Version), Title: p.Title,
		Document: document, Revision: snapshot.Document.Revision.String(),
		SourceLocale: sourceLocale, SnapshotDigest: snapshot.SnapshotDigest,
		Status:    managev1.PrivacyStatus(managev1.PrivacyStatus_value[p.Status]),
		CreatedAt: timestamppb.New(p.CreatedAt), UpdatedAt: timestamppb.New(p.UpdatedAt),
	}

	if p.EffectiveFrom != nil {
		privacy.EffectiveFrom = timestamppb.New(*p.EffectiveFrom)
	}
	if p.EffectiveUntil != nil {
		privacy.EffectiveUntil = timestamppb.New(*p.EffectiveUntil)
	}

	return privacy, nil
}
