package series

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// InternalPostSeriesService owns persistence for one resident Post Series
// source or existing target locale room. Yjs state remains transient.
type InternalPostSeriesService struct {
	documents   *AIDocumentService
	checkpoints PostSeriesCollaborationCheckpointFence
}

type PostSeriesCollaborationCheckpointFence interface {
	RequireCurrentContributors(
		context.Context,
		*gorm.DB,
		intrav1.CollaborationResourceType,
		string,
		[]string,
	) error
}

func NewInternalPostSeriesService(
	documents *AIDocumentService,
	checkpoints PostSeriesCollaborationCheckpointFence,
) *InternalPostSeriesService {
	if documents == nil || checkpoints == nil {
		panic("Post Series collaboration dependencies are required")
	}
	return &InternalPostSeriesService{documents: documents, checkpoints: checkpoints}
}

func (s *InternalPostSeriesService) LoadDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadPostSeriesDocumentRequest],
) (*connect.Response[intrav1.LoadPostSeriesDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	locale, err := canonicalPostSeriesRoomLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	if _, err := parseCanonicalPostSeriesUUID(req.Msg.SeriesId, "series_id"); err != nil {
		return nil, err
	}

	var response *connect.Response[intrav1.LoadPostSeriesDocumentResponse]
	err = s.documents.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := s.documents.loadAIDocumentState(ctx, tx, req.Msg.SeriesId, locale)
		if err != nil {
			return err
		}
		response = connect.NewResponse(&intrav1.LoadPostSeriesDocumentResponse{
			SourceLocale:     state.root.SourceLocale,
			Locale:           locale,
			LocaleExists:     state.localeExists,
			Source:           postSeriesLocaleFields(state.source.Title, state.source.Summary),
			Requested:        postSeriesRequestedLocaleFields(state.locale),
			DocumentRevision: string(state.documentRevision),
			TargetRevision:   postSeriesStringRevision(state.targetRevision),
		})
		return nil
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return response, nil
}

func (s *InternalPostSeriesService) SaveDocument(
	ctx context.Context,
	req *connect.Request[intrav1.SavePostSeriesDocumentRequest],
) (*connect.Response[intrav1.SavePostSeriesDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	r := req.Msg
	locale, err := canonicalPostSeriesRoomLocale(r.Locale)
	if err != nil {
		return nil, err
	}
	if _, err := parseCanonicalPostSeriesUUID(r.SeriesId, "series_id"); err != nil {
		return nil, err
	}
	expectedRevision, err := parseCanonicalPostSeriesUUID(r.ExpectedDocumentRevision, "expected_document_revision")
	if err != nil {
		return nil, err
	}
	if r.Requested == nil {
		return nil, errs.Required("requested")
	}
	contributor, err := requirePostSeriesCollaborationContributor(r.ContributorMemberIds)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var documentRevision string
	var targetRevision *string
	err = s.documents.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := s.documents.loadAIDocumentState(ctx, tx, r.SeriesId, locale)
		if err != nil {
			return err
		}
		if state.contentRevision != expectedRevision {
			return errs.CollaborationConflict(
				intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
				"Post Series document changed; reload before saving",
			)
		}
		if err := s.checkpoints.RequireCurrentContributors(
			ctx, tx,
			intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST_SERIES,
			r.SeriesId, r.ContributorMemberIds,
		); err != nil {
			return err
		}
		if locale != state.root.SourceLocale && !state.localeExists {
			return errs.FailedPrecondition("Post Series target locale document does not exist")
		}

		title, summary, err := canonicalPostSeriesLocaleMutation(
			r.Requested, locale == state.root.SourceLocale,
		)
		if err != nil {
			return err
		}
		current := state.locale
		changed := current == nil ||
			!nullableStringEqual(current.Title, title) ||
			!nullableStringEqual(current.Summary, summary)
		titleChanged := current == nil || !nullableStringEqual(current.Title, title)
		documentRevision = string(state.documentRevision)

		if locale == state.root.SourceLocale {
			if r.ExpectedTargetRevision != nil {
				return errs.InvalidArgument("expected_target_revision", "source locale has no target revision")
			}
			if changed {
				if err := updatePostSeriesLocaleRow(ctx, tx, r.SeriesId, locale, title, summary, now); err != nil {
					return err
				}
				next, err := advanceSeriesContentDocument(
					ctx, tx, r.SeriesId, state.contentDocumentID, state.contentRevision, now,
				)
				if err != nil {
					return err
				}
				documentRevision = next.String()
			}
		} else {
			if err := translation.ValidateExpectedTargetRevision(
				r.ExpectedTargetRevision, stringPostSeriesRevision(state.targetRevision), true,
			); err != nil {
				return errs.CollaborationConflict(
					intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
					"Post Series target changed; reload before saving",
				)
			}
			updatedAt := current.UpdatedAt
			if changed {
				updatedAt = translation.NextTargetUpdatedAt(now, current.UpdatedAt)
				if err := updatePostSeriesLocaleRow(ctx, tx, r.SeriesId, locale, title, summary, updatedAt); err != nil {
					return err
				}
			}
			derived, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
				LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
			})
			if err != nil {
				return errs.Internal(err)
			}
			targetRevision = &derived
		}

		if changed {
			if err := appendPostSeriesCollaborationAudit(
				ctx, tx, s.documents.auditWriter, contributor, r.SeriesId, locale,
			); err != nil {
				return err
			}
			if titleChanged {
				if _, err := s.documents.ogRefresh.RequestCurrent(
					ctx, tx, r.SeriesId, locale, false, "series_collaboration_title_updated",
				); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&intrav1.SavePostSeriesDocumentResponse{
		Locale: locale, DocumentRevision: documentRevision, TargetRevision: targetRevision,
	}), nil
}

func canonicalPostSeriesRoomLocale(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	normalized := localization.NormalizeSupportedLocale(trimmed)
	if value != trimmed || normalized == nil || *normalized != trimmed {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return trimmed, nil
}

func parseCanonicalPostSeriesUUID(value, field string) (uuid.UUID, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil || parsed.String() != trimmed || value != trimmed {
		return uuid.Nil, errs.InvalidArgument(field, "must be a canonical UUID")
	}
	return parsed, nil
}

func requirePostSeriesCollaborationContributor(values []string) (string, error) {
	if len(values) != 1 {
		return "", errs.InvalidArgument("contributor_member_ids", "requires exactly one origin Member")
	}
	if _, err := parseCanonicalPostSeriesUUID(values[0], "contributor_member_ids"); err != nil {
		return "", err
	}
	return values[0], nil
}

func postSeriesLocaleFields(title, summary *string) *intrav1.PostSeriesLocaleFields {
	return &intrav1.PostSeriesLocaleFields{Title: cloneString(title), Summary: cloneString(summary)}
}

func postSeriesRequestedLocaleFields(value *model.SeriesTranslation) *intrav1.PostSeriesLocaleFields {
	if value == nil {
		return &intrav1.PostSeriesLocaleFields{}
	}
	return postSeriesLocaleFields(value.Title, value.Summary)
}

func canonicalPostSeriesLocaleMutation(
	requested *intrav1.PostSeriesLocaleFields,
	source bool,
) (*string, *string, error) {
	title := cloneString(requested.Title)
	summary := cloneString(requested.Summary)
	if source {
		if title == nil || strings.TrimSpace(*title) == "" {
			return nil, nil, errs.Required("requested.title")
		}
		trimmed := strings.TrimSpace(*title)
		title = &trimmed
	}
	return title, summary, nil
}

func updatePostSeriesLocaleRow(
	ctx context.Context,
	tx *gorm.DB,
	seriesID, locale string,
	title, summary *string,
	now time.Time,
) error {
	result := tx.WithContext(ctx).Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, locale).
		Updates(structured.Fields{
			"title": title, "summary": summary, "content_text": summary, "updated_at": now,
		})
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("Post Series locale changed; reload before saving")
	}
	return nil
}

func appendPostSeriesCollaborationAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID, seriesID, locale string,
) error {
	return domainaudit.AppendMember(
		ctx, tx, writer, memberID, sharedtelemetry.AuditPostSeriesUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostSeriesLocaleContentAuditRecord(
				metadata, seriesID, locale, sharedtelemetry.AuditItemOperationUpdated,
			)
		},
	)
}

func postSeriesStringRevision(value *core.Revision) *string {
	if value == nil {
		return nil
	}
	revision := string(*value)
	return &revision
}

func stringPostSeriesRevision(value *core.Revision) string {
	if value == nil {
		return ""
	}
	return string(*value)
}
