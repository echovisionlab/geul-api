package emailauthoring

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// InternalEmailLayoutService provides internal-only access for email layout document operations.
type InternalEmailLayoutService struct {
	db            *gorm.DB
	auditWriter   domainaudit.Appender
	checkpoints   persistencecheckpoint.ContributorFence
	references    CampaignDeliveryReferences
	contentBlocks *contentblock.Store
}

type InternalEmailLayoutServiceOption func(*InternalEmailLayoutService)

func WithInternalEmailLayoutCampaignDeliveryReferences(references CampaignDeliveryReferences) InternalEmailLayoutServiceOption {
	return func(service *InternalEmailLayoutService) { service.references = references }
}

func WithInternalEmailLayoutContentBlockStore(store *contentblock.Store) InternalEmailLayoutServiceOption {
	return func(service *InternalEmailLayoutService) { service.contentBlocks = store }
}

func WithInternalEmailLayoutCheckpoints(checkpoints persistencecheckpoint.ContributorFence) InternalEmailLayoutServiceOption {
	return func(service *InternalEmailLayoutService) { service.checkpoints = checkpoints }
}

func NewAuditedInternalEmailLayoutService(db *gorm.DB, auditWriter domainaudit.Appender, options ...InternalEmailLayoutServiceOption) *InternalEmailLayoutService {
	if auditWriter == nil {
		panic("email layout audit writer is required")
	}
	service := &InternalEmailLayoutService{db: db, auditWriter: auditWriter}
	for _, option := range options {
		option(service)
	}
	dependencycheck.MustNotNil(service.contentBlocks, "email layout Content Document store")
	return service
}

// SaveDocument persists the exact source or existing target locale room.
func (s *InternalEmailLayoutService) SaveDocument(
	ctx context.Context,
	req *connect.Request[intrav1.SaveEmailLayoutDocumentRequest],
) (*connect.Response[intrav1.SaveEmailLayoutDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	r := req.Msg
	roomLocale, err := canonicalEmailLayoutRoomLocale(r.Locale)
	if err != nil {
		return nil, err
	}
	expectedDocumentRevision, err := parseEmailLayoutDocumentRevision(r.ExpectedDocumentRevision)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result := emailLayoutCollaborativeSaveResult{locale: roomLocale}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, r.EmailLayoutId, "UPDATE")
		if err != nil {
			return err
		}
		if authority.DocumentRevision != expectedDocumentRevision {
			return errs.CollaborationConflict(
				intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
				"Email Layout Content Document changed; reload before saving",
			)
		}
		if err := ensureEmailLayoutMutableForActiveDelivery(ctx, tx, s.references, authority.LayoutID); err != nil {
			return err
		}
		canonicalSource, err := emailutil.LoadCanonicalLayoutTranslationDocument(
			ctx, tx, r.EmailLayoutId, authority.SourceLocale,
		)
		if err != nil {
			return err
		}
		if roomLocale != authority.SourceLocale {
			return s.saveTargetDocument(ctx, tx, r, authority, canonicalSource, now, &result)
		}
		if len(r.LocaleValues) != 0 {
			return errs.InvalidArgument("locale_values", "source locale uses content_html")
		}
		if r.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source locale has no target revision")
		}
		originMemberID, err := requireEmailAuthoringMutationContributor(
			ctx, tx, r.ContributorMemberIds,
		)
		if err != nil {
			return err
		}
		if err := requireEmailLayoutCollaborationContributors(
			ctx, tx, s.checkpoints, r.EmailLayoutId, r.ContributorMemberIds,
		); err != nil {
			return err
		}

		contentHTMLValue := emailutil.NormalizeTemplatePlaceholders(strings.ReplaceAll(r.ContentHtml, "\x00", ""))
		canonicalHTMLValue, err := emailutil.CanonicalizeLayoutSourceMarkers(contentHTMLValue)
		if err != nil {
			return errs.InvalidArgument("html_content", err.Error())
		}
		contentHTMLValue = canonicalHTMLValue
		if err := emailutil.ValidateLayoutHTMLContentError(contentHTMLValue); err != nil {
			return errs.InvalidArgument("html_content", err.Error())
		}
		contentTextValue := strings.ReplaceAll(r.ContentText, "\x00", "")
		if strings.TrimSpace(contentTextValue) == "" {
			contentTextValue = emailutil.StripHTML(contentHTMLValue)
		}
		unchanged := derefString(canonicalSource.ContentHTML) == contentHTMLValue &&
			derefString(canonicalSource.ContentText) == contentTextValue
		advance, err := s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: authority.DocumentID, ExpectedRevision: expectedDocumentRevision},
			emailLayoutContentFence(s.references, r.EmailLayoutId),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				if unchanged {
					return contentblock.MetadataEffect{}, nil
				}
				if err := emailutil.SaveLayoutSourceLocaleDocument(
					ctx, tx, r.EmailLayoutId, authority.SourceLocale,
					emailutil.LayoutTranslationDocument{ContentHTML: &contentHTMLValue, ContentText: &contentTextValue},
					now,
				); err != nil {
					return contentblock.MetadataEffect{}, err
				}
				return contentblock.MetadataEffect{
					Changed: true, AffectsTranslationSource: true,
					ChangedLocales: []string{authority.SourceLocale},
				}, nil
			},
		)
		if err != nil {
			return err
		}
		result.documentRevision = advance.DocumentRevision.String()
		result.changed = advance.Changed
		if advance.Changed {
			return appendEmailLayoutLocaleContentAudit(
				ctx, tx, s.auditWriter, originMemberID, r.EmailLayoutId, roomLocale,
				emailAuthoringLocaleContentOperation(true, false, false, true),
			)
		}
		return nil
	}); err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&intrav1.SaveEmailLayoutDocumentResponse{
		Success: true, Locale: roomLocale,
		DocumentRevision: result.documentRevision, TargetRevision: result.targetRevision,
	}), nil
}

type emailLayoutCollaborativeSaveResult struct {
	locale           string
	documentRevision string
	targetRevision   *string
	changed          bool
}

func (s *InternalEmailLayoutService) saveTargetDocument(
	ctx context.Context,
	tx *gorm.DB,
	request *intrav1.SaveEmailLayoutDocumentRequest,
	authority emailLayoutDocumentAuthority,
	canonicalSource *emailutil.LayoutTranslationDocument,
	now time.Time,
	output *emailLayoutCollaborativeSaveResult,
) error {
	contributorMemberID, err := requireEmailAuthoringMutationContributor(
		ctx, tx, request.ContributorMemberIds,
	)
	if err != nil {
		return err
	}
	if err := requireEmailLayoutCollaborationContributors(
		ctx, tx, s.checkpoints, request.EmailLayoutId, request.ContributorMemberIds,
	); err != nil {
		return err
	}
	entry, err := emailutil.LoadLayoutTranslationEntry(
		ctx, tx, request.EmailLayoutId, output.locale,
	)
	if err != nil {
		return err
	}
	if entry == nil {
		return errs.FailedPrecondition("Email Layout target locale document does not exist")
	}
	currentRevision, err := deriveEmailLayoutTargetRevision(authority.DocumentRevision.String(), entry)
	if err != nil {
		return err
	}
	if err := translation.ValidateExpectedTargetRevision(
		request.ExpectedTargetRevision, derefString(currentRevision), true,
	); err != nil {
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Email Layout source or exact target changed since it was loaded; reload before saving",
		)
	}
	contentHTML, contentText, err := compileEmailLayoutTargetValues(
		derefString(canonicalSource.ContentHTML), request.LocaleValues,
	)
	if err != nil {
		return err
	}
	changed := derefString(entry.ContentHTML) != derefString(contentHTML) ||
		derefString(entry.ContentText) != derefString(contentText)
	updatedAt := entry.UpdatedAt
	if changed {
		updatedAt = translation.NextTargetUpdatedAt(now, entry.UpdatedAt)
		if err := emailutil.UpsertLayoutTranslationEntry(
			ctx, tx, request.EmailLayoutId, output.locale,
			translation.EntryWrite{ContentHTML: contentHTML, ContentText: contentText, Now: updatedAt},
		); err != nil {
			return err
		}
		if err := appendEmailLayoutLocaleContentAudit(
			ctx, tx, s.auditWriter, contributorMemberID, request.EmailLayoutId, output.locale,
			emailAuthoringLocaleContentOperation(false, false, false, true),
		); err != nil {
			return err
		}
	}
	nextEntry := &emailutil.LayoutTranslationEntry{
		LayoutTranslationDocument: emailutil.LayoutTranslationDocument{ContentHTML: contentHTML, ContentText: contentText, UpdatedAt: updatedAt},
	}
	nextRevision, err := deriveEmailLayoutTargetRevision(authority.DocumentRevision.String(), nextEntry)
	if err != nil {
		return err
	}
	output.documentRevision = authority.DocumentRevision.String()
	output.targetRevision = nextRevision
	output.changed = changed
	return nil
}

func canonicalEmailLayoutRoomLocale(value string) (string, error) {
	locale := strings.TrimSpace(value)
	if locale == "" {
		return "", errs.Required("locale")
	}
	canonical := localization.NormalizeSupportedLocale(locale)
	if value != locale || canonical == nil || *canonical != locale {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return locale, nil
}

func compileEmailLayoutTargetValues(
	canonicalSource string,
	values []*intrav1.EmailLayoutLocaleValue,
) (*string, *string, error) {
	units, err := emailutil.ExtractLayoutContentUnits(canonicalSource)
	if err != nil {
		return nil, nil, errs.FailedPrecondition("Email Layout source unit markers require backfill before collaboration editing")
	}
	allowed := make(map[string]struct{}, len(units))
	for _, unit := range units {
		allowed[unit.Handle] = struct{}{}
	}
	requested := make(map[string]string, len(values))
	for _, value := range values {
		if value == nil {
			return nil, nil, errs.InvalidArgument("locale_values", "must not contain null values")
		}
		handle := strings.TrimSpace(value.Handle)
		if _, ok := allowed[handle]; !ok {
			return nil, nil, errs.InvalidArgument("locale_values", "contains an unknown Email Layout unit handle")
		}
		if _, duplicate := requested[handle]; duplicate {
			return nil, nil, errs.InvalidArgument("locale_values", "contains a duplicate Email Layout unit handle")
		}
		requested[handle] = strings.ReplaceAll(value.Value, "\x00", "")
	}
	contentHTML, contentText, err := emailutil.ApplyLayoutLocaleValues(canonicalSource, requested)
	if err != nil {
		return nil, nil, errs.InvalidArgument("locale_values", err.Error())
	}
	return contentHTML, contentText, nil
}

// LoadDocument loads the exact locale room, falling back read-only to
// the source projection when a requested target row does not exist.
func (s *InternalEmailLayoutService) LoadDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadEmailLayoutDocumentRequest],
) (*connect.Response[intrav1.LoadEmailLayoutDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	roomLocale, err := canonicalEmailLayoutRoomLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	var response *connect.Response[intrav1.LoadEmailLayoutDocumentResponse]
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, req.Msg.EmailLayoutId, "SHARE")
		if err != nil {
			return err
		}
		state, err := emailutil.LoadCanonicalLayoutTranslationDocument(
			ctx, tx, req.Msg.EmailLayoutId, authority.SourceLocale,
		)
		if err != nil {
			return err
		}
		var targetRevision *string
		var units []*intrav1.EmailLayoutContentUnit
		var localeValues []*intrav1.EmailLayoutLocaleValue
		if roomLocale != authority.SourceLocale {
			entry, loadErr := emailutil.LoadLayoutTranslationEntry(
				ctx, tx, req.Msg.EmailLayoutId, roomLocale,
			)
			if loadErr != nil {
				return loadErr
			}
			sourceUnits, unitErr := emailutil.ExtractLayoutContentUnits(derefString(state.ContentHTML))
			if unitErr != nil {
				return errs.FailedPrecondition(
					"Email Layout source unit markers require backfill before collaboration editing",
				)
			}
			units = make([]*intrav1.EmailLayoutContentUnit, 0, len(sourceUnits))
			for _, unit := range sourceUnits {
				units = append(units, &intrav1.EmailLayoutContentUnit{
					Handle: unit.Handle, Kind: unit.Kind, Element: unit.Element,
					Attribute: unit.Attribute, Order: uint32(unit.Order), SourceValue: unit.SourceValue,
				})
			}
			if entry != nil && entry.ContentHTML != nil {
				storedValues, valueErr := emailutil.ExtractLayoutStoredLocaleValues(*entry.ContentHTML)
				if valueErr != nil {
					return errs.FailedPrecondition(
						"Email Layout target unit markers require backfill before collaboration editing",
					)
				}
				localeValues = make([]*intrav1.EmailLayoutLocaleValue, 0, len(storedValues))
				for _, unit := range sourceUnits {
					if value, present := storedValues[unit.Handle]; present {
						localeValues = append(localeValues, &intrav1.EmailLayoutLocaleValue{Handle: unit.Handle, Value: value})
					}
				}
			}
			targetRevision, loadErr = deriveEmailLayoutTargetRevision(authority.DocumentRevision.String(), entry)
			if loadErr != nil {
				return loadErr
			}
		}
		contentHTML, contentText := derefString(state.ContentHTML), derefString(state.ContentText)
		if roomLocale != authority.SourceLocale {
			contentHTML, contentText = "", ""
		}
		response = connect.NewResponse(&intrav1.LoadEmailLayoutDocumentResponse{
			ContentHtml: contentHTML, ContentText: contentText,
			SourceLocale: authority.SourceLocale, Locale: roomLocale,
			DocumentRevision: authority.DocumentRevision.String(), TargetRevision: targetRevision,
			Units: units, LocaleValues: localeValues,
		})
		return nil
	}); err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return response, nil
}
