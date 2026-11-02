package post

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// stubStore is a Store for the pure internal tests: the methods it sets
// answer from its fields, and any other call panics on the nil Store.
type stubStore struct {
	Store
	preferredLocale *string
	defaultLocale   string
	// texts are the post_translation rows by locale.
	texts   map[string]AIDocumentLocaleMetadata
	written []LocaleMetadata
}

func (s *stubStore) MemberPreferredLocale(context.Context, string) (*string, error) {
	return s.preferredLocale, nil
}

func (s *stubStore) DefaultTranslationLocale(context.Context) (string, error) {
	return s.defaultLocale, nil
}

func (s *stubStore) LocaleText(_ context.Context, _, locale string, _ bool) (AIDocumentLocaleMetadata, bool, error) {
	text, ok := s.texts[locale]
	return text, ok, nil
}

func (s *stubStore) UpsertLocaleText(_ context.Context, _, locale string, title, summary *string, now time.Time) error {
	if s.texts == nil {
		s.texts = map[string]AIDocumentLocaleMetadata{}
	}
	s.texts[locale] = AIDocumentLocaleMetadata{Title: title, Summary: summary}
	s.written = append(s.written, LocaleMetadata{Locale: locale, Title: title, Summary: summary, UpdatedAt: now})
	return nil
}

func (s *stubStore) AppendMemberAudit(ctx context.Context, writer domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendMember(ctx, nil, writer, memberID, action, build)
}
