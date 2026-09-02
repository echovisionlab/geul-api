package series

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ApplyProviderTranslationCandidateWithDB patches only surviving request-time
// Series locale fields. If the requested target became the current source,
// the same accepted response advances the shared document revision instead of
// manufacturing a target revision.
func ApplyProviderTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if tx == nil || job == nil || job.EntityType != "series" || candidate == nil || !candidate.HasProviderUnitPatch() {
		return errs.Internal(errors.New("post Series provider translation candidate is required"))
	}
	if auditWriter == nil {
		return errs.Internal(errors.New("post Series provider translation Audit writer is required"))
	}
	memberID, err := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if err != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errs.InternalMsg("Post Series provider translation requires canonical requester Member")
	}
	now := input.Now.UTC()
	if now.IsZero() {
		return errs.InternalMsg("Post Series provider translation time is required")
	}
	current, err := loadSeriesInterchangeState(ctx, tx, job.EntityID, job.TargetLocale, true)
	if err != nil {
		return err
	}
	patch, _ := candidate.ProviderPatch()
	nextTitle, nextSummary, nextContentText := current.target.Title, current.target.Summary, current.target.ContentText
	for _, unit := range patch.Units {
		result, ok := patch.Results[unit.UnitID]
		if !ok || unit.ContainerType != translation.ContainerTypeEntity || unit.ContainerID != job.EntityID {
			continue
		}
		value := result.TranslatedText
		switch unit.UnitID {
		case "entity:title":
			nextTitle = &value
		case "entity:summary":
			nextSummary = &value
		case "entity:content_text":
			nextContentText = &value
		}
	}
	if current.exists && nullableStringEqual(current.target.Title, nextTitle) &&
		nullableStringEqual(current.target.Summary, nextSummary) &&
		nullableStringEqual(current.target.ContentText, nextContentText) {
		return nil
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	nextUpdatedAt := now
	if current.exists {
		nextUpdatedAt = translation.NextTargetUpdatedAt(now, current.target.UpdatedAt)
		updated := tx.WithContext(ctx).Table("series_translation").
			Where("entity_id = ? AND locale = ?", job.EntityID, job.TargetLocale).
			Updates(structured.Fields{
				"title": nextTitle, "summary": nextSummary, "content_text": nextContentText, "updated_at": nextUpdatedAt,
			})
		if updated.Error != nil {
			return errs.Internal(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return errs.FailedPrecondition("Post Series locale changed; retry")
		}
	} else {
		if job.TargetLocale == current.sourceLocale {
			return errs.FailedPrecondition("Post Series source locale copy is missing")
		}
		operation = sharedtelemetry.AuditItemOperationCreated
		if err := tx.WithContext(ctx).Table("series_translation").Create(&model.SeriesTranslation{
			EntityID: job.EntityID, Locale: job.TargetLocale, Title: nextTitle, Summary: nextSummary,
			ContentText: nextContentText, CreatedAt: nextUpdatedAt, UpdatedAt: nextUpdatedAt,
		}).Error; err != nil {
			return errs.Internal(err)
		}
	}
	if job.TargetLocale == current.sourceLocale {
		state, err := loadSeriesContentDocumentState(ctx, tx, job.EntityID, false)
		if err != nil {
			return err
		}
		if _, err := advanceSeriesContentDocument(ctx, tx, job.EntityID, state.ID, state.Revision, now); err != nil {
			return err
		}
	}
	return domainaudit.AppendMember(
		ctx, tx, auditWriter, memberID.String(), sharedtelemetry.AuditPostSeriesUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostSeriesLocaleContentAuditRecord(
				metadata, job.EntityID, job.TargetLocale, operation,
			)
		},
	)
}

// UpsertTranslationEntry persists one Series locale entry. Row existence is
// the only target-presence authority; the source locale role and document
// revision are owned by the Series root and content document.
func UpsertTranslationEntry(
	ctx context.Context,
	db *gorm.DB,
	seriesID string,
	locale string,
	input translation.EntryWrite,
) error {
	return db.WithContext(ctx).Exec(
		`INSERT INTO series_translation (
			entity_id, locale,
			title, summary, content_json, content_html, content_text,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			content_json = EXCLUDED.content_json,
			content_html = EXCLUDED.content_html,
			content_text = EXCLUDED.content_text,
			updated_at = EXCLUDED.updated_at`,
		seriesID,
		locale,
		input.Title,
		input.Summary,
		jsonValueOrNil(input.ContentJSON),
		input.ContentHTML,
		input.ContentText,
		input.Now,
		input.Now,
	).Error
}

// SaveSourceLocaleDocument persists the authoritative Series copy, makes it
// the only source-locale row, and keeps the Series root projection aligned in
// the same transaction.
func SaveSourceLocaleDocument(
	ctx context.Context,
	db *gorm.DB,
	seriesID string,
	locale string,
	title *string,
	summary *string,
	now time.Time,
) error {
	if title == nil || strings.TrimSpace(*title) == "" {
		return errs.Required("title")
	}
	if err := UpsertTranslationEntry(ctx, db, seriesID, locale, translation.EntryWrite{
		Title:       title,
		Summary:     summary,
		ContentText: summary,
		Now:         now,
	}); err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&model.Series{}).Where("id = ?", seriesID).
		Updates(structured.Fields{"source_locale": locale, "updated_at": now}).Error
}

func jsonValueOrNil(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}
