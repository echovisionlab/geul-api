package emailauthoring

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// EmailLayoutTranslationInterchangeTarget is Email Layout's sparse target
// values keyed by stable source-unit handle.
type EmailLayoutTranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Values   map[string]string
}

// EmailLayoutTranslationInterchangeMutation replaces the complete sparse
// target value map already compiled by the shared XLIFF adapter.
type EmailLayoutTranslationInterchangeMutation struct {
	TargetLocale     string
	ExpectedRevision string
	ExpectedPresence bool
	Values           map[string]string
	Now              time.Time
}

// LoadEmailLayoutTranslationInterchangeTarget loads target-owned values
// without resolving omitted units from the canonical source wrapper.
func LoadEmailLayoutTranslationInterchangeTarget(
	ctx context.Context,
	store Store,
	layoutID string,
	targetLocale string,
) (EmailLayoutTranslationInterchangeTarget, error) {
	layoutID, err := emailcontent.CanonicalID("email_layout_id", layoutID)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, err
	}
	targetLocale, err = canonicalEmailLayoutRoomLocale(targetLocale)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, err
	}
	authority, err := loadEmailLayoutDocumentAuthority(ctx, store, layoutID, emailcontent.ShareLock)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, err
	}
	if targetLocale == authority.SourceLocale {
		return EmailLayoutTranslationInterchangeTarget{}, domainerr.InvalidField("target_locale", "must differ from the Email Layout source locale")
	}
	entry, err := store.LockLayoutTranslationEntry(ctx, layoutID, targetLocale, emailcontent.ShareLock)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, err
	}
	return projectEmailLayoutInterchangeTarget(authority.DocumentRevision.String(), entry)
}

// ApplyEmailLayoutTranslationInterchange applies one complete sparse value map
// under the Layout lifecycle fence and target-row CAS.
func ApplyEmailLayoutTranslationInterchange(
	ctx context.Context,
	store Store,
	layoutID string,
	sourceLocale string,
	input EmailLayoutTranslationInterchangeMutation,
) (EmailLayoutTranslationInterchangeTarget, bool, error) {
	layoutID, err := emailcontent.CanonicalID("email_layout_id", layoutID)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	input.TargetLocale, err = canonicalEmailLayoutRoomLocale(input.TargetLocale)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	sourceLocale, err = canonicalEmailLayoutRoomLocale(sourceLocale)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	if input.TargetLocale == sourceLocale {
		return EmailLayoutTranslationInterchangeTarget{}, false, domainerr.InvalidField("target_locale", "must be a non-source Email Layout locale")
	}
	if err := lockEmailLayoutAIDocumentRoot(ctx, store, layoutID, emailcontent.UpdateLock); err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	if err := store.RequireLayoutMutable(ctx, layoutID); err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	authority, err := loadEmailLayoutDocumentAuthority(ctx, store, layoutID, emailcontent.UpdateLock)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	if authority.SourceLocale != sourceLocale {
		return EmailLayoutTranslationInterchangeTarget{}, false, domainerr.Precondition("Email Layout source locale changed; reload before importing")
	}
	source, err := store.CanonicalLayoutSource(ctx, layoutID, sourceLocale)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	entry, err := store.LockLayoutTranslationEntry(ctx, layoutID, input.TargetLocale, emailcontent.UpdateLock)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	current, err := projectEmailLayoutInterchangeTarget(authority.DocumentRevision.String(), entry)
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	if current.Exists != input.ExpectedPresence ||
		(current.Exists && current.Revision != strings.TrimSpace(input.ExpectedRevision)) ||
		(!current.Exists && strings.TrimSpace(input.ExpectedRevision) != "") {
		var currentTargetRevision *string
		if current.Exists {
			targetRevision := current.Revision
			currentTargetRevision = &targetRevision
		}
		return EmailLayoutTranslationInterchangeTarget{}, false, &EmailLayoutAIDocumentRevisionConflictError{
			Kind:                    EmailLayoutAIDocumentTargetRevisionConflict,
			CurrentDocumentRevision: authority.DocumentRevision.String(),
			CurrentTargetRevision:   currentTargetRevision,
		}
	}
	nextValues := maps.Clone(input.Values)
	if nextValues == nil {
		nextValues = make(map[string]string)
	}
	changed := !current.Exists || !maps.Equal(current.Values, nextValues)
	if !changed {
		return current, false, nil
	}
	if input.Now.IsZero() {
		input.Now = dbtime.Now()
	} else {
		input.Now = input.Now.UTC()
	}
	updatedAt, err := saveEmailLayoutTarget(ctx, store, layoutID, input.TargetLocale, derefString(source.ContentHTML), nextValues, input.Now, emailLayoutInterchangeUpdatedAt(entry))
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: authority.DocumentRevision.String(), LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, false, err
	}
	return EmailLayoutTranslationInterchangeTarget{
		Exists: true, Revision: revision, Values: nextValues,
	}, true, nil
}

func projectEmailLayoutInterchangeTarget(
	documentRevision string,
	entry *email.LayoutTranslationEntry,
) (EmailLayoutTranslationInterchangeTarget, error) {
	if entry == nil {
		return EmailLayoutTranslationInterchangeTarget{Values: make(map[string]string)}, nil
	}
	values := make(map[string]string)
	var err error
	if entry.ContentHTML != nil {
		values, err = email.ExtractLayoutStoredLocaleValues(*entry.ContentHTML)
		if err != nil {
			return EmailLayoutTranslationInterchangeTarget{}, domainerr.Precondition("Email Layout target unit markers require backfill before interchange").WithCause(failurecause.ServerFault(err))
		}
	}
	updatedAt := entry.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return EmailLayoutTranslationInterchangeTarget{}, err
	}
	return EmailLayoutTranslationInterchangeTarget{
		Exists: true, Revision: revision, Values: values,
	}, nil
}

func emailLayoutInterchangeUpdatedAt(entry *email.LayoutTranslationEntry) time.Time {
	if entry == nil {
		return time.Time{}
	}
	return entry.UpdatedAt
}
