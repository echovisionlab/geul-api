package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

type translationRuntimeSettings = translation.RuntimeSettings

func normalizeTranslationRuntimeSettings(input translationRuntimeSettings) (translationRuntimeSettings, error) {
	return translation.NormalizeRuntimeSettings(input)
}

func loadTranslationRuntimeSettings(ctx context.Context, db *gorm.DB) (translationRuntimeSettings, error) {
	return translation.LoadRuntimeSettings(ctx, db)
}

func toProtoTranslationSettings(settings translationRuntimeSettings) *managev1.TranslationSettings {
	if settings.DefaultLocale == "" {
		return nil
	}

	var updatedAt *time.Time
	if settings.UpdatedAt != nil {
		t := settings.UpdatedAt.UTC()
		updatedAt = &t
	}

	resp := &managev1.TranslationSettings{
		DefaultLocale:  settings.DefaultLocale,
		ProtectedTerms: append([]string(nil), settings.ProtectedTerms...),
	}
	if updatedAt != nil {
		resp.UpdatedAt = timestamppb.New(*updatedAt)
	}
	return resp
}

func translationRuntimeSettingsFromProto(proto *managev1.TranslationSettings) (translationRuntimeSettings, error) {
	if proto == nil {
		return translationRuntimeSettings{}, fmt.Errorf("translation settings are required")
	}

	settings := translationRuntimeSettings{
		DefaultLocale:  strings.TrimSpace(proto.DefaultLocale),
		ProtectedTerms: append([]string(nil), proto.ProtectedTerms...),
	}

	return normalizeTranslationRuntimeSettings(settings)
}
