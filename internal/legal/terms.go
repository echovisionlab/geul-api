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

// TermsService implements the TermsService Connect handler
type TermsService struct {
	managev1connect.UnimplementedTermsServiceHandler
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	baseURL       string
	notice        NoticeDelivery
	auditWriter   domainaudit.Appender
	contentBlocks *contentblock.Store
	legalOG       OG
}

type TermsServiceOption func(*TermsService)

func WithTermsContentBlockStore(store *contentblock.Store) TermsServiceOption {
	return func(service *TermsService) { service.contentBlocks = store }
}

// NewAuditedTermsService makes every authoritative Terms mutation append its
// typed Domain Audit in the same transaction.
func NewAuditedTermsService(
	db *gorm.DB,
	baseURL string,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	dependencies Dependencies,
	options ...TermsServiceOption,
) *TermsService {
	if auditWriter == nil {
		panic("terms audit writer is required")
	}
	service := NewTermsService(
		db, baseURL, spiceDB, dependencies, options...,
	)
	service.auditWriter = auditWriter
	return service
}

// NewTermsService creates a new TermsService
func NewTermsService(
	db *gorm.DB,
	baseURL string,
	spiceDB *auth.SpiceDBClient,
	dependencies Dependencies,
	options ...TermsServiceOption,
) *TermsService {
	dependencycheck.MustNotNil(db, "db")
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	dependencycheck.MustNotNil(dependencies.OG, "legal OG")
	dependencycheck.MustNotNil(dependencies.Notice, "legal notice delivery")
	service := &TermsService{
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

// GetActiveTerms returns the currently TERMS_STATUS_ACTIVE terms
// GetScheduledTerms returns the next TERMS_STATUS_SCHEDULED terms
// =============================================================================
// Admin Endpoints
// =============================================================================

// GetTermsVersion returns one history row with its typed Block document.
func (s *TermsService) GetTermsVersion(
	ctx context.Context,
	req *connect.Request[managev1.GetTermsVersionRequest],
) (*connect.Response[managev1.Terms], error) {
	var response *connect.Response[managev1.Terms]
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var terms model.Terms
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&terms, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("terms", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if err := requireLegalVersionViewOrNotFound(ctx, tx, s.spiceDB, "terms", terms.ID, terms.Status); err != nil {
			return err
		}
		scoped := *s
		scoped.db = tx
		var err error
		response, err = scoped.termsResponse(ctx, &terms)
		return err
	})
	return response, err
}

// ListTermsVersions returns all terms versions
func (s *TermsService) ListTermsVersions(
	ctx context.Context,
	req *connect.Request[managev1.ListTermsVersionsRequest],
) (*connect.Response[managev1.ListTermsVersionsResponse], error) {
	can, err := policyv1.TermsHistory.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	var versions []model.Terms
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Terms{})

	if req.Msg.Status != nil && *req.Msg.Status != managev1.TermsStatus_TERMS_STATUS_UNSPECIFIED {
		query = query.Where("status = ?", req.Msg.Status.String())
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

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

	protoVersions := make([]*managev1.Terms, len(versions))
	for i, v := range versions {
		proto, err := s.toProtoTermsWithDocument(ctx, &v)
		if err != nil {
			return nil, err
		}
		protoVersions[i] = proto
	}

	return connect.NewResponse(&managev1.ListTermsVersionsResponse{
		Versions: protoVersions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreateTermsVersion creates a new terms version
func (s *TermsService) CreateTermsVersion(
	ctx context.Context,
	req *connect.Request[managev1.CreateTermsVersionRequest],
) (*connect.Response[managev1.Terms], error) {
	termsCan, err := policyv1.TermsHistory.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	// Use transaction with advisory lock to prevent race condition on version assignment.
	var terms model.Terms
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		// Advisory lock keyed on table name hash to serialize version assignment
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext('terms_history_version'))").Error; err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, termsCan); err != nil {
			return err
		}
		if s.contentBlocks == nil {
			return errs.InternalMsg("terms content Block store is not configured")
		}
		if req.Msg.Document == nil {
			return errs.Required("document")
		}
		title := "Terms of Service"
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
			return normalizeLegalContentBlockError("terms", err)
		}
		contentDocumentID := created.Document.ID.String()
		if err := tx.Raw(`
			INSERT INTO terms_history (title, content, status, version, created_at, updated_at, content_document_id, source_locale)
			VALUES (?, '', ?, (SELECT COALESCE(MAX(version), 0) + 1 FROM terms_history), ?, ?, ?, ?)
			RETURNING id, version, title, content, status, created_at, updated_at, content_document_id
		`, title, managev1.TermsStatus_TERMS_STATUS_DRAFT.String(), now, now, contentDocumentID, sourceLocale).Scan(&terms).Error; err != nil {
			return err
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(
			created.Document.ID, created.Document.Revision, req.Msg.Document,
		)
		if err != nil {
			return normalizeLegalContentBlockError("terms", err)
		}
		_, err = s.contentBlocks.ReplaceSnapshot(
			ctx, tx, replacement, legalDocumentOwnershipFence("terms", terms.ID),
		)
		if err != nil {
			return normalizeLegalContentBlockError("terms", err)
		}
		finalSnapshot, err := s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, created.Document.ID, sourceLocale,
		)
		if err != nil {
			return normalizeLegalContentBlockError("terms", err)
		}
		if err := (internalLegalDocumentService{db: tx, kind: "terms", contentBlocks: s.contentBlocks, legalOG: s.legalOG}).
			refreshDerivedContentProjectionsWithDB(ctx, tx, terms.ID, finalSnapshot, sourceLocale, now); err != nil {
			return err
		}
		if err := appendLegalPolicyIdentityAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLegalPolicyCreated, "terms", terms.ID, terms.Version); err != nil {
			return err
		}
		touchPolicy, err := policyv1.TermsHistory.TouchPolicy(terms.ID)
		if err != nil {
			return err
		}
		deletePolicy, err := policyv1.TermsHistory.DeletePolicy(terms.ID)
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
	return s.termsResponse(ctx, &terms)
}

// ScheduleTerms schedules terms for a future date
func (s *TermsService) ScheduleTerms(
	ctx context.Context,
	req *connect.Request[managev1.ScheduleTermsRequest],
) (*connect.Response[managev1.TermsLifecycleMutationResponse], error) {
	// Validate effective_from is provided
	if req.Msg.EffectiveFrom == nil {
		return nil, errs.Required("effective_from")
	}

	effectiveFrom := req.Msg.EffectiveFrom.AsTime()
	now := time.Now().UTC()
	if effectiveFrom.Before(now) {
		return nil, errs.InvalidArgument("effective_from", "must be in the future")
	}

	var terms model.Terms
	var run *model.CampaignDeliveryRun
	previewURL, err := newAutomaticLegalNoticePreviewURL(s.baseURL)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.legalOG.LockActivation(ctx, tx, "terms"); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&terms, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("terms", req.Msg.Id)
			}
			return err
		}
		policy, err := legalDocumentPolicyForType("terms")
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "publish", false); err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, s.spiceDB, "terms", terms.ID,
			legalMutationAction(policy, terms.Status, legalActionPublish),
		); err != nil {
			return err
		}
		if err := verifyLegalExpectedRevisionWithDB(
			ctx, tx, s.contentBlocks, "terms", terms.ID, req.Msg.ExpectedRevision,
		); err != nil {
			return err
		}
		if terms.Status != managev1.TermsStatus_TERMS_STATUS_DRAFT.String() {
			return errs.FailedPrecondition(errs.MsgOnlyScheduleDraft)
		}
		if err := EnsureLegalNoticeScheduleSlotWithDB(
			ctx,
			tx,
			EmailDeliveryReferenceTypeTerms,
			terms.ID,
		); err != nil {
			return err
		}
		mutationNow := time.Now()
		if err := tx.Model(&terms).Updates(structured.Fields{
			"status":         managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			"effective_from": effectiveFrom, "updated_at": mutationNow,
		}).Error; err != nil {
			return err
		}
		terms.Status = managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String()
		terms.EffectiveFrom = &effectiveFrom
		terms.UpdatedAt = mutationNow
		if err := s.legalOG.RequestSaved(
			ctx, tx, "terms", req.Msg.Id, "", true, "terms_scheduled",
		); err != nil {
			return err
		}
		run, err = s.notice.CreateRun(
			ctx,
			tx,
			EmailDeliveryReferenceTypeTerms,
			terms.ID,
			email.EventTermsUpdate.String(),
			map[string]string{
				"policy_title":   terms.Title,
				"effective_date": effectiveFrom.Format("2006-01-02"),
				"preview_url":    previewURL,
			},
			legalNoticeDispatchAt(now, effectiveFrom),
		)
		if err != nil {
			return err
		}
		return appendLegalPolicyLifecycleAudit(
			ctx, tx, s.auditWriter, "terms", terms.ID, terms.Version,
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

	return connect.NewResponse(termsLifecycleMutationResponse(&terms, req.Msg.ExpectedRevision, true)), nil
}

// CancelTermsSchedule cancels a TERMS_STATUS_SCHEDULED terms
func (s *TermsService) CancelTermsSchedule(
	ctx context.Context,
	req *connect.Request[managev1.CancelTermsScheduleRequest],
) (*connect.Response[managev1.TermsLifecycleMutationResponse], error) {
	var terms model.Terms
	var documentRevision string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.legalOG.LockActivation(ctx, tx, "terms"); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&terms, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("terms", req.Msg.Id)
			}
			return err
		}
		policy, err := legalDocumentPolicyForType("terms")
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "publish", false); err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, s.spiceDB, "terms", terms.ID,
			legalMutationAction(policy, terms.Status, legalActionPublish),
		); err != nil {
			return err
		}
		if terms.Status != managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String() {
			return errs.FailedPrecondition(errs.MsgOnlyCancelScheduled)
		}
		if terms.ContentDocumentID == nil {
			return errs.FailedPrecondition("terms content document has not been populated")
		}
		documentID, err := parseLegalContentUUID("content_document_id", *terms.ContentDocumentID)
		if err != nil {
			return err
		}
		revision, err := loadLegalDocumentRevisionWithDB(ctx, tx, documentID)
		if err != nil {
			return normalizeLegalContentBlockError("terms", err)
		}
		documentRevision = revision.String()
		if err := cancelActiveLegalNoticeDeliveryRuns(
			ctx,
			tx,
			EmailDeliveryReferenceTypeTerms,
			terms.ID,
		); err != nil {
			return err
		}
		if err := deleteAutomaticLegalNoticePreviewShareLinks(
			ctx,
			tx,
			EmailDeliveryReferenceTypeTerms,
			terms.ID,
		); err != nil {
			return err
		}
		currentBefore, err := s.legalOG.CurrentForRoute(ctx, tx, "terms")
		if err != nil {
			return err
		}
		var cancelledEffectiveAt *time.Time
		if terms.EffectiveFrom != nil {
			value := terms.EffectiveFrom.UTC()
			cancelledEffectiveAt = &value
		}
		mutationNow := time.Now()
		if err := tx.Model(&terms).Updates(structured.Fields{
			"status":         managev1.TermsStatus_TERMS_STATUS_DRAFT.String(),
			"effective_from": nil,
			"updated_at":     mutationNow,
		}).Error; err != nil {
			return err
		}
		terms.Status = managev1.TermsStatus_TERMS_STATUS_DRAFT.String()
		terms.EffectiveFrom = nil
		terms.UpdatedAt = mutationNow
		if err := appendLegalPolicyLifecycleAudit(
			ctx, tx, s.auditWriter, "terms", terms.ID, terms.Version,
			[]string{"effective_at", "status"}, sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateDraft,
			cancelledEffectiveAt, nil,
		); err != nil {
			return err
		}
		if currentBefore == nil || currentBefore.ID != terms.ID {
			return nil
		}
		current, currentErr := s.legalOG.CurrentForRoute(ctx, tx, "terms")
		if currentErr != nil {
			return currentErr
		}
		if current != nil {
			requestErr := s.legalOG.RequestSaved(
				ctx,
				tx,
				"terms",
				current.ID,
				"",
				true,
				"terms_schedule_cancelled",
			)
			return requestErr
		}
		return s.legalOG.CancelAndRelease(
			ctx, tx, "terms", s.legalOG.RouteID("terms"),
		)
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(termsLifecycleMutationResponse(&terms, documentRevision, true)), nil
}

// ActivateTermsNow activates terms immediately
func (s *TermsService) ActivateTermsNow(
	ctx context.Context,
	req *connect.Request[managev1.ActivateTermsNowRequest],
) (*connect.Response[managev1.TermsLifecycleMutationResponse], error) {
	now := time.Now()
	var terms model.Terms
	var effectiveRun *model.CampaignDeliveryRun
	termsURL := fmt.Sprintf("%s/terms", s.baseURL)

	// Policy state, the required sealed effective notice, and canonical OG
	// planning commit together. Provider dispatch remains post-commit.
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.legalOG.LockActivation(ctx, tx, "terms"); err != nil {
			return err
		}
		root, err := loadLegalContentDocumentRoot(ctx, tx, "terms", req.Msg.Id, true)
		if err != nil {
			return err
		}
		policy, err := legalDocumentPolicyForType("terms")
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "publish", false); err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, s.spiceDB, "terms", root.ID,
			legalMutationAction(policy, root.Status, legalActionPublish),
		); err != nil {
			return err
		}
		if err := verifyLegalExpectedRevisionWithDB(
			ctx, tx, s.contentBlocks, "terms", req.Msg.Id, req.Msg.ExpectedRevision,
		); err != nil {
			return err
		}
		activated, run, err := ActivateAuditedLegalNoticeDocumentWithEffectiveRunWithDB(
			ctx,
			tx,
			s.auditWriter,
			s.legalOG,
			s.notice,
			EmailDeliveryReferenceTypeTerms,
			req.Msg.Id,
			LegalNoticeActivationImmediate,
			email.EventTermsEffective.String(),
			map[string]string{"terms_url": termsURL},
			now,
		)
		if err != nil {
			return err
		}
		if !activated {
			return gorm.ErrRecordNotFound
		}
		if err := s.legalOG.RequestSaved(
			ctx, tx, "terms", req.Msg.Id, "", true, "terms_activated",
		); err != nil {
			return err
		}
		if err := tx.First(&terms, "id = ?", req.Msg.Id).Error; err != nil {
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
		EmailDeliveryReferenceTypeTerms,
		terms.ID,
		now,
		effectiveRun,
	)

	return connect.NewResponse(termsLifecycleMutationResponse(&terms, req.Msg.ExpectedRevision, true)), nil
}

// DeleteTerms deletes terms
func (s *TermsService) DeleteTerms(
	ctx context.Context,
	req *connect.Request[managev1.DeleteTermsRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	var terms model.Terms
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&terms, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("terms", req.Msg.Id)
			}
			return err
		}
		policy, err := legalDocumentPolicyForType("terms")
		if err != nil {
			return err
		}
		if err := requireActiveLegalPrincipal(ctx, tx, "delete", false); err != nil {
			return err
		}
		if err := requireLegalPermission(
			ctx, s.spiceDB, "terms", terms.ID,
			legalMutationAction(policy, terms.Status, legalActionDelete),
		); err != nil {
			return err
		}
		if err := verifyLegalExpectedRevisionWithDB(
			ctx, tx, s.contentBlocks, "terms", terms.ID, req.Msg.ExpectedRevision,
		); err != nil {
			return err
		}
		if terms.Status != managev1.TermsStatus_TERMS_STATUS_DRAFT.String() {
			return errs.FailedPrecondition("can only delete draft terms")
		}
		var runCount int64
		if err := tx.Model(&model.CampaignDeliveryRun{}).
			Where("terms_id = ?", terms.ID).
			Count(&runCount).Error; err != nil {
			return err
		}
		if runCount > 0 {
			return errs.FailedPrecondition(
				"terms delivery history must be preserved",
			)
		}
		if terms.ContentDocumentID == nil {
			return errs.FailedPrecondition("terms content document has not been populated")
		}
		documentID, err := parseLegalContentUUID("content_document_id", *terms.ContentDocumentID)
		if err != nil {
			return err
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx, tx, documentID, legalDocumentOwnershipFence("terms", terms.ID),
		); err != nil {
			return normalizeLegalContentBlockError("terms", err)
		}
		if err := tx.
			Where("entity_type = ? AND entity_id = ?", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String(), terms.ID).
			Delete(&model.ShareLink{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&terms).Error; err != nil {
			if dberrors.IsForeignKeyViolation(err) {
				return errs.FailedPrecondition(
					"terms delivery history or another durable reference exists",
				)
			}
			return err
		}
		if err := appendLegalPolicyIdentityAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLegalPolicyDeleted, "terms", terms.ID, terms.Version); err != nil {
			return err
		}
		deletePolicy, err := policyv1.TermsHistory.DeletePolicy(terms.ID)
		if err != nil {
			return err
		}
		touchPolicy, err := policyv1.TermsHistory.TouchPolicy(terms.ID)
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
	if err := s.legalOG.ReleaseAssets(ctx, s.db, "terms", terms.ID); err != nil {
		slog.Warn("failed to release legal OG assets", "entity_type", "terms", "entity_id", terms.ID, "error", err)
	}

	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}

// RegenerateTermsDerivedContent rebuilds HTML/text from the authoritative typed Block document.
func (s *TermsService) RegenerateTermsDerivedContent(
	ctx context.Context,
	req *connect.Request[managev1.RegenerateTermsDerivedContentRequest],
) (*connect.Response[managev1.RegenerateTermsDerivedContentResponse], error) {
	snapshotDigest, err := regenerateLegalDerivedContent(
		ctx, s.db, s.contentBlocks, s.spiceDB, s.legalOG,
		"terms", req.Msg.Id, req.Msg.ExpectedSnapshotDigest,
	)
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.RegenerateTermsDerivedContentResponse{
		Regenerated: true, SnapshotDigest: snapshotDigest,
	}), nil
}

// =============================================================================
// Helper Methods
// =============================================================================

func termsLifecycleMutationResponse(
	terms *model.Terms,
	documentRevision string,
	changed bool,
) *managev1.TermsLifecycleMutationResponse {
	response := &managev1.TermsLifecycleMutationResponse{
		Id:               terms.ID,
		Changed:          changed,
		DocumentRevision: documentRevision,
		Status:           managev1.TermsStatus(managev1.TermsStatus_value[terms.Status]),
		UpdatedAt:        timestamppb.New(terms.UpdatedAt),
	}
	if terms.EffectiveFrom != nil {
		response.EffectiveFrom = timestamppb.New(*terms.EffectiveFrom)
	}
	if terms.EffectiveUntil != nil {
		response.EffectiveUntil = timestamppb.New(*terms.EffectiveUntil)
	}
	return response
}

func (s *TermsService) termsResponse(
	ctx context.Context,
	terms *model.Terms,
) (*connect.Response[managev1.Terms], error) {
	proto, err := s.toProtoTermsWithDocument(ctx, terms)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func (s *TermsService) toProtoTermsWithDocument(
	ctx context.Context,
	t *model.Terms,
) (*managev1.Terms, error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("terms content Block store is not configured")
	}
	var persisted model.Terms
	var sourceLocale string
	var snapshot contentblock.Snapshot
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			First(&persisted, "id = ?", t.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("terms", t.ID)
			}
			return err
		}
		if persisted.ContentDocumentID == nil {
			return errs.FailedPrecondition("terms content document has not been populated")
		}
		documentID, err := parseLegalContentUUID("content_document_id", *persisted.ContentDocumentID)
		if err != nil {
			return err
		}
		var root legalContentDocumentRoot
		root, err = loadLegalContentDocumentRoot(ctx, tx, "terms", t.ID, false)
		if err != nil {
			return err
		}
		sourceLocale = root.SourceLocale
		snapshot, err = s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, sourceLocale)
		return err
	})
	if err != nil {
		return nil, normalizeLegalContentBlockError("terms", err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return nil, normalizeLegalContentBlockError("terms", err)
	}
	t = &persisted
	terms := &managev1.Terms{
		Id: t.ID, Version: int32(t.Version), Title: t.Title,
		Document: document, Revision: snapshot.Document.Revision.String(),
		SourceLocale: sourceLocale, SnapshotDigest: snapshot.SnapshotDigest,
		Status:    managev1.TermsStatus(managev1.TermsStatus_value[t.Status]),
		CreatedAt: timestamppb.New(t.CreatedAt), UpdatedAt: timestamppb.New(t.UpdatedAt),
	}

	if t.EffectiveFrom != nil {
		terms.EffectiveFrom = timestamppb.New(*t.EffectiveFrom)
	}
	if t.EffectiveUntil != nil {
		terms.EffectiveUntil = timestamppb.New(*t.EffectiveUntil)
	}

	return terms, nil
}
