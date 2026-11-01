package public

import (
	"context"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func loadPublicProgramEventType(ctx context.Context, store Store, typeID string, acceptLanguage string) (TypeEntry, error) {
	eventType, err := store.Type(ctx, typeID)
	if err != nil {
		return TypeEntry{}, err
	}
	localesByType, err := store.TypeLocales(ctx, []string{eventType.ID})
	if err != nil {
		return TypeEntry{}, err
	}
	return publicTypeEntry(eventType, localesByType[eventType.ID], acceptLanguage)
}

func publicTypeEntry(
	eventType programevent.Type,
	locales []programevent.TypeLocale,
	acceptLanguage string,
) (TypeEntry, error) {
	locale, err := selectPublicProgramEventTypeLocale(locales, eventType.ID, acceptLanguage)
	if err != nil {
		return TypeEntry{}, err
	}
	return TypeEntry{Type: eventType, Name: locale.Name, Description: locale.Description}, nil
}

func selectPublicProgramEventTypeLocale(rows []programevent.TypeLocale, typeID string, acceptLanguage string) (programevent.TypeLocale, error) {
	if len(rows) == 0 {
		return programevent.TypeLocale{}, domainerr.ResourceNotFound("program event type locale", typeID)
	}
	requested := resolveRequestedLocale(acceptLanguage)
	if matched, ok := findProgramEventTypeLocale(rows, requested); ok {
		return matched, nil
	}
	if matched, ok := findProgramEventTypeLocale(rows, defaultPublicLocale); ok {
		return matched, nil
	}
	return rows[0], nil
}

func collectPublicProgramEventTypeIDs(events []programevent.Event) []string {
	typeIDs := make([]string, 0, len(events))
	for i := range events {
		if events[i].TypeID != "" {
			typeIDs = append(typeIDs, events[i].TypeID)
		}
	}
	return sliceutil.UniqueTrimmed(typeIDs)
}

func findProgramEventTypeLocale(rows []programevent.TypeLocale, locale string) (programevent.TypeLocale, bool) {
	for _, row := range rows {
		if normalizeSourceLocale(row.Locale) == normalizeSourceLocale(locale) {
			return row, true
		}
	}
	return programevent.TypeLocale{}, false
}

func loadProgramEventPosterAsset(ctx context.Context, store Store, assets Assets, eventID string) *commonv1.AssetRef {
	fileID, err := store.PosterFileID(ctx, eventID)
	if err != nil || strings.TrimSpace(fileID) == "" {
		if err != nil {
			slog.ErrorContext(ctx, "Program Event poster lookup failed; serving event without poster", "domain", "programevent", "operation", "load_poster", failurecause.Attr(err), "event_id", eventID)
		}
		return nil
	}
	return assets.ResolveReadyAssetForSourceFile(ctx, fileID, "poster", "image")
}

func loadProgramEventFileAsset(ctx context.Context, assets Assets, fileID *string) *commonv1.AssetRef {
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil
	}
	return assets.ResolveReadyAssetForSourceFile(ctx, *fileID, "image", "poster", "map_image")
}

func (s *Service) loadPublicProgramEventCredits(ctx context.Context, store Store, eventID string) ([]Credit, error) {
	rows, err := store.Credits(ctx, eventID)
	if err != nil {
		return nil, err
	}
	memberIDs := make([]string, 0, len(rows))
	imageFileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ArtistImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *row.ArtistImageFileID)
		}
		if row.MemberID != nil {
			memberIDs = append(memberIDs, *row.MemberID)
		}
	}
	members, err := s.creditMembers.LoadPublicCreditMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	images, err := s.assets.ResolveReadyAssetsForSourceFiles(ctx, imageFileIDs, "image")
	if err != nil {
		return nil, err
	}
	result := make([]Credit, 0, len(rows))
	for _, row := range rows {
		item := Credit{
			ID:          row.ID,
			DisplayName: row.DisplayName,
			CreditRole:  row.CreditRole,
			Description: row.Description,
			SortOrder:   row.SortOrder,
		}
		if row.ArtistID != nil {
			name := ""
			if row.ArtistName != nil {
				name = *row.ArtistName
			}
			item.Artist = &CreditArtist{ID: *row.ArtistID, Name: name, Slug: row.ArtistSlug}
			if row.ArtistImageFileID != nil {
				item.Artist.ImageAsset = images[*row.ArtistImageFileID]
			}
		}
		if row.MemberID != nil {
			item.Member = members[*row.MemberID]
		}
		result = append(result, item)
	}
	return result, nil
}

func collectPublicProgramEventIDs(events []programevent.Event) []string {
	ids := make([]string, 0, len(events))
	for i := range events {
		ids = append(ids, events[i].ID)
	}
	return ids
}
