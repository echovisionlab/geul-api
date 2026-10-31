package series

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// PostSeriesCollaborationCheckpointFence authorizes the contributors of a
// collaboration save inside its transaction.
type PostSeriesCollaborationCheckpointFence interface {
	RequireCurrentContributors(
		context.Context,
		*gorm.DB,
		intrav1.CollaborationResourceType,
		string,
		[]string,
	) error
}

// LoadPostSeriesDocumentQuery reads one resident Post Series source or
// existing target locale room. Yjs state remains transient.
type LoadPostSeriesDocumentQuery struct {
	SeriesID string
	Locale   string
}

// SavePostSeriesDocumentCommand saves one room's locale fields.
// ContributorMemberIDs name the one origin Member editor-collab derived
// from admitted frames.
type SavePostSeriesDocumentCommand struct {
	SeriesID                 string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	Requested                *intrav1.PostSeriesLocaleFields
	ContributorMemberIDs     []string
}

func (s *Service) LoadPostSeriesDocument(ctx context.Context, query LoadPostSeriesDocumentQuery) (*intrav1.LoadPostSeriesDocumentResponse, error) {
	locale, err := canonicalPostSeriesRoomLocale(query.Locale)
	if err != nil {
		return nil, err
	}
	if _, err := parseCanonicalPostSeriesUUID(query.SeriesID, "series_id"); err != nil {
		return nil, err
	}
	var state postSeriesAIDocumentState
	// editor-collab reads as itself; the room's rows are read under their
	// locks so the revisions agree with the fields.
	if err := s.readLocked(ctx, mutation.Locked(func(context.Context, *gorm.DB, mutation.Require) error { return nil }), func(ctx context.Context, tx *gorm.DB) error {
		var err error
		state, err = s.loadAIDocumentState(ctx, tx, query.SeriesID, locale)
		return err
	}); err != nil {
		return nil, err
	}
	return &intrav1.LoadPostSeriesDocumentResponse{
		SourceLocale:     state.root.SourceLocale,
		Locale:           locale,
		LocaleExists:     state.localeExists,
		Source:           postSeriesLocaleFields(state.source.Title, state.source.Summary),
		Requested:        postSeriesRequestedLocaleFields(state.locale),
		DocumentRevision: string(state.documentRevision),
		TargetRevision:   core.OptionalRevisionString(state.targetRevision),
	}, nil
}

// postSeriesRoomSave is the outcome of a collaboration save.
type postSeriesRoomSave struct {
	documentRevision string
	targetRevision   *string
	changed          bool
	titleChanged     bool
}

func (s *Service) SavePostSeriesDocument(ctx context.Context, command SavePostSeriesDocumentCommand) (*intrav1.SavePostSeriesDocumentResponse, error) {
	locale, err := canonicalPostSeriesRoomLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	if _, err := parseCanonicalPostSeriesUUID(command.SeriesID, "series_id"); err != nil {
		return nil, err
	}
	expectedRevision, err := parseCanonicalPostSeriesUUID(command.ExpectedDocumentRevision, "expected_document_revision")
	if err != nil {
		return nil, err
	}
	if command.Requested == nil {
		return nil, domainerr.Required("requested")
	}
	contributor, err := requirePostSeriesCollaborationContributor(command.ContributorMemberIDs)
	if err != nil {
		return nil, err
	}

	var state postSeriesAIDocumentState
	saved, err := mutation.Run(ctx, s.mutations, mutation.Spec[postSeriesRoomSave]{
		// editor-collab calls as itself: the room is locked, then its
		// contributors' current permission authorizes the save.
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
			var err error
			if state, err = s.loadAIDocumentState(ctx, tx, command.SeriesID, locale); err != nil {
				return err
			}
			if state.contentRevision != expectedRevision {
				return domainerr.CollaborationConflict(
					intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
					"Post Series document changed; reload before saving", domainerr.CollaborationRevisions{Document: state.contentRevision.String()},
				)
			}
			return s.checkpoints.RequireCurrentContributors(
				ctx, tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST_SERIES,
				command.SeriesID, command.ContributorMemberIDs,
			)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (postSeriesRoomSave, error) {
			return s.savePostSeriesRoom(ctx, tx, state, command, locale, dbtime.Now())
		},
		Audit: func(saved postSeriesRoomSave) mutation.Audit {
			if !saved.changed {
				return mutation.Unchanged()
			}
			return mutation.MemberAudit(contributor, sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPostSeriesLocaleContentAuditRecord(metadata, command.SeriesID, locale, sharedtelemetry.AuditItemOperationUpdated)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return &intrav1.SavePostSeriesDocumentResponse{
		Locale: locale, DocumentRevision: saved.documentRevision, TargetRevision: saved.targetRevision,
	}, nil
}

// savePostSeriesRoom writes the room's requested locale fields: the source
// row and its content document revision, or the target row and its derived
// revision.
func (s *Service) savePostSeriesRoom(
	ctx context.Context,
	tx *gorm.DB,
	state postSeriesAIDocumentState,
	command SavePostSeriesDocumentCommand,
	locale string,
	now time.Time,
) (postSeriesRoomSave, error) {
	isSource := locale == state.root.SourceLocale
	if !isSource && !state.localeExists {
		return postSeriesRoomSave{}, domainerr.Precondition("Post Series target locale document does not exist")
	}
	title, summary, err := canonicalPostSeriesLocaleMutation(command.Requested, isSource)
	if err != nil {
		return postSeriesRoomSave{}, err
	}
	current := state.locale
	saved := postSeriesRoomSave{
		documentRevision: string(state.documentRevision),
		changed:          current == nil || !nullableStringEqual(current.Title, title) || !nullableStringEqual(current.Summary, summary),
		titleChanged:     current == nil || !nullableStringEqual(current.Title, title),
	}
	if isSource {
		if command.ExpectedTargetRevision != nil {
			return postSeriesRoomSave{}, domainerr.InvalidField("expected_target_revision", "source locale has no target revision")
		}
		if saved.changed {
			if err := updatePostSeriesLocaleRow(ctx, tx, command.SeriesID, locale, title, summary, now); err != nil {
				return postSeriesRoomSave{}, err
			}
			next, err := advanceSeriesContentDocument(ctx, tx, command.SeriesID, state.contentDocumentID, state.contentRevision, now)
			if err != nil {
				return postSeriesRoomSave{}, err
			}
			saved.documentRevision = next.String()
		}
	} else {
		if err := translation.ValidateExpectedTargetRevision(
			command.ExpectedTargetRevision, stringPostSeriesRevision(state.targetRevision), true,
		); err != nil {
			conflict := translation.WithDocumentRevision(err, state.contentRevision.String())
			return postSeriesRoomSave{}, domainerr.CollaborationConflict(
				intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
				"Post Series target changed; reload before saving", domainerr.CollaborationRevisionsOf(conflict),
			)
		}
		updatedAt := current.UpdatedAt
		if saved.changed {
			updatedAt = translation.NextTargetUpdatedAt(now, current.UpdatedAt)
			if err := updatePostSeriesLocaleRow(ctx, tx, command.SeriesID, locale, title, summary, updatedAt); err != nil {
				return postSeriesRoomSave{}, err
			}
		}
		derived, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
			LocaleExists: true, DocumentRevision: saved.documentRevision, LocaleUpdatedAt: &updatedAt,
		})
		if err != nil {
			return postSeriesRoomSave{}, err
		}
		saved.targetRevision = &derived
	}
	if saved.changed && saved.titleChanged {
		if _, err := s.ogRefresh.RequestCurrent(ctx, tx, command.SeriesID, locale, false, "series_collaboration_title_updated"); err != nil {
			return postSeriesRoomSave{}, err
		}
	}
	return saved, nil
}

func canonicalPostSeriesRoomLocale(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	normalized := localization.NormalizeSupportedLocale(trimmed)
	if value != trimmed || normalized == nil || *normalized != trimmed {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return trimmed, nil
}

func parseCanonicalPostSeriesUUID(value, field string) (uuid.UUID, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil || parsed.String() != trimmed || value != trimmed {
		return uuid.Nil, domainerr.InvalidField(field, "must be a canonical UUID")
	}
	return parsed, nil
}

func requirePostSeriesCollaborationContributor(values []string) (string, error) {
	if len(values) != 1 {
		return "", domainerr.InvalidField("contributor_member_ids", "requires exactly one origin Member")
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
			return nil, nil, domainerr.Required("requested.title")
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
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domainerr.Precondition("Post Series locale changed; reload before saving")
	}
	return nil
}

func stringPostSeriesRevision(value *core.Revision) string {
	if value == nil {
		return ""
	}
	return string(*value)
}
