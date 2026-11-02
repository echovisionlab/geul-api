package series

import (
	"context"
	"errors"
	"strings"
	"time"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// LocaleFields are the locale copy fields of a collaboration room.
type LocaleFields struct {
	Title   *string
	Summary *string
}

// LoadPostSeriesDocumentQuery reads one resident Post Series source or
// existing target locale room. Yjs state remains transient.
type LoadPostSeriesDocumentQuery struct {
	SeriesID string
	Locale   string
}

// RoomDocument is the state of a collaboration room.
type RoomDocument struct {
	SourceLocale     string
	Locale           string
	LocaleExists     bool
	Source           LocaleFields
	Requested        LocaleFields
	DocumentRevision string
	TargetRevision   *string
}

// SavePostSeriesDocumentCommand saves one room's locale fields.
// ContributorMemberIDs name the one origin Member editor-collab derived
// from admitted frames.
type SavePostSeriesDocumentCommand struct {
	SeriesID                 string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	Requested                *LocaleFields
	ContributorMemberIDs     []string
}

// RoomSave is the outcome of a collaboration save.
type RoomSave struct {
	Locale           string
	DocumentRevision string
	TargetRevision   *string
}

func (s *Service) LoadPostSeriesDocument(ctx context.Context, query LoadPostSeriesDocumentQuery) (RoomDocument, error) {
	locale, err := canonicalPostSeriesRoomLocale(query.Locale)
	if err != nil {
		return RoomDocument{}, err
	}
	if _, err := parseCanonicalPostSeriesUUID(query.SeriesID, "series_id"); err != nil {
		return RoomDocument{}, err
	}
	var state postSeriesAIDocumentState
	// editor-collab reads as itself; the room's rows are read under their
	// locks so the revisions agree with the fields.
	if err := s.readLocked(ctx, mutation.Locked(func(context.Context, Store, mutation.Require) error { return nil }), func(ctx context.Context, store Store) error {
		var err error
		state, err = loadAIDocumentState(ctx, store, query.SeriesID, locale)
		return err
	}); err != nil {
		return RoomDocument{}, err
	}
	return RoomDocument{
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

func (s *Service) SavePostSeriesDocument(ctx context.Context, command SavePostSeriesDocumentCommand) (RoomSave, error) {
	locale, err := canonicalPostSeriesRoomLocale(command.Locale)
	if err != nil {
		return RoomSave{}, err
	}
	if _, err := parseCanonicalPostSeriesUUID(command.SeriesID, "series_id"); err != nil {
		return RoomSave{}, err
	}
	expectedRevision, err := parseCanonicalPostSeriesUUID(command.ExpectedDocumentRevision, "expected_document_revision")
	if err != nil {
		return RoomSave{}, err
	}
	if command.Requested == nil {
		return RoomSave{}, domainerr.Required("requested")
	}
	contributor, err := requirePostSeriesCollaborationContributor(command.ContributorMemberIDs)
	if err != nil {
		return RoomSave{}, err
	}

	var state postSeriesAIDocumentState
	saved, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[postSeriesRoomSave, Store]{
		// editor-collab calls as itself: the room is locked, then its
		// contributors' current permission authorizes the save.
		Access: mutation.Locked(func(ctx context.Context, store Store, _ mutation.Require) error {
			var err error
			if state, err = loadAIDocumentState(ctx, store, command.SeriesID, locale); err != nil {
				return err
			}
			if state.contentRevision != expectedRevision {
				return domainerr.CollaborationConflict(
					intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
					"Post Series document changed; reload before saving", domainerr.CollaborationRevisions{Document: state.contentRevision.String()},
				)
			}
			return store.RequireCurrentContributors(ctx, command.SeriesID, command.ContributorMemberIDs)
		}),
		Change: func(ctx context.Context, store Store) (postSeriesRoomSave, error) {
			return savePostSeriesRoom(ctx, store, state, command, locale, dbtime.Now())
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
		return RoomSave{}, err
	}
	return RoomSave{Locale: locale, DocumentRevision: saved.documentRevision, TargetRevision: saved.targetRevision}, nil
}

// savePostSeriesRoom writes the room's requested locale fields: the source
// row and its content document revision, or the target row and its derived
// revision.
func savePostSeriesRoom(
	ctx context.Context,
	store Store,
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
		changed:          current == nil || !equalOptionalString(current.Title, title) || !equalOptionalString(current.Summary, summary),
		titleChanged:     current == nil || !equalOptionalString(current.Title, title),
	}
	room := postSeriesRoom{store: store, state: state, seriesID: command.SeriesID, locale: locale, title: title, summary: summary}
	if isSource {
		err = room.saveSource(ctx, command.ExpectedTargetRevision, now, &saved)
	} else {
		err = room.saveTarget(ctx, command.ExpectedTargetRevision, now, &saved)
	}
	if err != nil {
		return postSeriesRoomSave{}, err
	}
	if saved.changed && saved.titleChanged {
		if _, err := store.RequestOG(ctx, command.SeriesID, locale, false, "series_collaboration_title_updated"); err != nil {
			return postSeriesRoomSave{}, err
		}
	}
	return saved, nil
}

// postSeriesRoom is the locked room a save writes and the copy it writes.
type postSeriesRoom struct {
	store          Store
	state          postSeriesAIDocumentState
	seriesID       string
	locale         string
	title, summary *string
}

// saveSource writes the source row and advances the content document.
func (r postSeriesRoom) saveSource(ctx context.Context, expectedTarget *string, now time.Time, saved *postSeriesRoomSave) error {
	if expectedTarget != nil {
		return domainerr.InvalidField("expected_target_revision", "source locale has no target revision")
	}
	if !saved.changed {
		return nil
	}
	if err := updatePostSeriesLocaleRow(ctx, r.store, r.seriesID, r.locale, r.title, r.summary, now); err != nil {
		return err
	}
	next, err := advanceSeriesContentDocument(ctx, r.store, r.seriesID, r.state.contentDocumentID, r.state.contentRevision, now)
	if err != nil {
		return err
	}
	saved.documentRevision = next.String()
	return nil
}

// saveTarget checks the target revision, writes the target row and derives
// its next revision.
func (r postSeriesRoom) saveTarget(ctx context.Context, expectedTarget *string, now time.Time, saved *postSeriesRoomSave) error {
	if err := translation.ValidateExpectedTargetRevision(
		expectedTarget, stringPostSeriesRevision(r.state.targetRevision), true,
	); err != nil {
		conflict := translation.WithDocumentRevision(err, r.state.contentRevision.String())
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Post Series target changed; reload before saving", domainerr.CollaborationRevisionsOf(conflict),
		)
	}
	updatedAt := r.state.locale.UpdatedAt
	if saved.changed {
		updatedAt = translation.NextTargetUpdatedAt(now, r.state.locale.UpdatedAt)
		if err := updatePostSeriesLocaleRow(ctx, r.store, r.seriesID, r.locale, r.title, r.summary, updatedAt); err != nil {
			return err
		}
	}
	derived, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: saved.documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return err
	}
	saved.targetRevision = &derived
	return nil
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

func postSeriesLocaleFields(title, summary *string) LocaleFields {
	return LocaleFields{Title: cloneString(title), Summary: cloneString(summary)}
}

func postSeriesRequestedLocaleFields(value *Translation) LocaleFields {
	if value == nil {
		return LocaleFields{}
	}
	return postSeriesLocaleFields(value.Title, value.Summary)
}

func canonicalPostSeriesLocaleMutation(
	requested *LocaleFields,
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
	store Store,
	seriesID, locale string,
	title, summary *string,
	now time.Time,
) error {
	err := store.UpdateTranslation(ctx, seriesID, locale, title, summary, summary, now)
	if errors.Is(err, ErrTranslationNotFound) {
		return domainerr.Precondition("Post Series locale changed; reload before saving")
	}
	return err
}

func stringPostSeriesRevision(value *core.Revision) string {
	if value == nil {
		return ""
	}
	return string(*value)
}
