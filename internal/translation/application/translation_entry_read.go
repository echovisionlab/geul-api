package application

import (
	"context"
	"database/sql"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func parseTranslationTarget(target *managev1.TranslationTarget) (string, string, error) {
	if target == nil {
		return "", "", errs.InvalidArgument("target", "target is required")
	}
	definition, ok := translation.DefinitionForProto(target.EntityType)
	if !ok {
		return "", "", errs.InvalidArgument("target.entity_type", "unsupported translation entity type")
	}
	entityType := string(definition.Kind)
	entityID := strings.TrimSpace(target.EntityId)
	if entityID == "" {
		return "", "", errs.InvalidArgument("target.entity_id", "entity id is required")
	}
	return entityType, entityID, nil
}

func (s *TranslationService) scanTranslationEntryRow(
	ctx context.Context,
	rows *sql.Rows,
	entityType string,
	entityID string,
) (*managev1.TranslationEntry, error) {
	var locale string
	var title sql.NullString
	var summary sql.NullString
	var contentHTML sql.NullString
	var contentText sql.NullString
	var contentJSON []byte
	var updatedAt time.Time
	var ogAssetID sql.NullString

	if err := rows.Scan(
		&locale,
		&title,
		&summary,
		&contentHTML,
		&contentText,
		&contentJSON,
		&updatedAt,
		&ogAssetID,
	); err != nil {
		return nil, err
	}

	definition, _ := translation.DefinitionForKind(entityType)
	entry := &managev1.TranslationEntry{
		Target:    &managev1.TranslationTarget{EntityType: definition.Proto, EntityId: entityID},
		Locale:    locale,
		UpdatedAt: timestamppb.New(updatedAt),
	}
	if title.Valid {
		entry.Title = &title.String
	}
	if summary.Valid {
		entry.Summary = &summary.String
	}
	if contentHTML.Valid {
		entry.ContentHtml = &contentHTML.String
	}
	if contentText.Valid {
		entry.ContentText = &contentText.String
	}
	if len(contentJSON) > 0 {
		entry.ContentJson = contentJSON
	}
	if ogAssetID.Valid {
		asset, err := mediaasset.NewLifecycle(s.db, s.cdnDomain).ReadyAssetRef(ctx, ogAssetID.String)
		if err != nil {
			return nil, err
		}
		entry.OgAsset = asset
	}
	return entry, nil
}
