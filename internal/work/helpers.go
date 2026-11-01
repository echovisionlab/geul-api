package work

import (
	"context"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

func nullableStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func normalizeInitialSourceLocale(ctx context.Context, store Store, locale string) string {
	if normalized := localization.NormalizeSupportedLocale(locale); normalized != nil {
		return *normalized
	}
	if defaultLocale, err := store.DefaultLocale(ctx); err == nil {
		if normalized := localization.NormalizeSupportedLocale(defaultLocale); normalized != nil {
			return *normalized
		}
	} else {
		slog.ErrorContext(ctx, "Translation runtime settings lookup failed; using the default Work source locale", "domain", "work", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
	}
	return translation.DefaultLocale
}

func resolveInitialSourceLocale(ctx context.Context, store Store, acceptLanguage string) string {
	if principal := auth.GetUser(ctx); principal != nil && principal.Authenticated {
		preferred, err := store.PreferredLocale(ctx, principal.MemberID.String())
		if err == nil && preferred != nil {
			if locale := localization.NormalizeSupportedLocale(*preferred); locale != nil {
				return *locale
			}
		} else if err != nil {
			slog.ErrorContext(ctx, "Member preferred locale lookup failed; inferring Work source locale from the request", "domain", "work", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	return normalizeInitialSourceLocale(ctx, store, "")
}

func resolveCreateSourceLocale(ctx context.Context, store Store, requestedLocale, acceptLanguage string) (string, error) {
	if requestedLocale != "" {
		if locale := localization.NormalizeExactSupportedLocale(requestedLocale); locale != nil {
			return *locale, nil
		}
		return "", domainerr.InvalidField("source_locale", "must be a supported canonical locale")
	}
	return resolveInitialSourceLocale(ctx, store, acceptLanguage), nil
}

func normalizeOptionalNullableString(value *string) (*string, bool) {
	if value == nil {
		return nil, false
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, true
	}
	return &normalized, true
}

// IsValidUUID reports whether value is a UUID accepted by Work public reads.
func IsValidUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }

// RequireExists verifies that the Work exists for an owning-domain consumer
// that stores a Work reference, through store bound to its database handle.
func RequireExists(ctx context.Context, store Store, workID string) error {
	return requireWorkExists(ctx, store, workID)
}

// workUpdatedAudit names a Work update record attributed to the request's
// member actor.
func workUpdatedAudit(build domainaudit.Builder) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditWorkUpdated, build)
}

func uniqueAssetIDs(candidates ...*string) []string {
	ids := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		id := strings.TrimSpace(*candidate)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (s *Service) loadReadyManageOgAssetRefs(ctx context.Context, candidates ...*string) (map[string]*commonv1.AssetRef, error) {
	ids := uniqueAssetIDs(candidates...)
	ready := make(map[string]*commonv1.AssetRef, len(ids))
	if len(ids) == 0 {
		return ready, nil
	}
	if err := s.read(ctx, func(store Store) error {
		var err error
		ready, err = store.ReadyAssets(ctx, ids)
		return err
	}); err != nil {
		return nil, err
	}
	return ready, nil
}

func (s *Service) readyManageOgAssetRef(ctx context.Context, candidates ...*string) (*commonv1.AssetRef, error) {
	ready, err := s.loadReadyManageOgAssetRefs(ctx, candidates...)
	if err != nil {
		return nil, err
	}
	return manageOgAssetFromReadyMap(ready, candidates...), nil
}

func manageOgAssetFromReadyMap(ready map[string]*commonv1.AssetRef, candidates ...*string) *commonv1.AssetRef {
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if asset := ready[strings.TrimSpace(*candidate)]; asset != nil {
			return asset
		}
	}
	return nil
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
