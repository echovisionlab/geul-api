package emailauthoring

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SaveLayoutDocumentCommand saves a collaboration room's Email Layout
// document: the source HTML of the source locale room, or the unit values of
// an existing target locale room.
type SaveLayoutDocumentCommand struct {
	LayoutID                 string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ContentHTML              string
	ContentText              string
	// LocaleValues are a target room's unit values; a nil entry is invalid.
	LocaleValues []*LayoutUnitValue
	Contributors []string
}

// LayoutUnitValue is a locale's value of the source unit Handle.
type LayoutUnitValue struct {
	Handle string
	Value  string
}

// LayoutDocumentSave is the revision state a layout room save left.
type LayoutDocumentSave struct {
	Locale           string
	DocumentRevision string
	TargetRevision   *string
}

// layoutCollaborationSave is one saved layout room write and the record it
// names.
type layoutCollaborationSave struct {
	LayoutDocumentSave
	record mutation.Audit
}

// layoutRoomWrite is a room save against the locked layout authority.
type layoutRoomWrite struct {
	command         SaveLayoutDocumentCommand
	locale          string
	expected        uuid.UUID
	authority       LayoutAuthority
	canonicalSource *emailutil.LayoutTranslationDocument
	originMemberID  string
	now             time.Time
}

// SaveLayoutDocument persists the exact source or existing target locale
// room.
func (s *InternalService) SaveLayoutDocument(ctx context.Context, command SaveLayoutDocumentCommand) (LayoutDocumentSave, error) {
	roomLocale, err := canonicalEmailLayoutRoomLocale(command.Locale)
	if err != nil {
		return LayoutDocumentSave{}, err
	}
	expected, err := parseEmailLayoutDocumentRevision(command.ExpectedDocumentRevision)
	if err != nil {
		return LayoutDocumentSave{}, err
	}
	write := layoutRoomWrite{command: command, locale: roomLocale, expected: expected, now: dbtime.Now()}
	saved, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[layoutCollaborationSave, Store]{
		Access: emailcontent.CollaborationAccess,
		Change: func(ctx context.Context, store Store) (layoutCollaborationSave, error) {
			if err := lockLayoutRoom(ctx, store, &write); err != nil {
				return layoutCollaborationSave{}, err
			}
			if roomLocale != write.authority.SourceLocale {
				return saveLayoutTarget(ctx, store, write)
			}
			return saveLayoutSource(ctx, store, write)
		},
		Audit: func(saved layoutCollaborationSave) mutation.Audit { return saved.record },
	})
	return saved.LayoutDocumentSave, err
}

// lockLayoutRoom locks the layout authority of a room save, proves the
// expected document revision and the delivery lifecycle, and reads the
// canonical source.
func lockLayoutRoom(ctx context.Context, store Store, write *layoutRoomWrite) error {
	authority, err := loadEmailLayoutDocumentAuthority(ctx, store, write.command.LayoutID, emailcontent.UpdateLock)
	if err != nil {
		return err
	}
	if authority.DocumentRevision != write.expected {
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Email Layout Content Document changed; reload before saving",
			domainerr.CollaborationRevisions{Document: authority.DocumentRevision.String()},
		)
	}
	if err := store.RequireLayoutMutable(ctx, authority.LayoutID); err != nil {
		return err
	}
	write.authority = authority
	source, err := store.CanonicalLayoutSource(ctx, write.command.LayoutID, authority.SourceLocale)
	write.canonicalSource = &source
	return err
}

// requireLayoutContributors locks the room's single origin Member and proves
// the room's contributors current.
func requireLayoutContributors(ctx context.Context, store Store, write *layoutRoomWrite) error {
	originMemberID, err := requireOriginMember(ctx, store, write.command.Contributors)
	if err != nil {
		return err
	}
	write.originMemberID = originMemberID
	return store.RequireCurrentLayoutContributors(ctx, write.command.LayoutID, write.command.Contributors)
}

// saveLayoutSource stores the source room's HTML as the next document
// revision.
func saveLayoutSource(ctx context.Context, store Store, write layoutRoomWrite) (layoutCollaborationSave, error) {
	if len(write.command.LocaleValues) != 0 {
		return layoutCollaborationSave{}, domainerr.InvalidField("locale_values", "source locale uses content_html")
	}
	if write.command.ExpectedTargetRevision != nil {
		return layoutCollaborationSave{}, domainerr.InvalidField("expected_target_revision", "source locale has no target revision")
	}
	if err := requireLayoutContributors(ctx, store, &write); err != nil {
		return layoutCollaborationSave{}, err
	}
	contentHTML := emailutil.NormalizeTemplatePlaceholders(strings.ReplaceAll(write.command.ContentHTML, "\x00", ""))
	contentHTML, err := emailutil.CanonicalizeLayoutSourceMarkers(contentHTML)
	if err != nil {
		return layoutCollaborationSave{}, domainerr.InvalidField("html_content", err.Error())
	}
	if err := emailutil.ValidateLayoutHTMLContentError(contentHTML); err != nil {
		return layoutCollaborationSave{}, domainerr.InvalidField("html_content", err.Error())
	}
	contentText := strings.ReplaceAll(write.command.ContentText, "\x00", "")
	if strings.TrimSpace(contentText) == "" {
		contentText = emailutil.StripHTML(contentHTML)
	}
	var document *emailutil.LayoutTranslationDocument
	if derefString(write.canonicalSource.ContentHTML) != contentHTML || derefString(write.canonicalSource.ContentText) != contentText {
		document = &emailutil.LayoutTranslationDocument{ContentHTML: &contentHTML, ContentText: &contentText}
	}
	advance, err := saveEmailLayoutSource(ctx, store, write.command.LayoutID, write.authority, write.expected, document, write.now)
	if err != nil {
		return layoutCollaborationSave{}, err
	}
	saved := layoutCollaborationSave{
		LayoutDocumentSave: LayoutDocumentSave{Locale: write.locale, DocumentRevision: advance.DocumentRevision.String()},
		record:             mutation.Unchanged(),
	}
	if advance.Changed {
		saved.record = emailLayoutLocaleContentChange(write.originMemberID, write.command.LayoutID, write.locale, emailcontent.LocaleContentOperation(true, false, false, true))
	}
	return saved, nil
}

// saveLayoutTarget stores the target room's unit values over the source.
func saveLayoutTarget(ctx context.Context, store Store, write layoutRoomWrite) (layoutCollaborationSave, error) {
	if err := requireLayoutContributors(ctx, store, &write); err != nil {
		return layoutCollaborationSave{}, err
	}
	entry, err := store.LayoutTranslationEntry(ctx, write.command.LayoutID, write.locale)
	if err != nil {
		return layoutCollaborationSave{}, err
	}
	if entry == nil {
		return layoutCollaborationSave{}, domainerr.Precondition("Email Layout target locale document does not exist")
	}
	documentRevision := write.authority.DocumentRevision.String()
	currentRevision, err := deriveEmailLayoutTargetRevision(documentRevision, entry)
	if err != nil {
		return layoutCollaborationSave{}, err
	}
	if err := translation.ValidateExpectedTargetRevision(write.command.ExpectedTargetRevision, derefString(currentRevision), true); err != nil {
		return layoutCollaborationSave{}, domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Email Layout source or exact target changed since it was loaded; reload before saving",
			domainerr.CollaborationRevisionsOf(translation.WithDocumentRevision(err, documentRevision)),
		)
	}
	contentHTML, contentText, err := compileEmailLayoutTargetValues(derefString(write.canonicalSource.ContentHTML), write.command.LocaleValues)
	if err != nil {
		return layoutCollaborationSave{}, err
	}
	saved := layoutCollaborationSave{record: mutation.Unchanged()}
	updatedAt := entry.UpdatedAt
	if derefString(entry.ContentHTML) != derefString(contentHTML) || derefString(entry.ContentText) != derefString(contentText) {
		updatedAt = translation.NextTargetUpdatedAt(write.now, entry.UpdatedAt)
		if err := store.UpsertLayoutTranslation(
			ctx, write.command.LayoutID, write.locale,
			translation.EntryWrite{ContentHTML: contentHTML, ContentText: contentText, Now: updatedAt},
		); err != nil {
			return layoutCollaborationSave{}, err
		}
		saved.record = emailLayoutLocaleContentChange(write.originMemberID, write.command.LayoutID, write.locale, emailcontent.LocaleContentOperation(false, false, false, true))
	}
	nextRevision, err := deriveEmailLayoutTargetRevision(documentRevision, &emailutil.LayoutTranslationEntry{
		LayoutTranslationDocument: emailutil.LayoutTranslationDocument{ContentHTML: contentHTML, ContentText: contentText, UpdatedAt: updatedAt},
	})
	if err != nil {
		return layoutCollaborationSave{}, err
	}
	saved.LayoutDocumentSave = LayoutDocumentSave{Locale: write.locale, DocumentRevision: documentRevision, TargetRevision: nextRevision}
	return saved, nil
}

// emailLayoutLocaleContentChange is the record of a change to the layout's
// content in locale, credited to memberID.
func emailLayoutLocaleContentChange(memberID, layoutID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditEmailLayoutUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailLayoutLocaleContentAuditRecord(metadata, layoutID, locale, operation)
	})
}

func canonicalEmailLayoutRoomLocale(value string) (string, error) {
	locale := strings.TrimSpace(value)
	if locale == "" {
		return "", domainerr.Required("locale")
	}
	canonical := localization.NormalizeSupportedLocale(locale)
	if value != locale || canonical == nil || *canonical != locale {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return locale, nil
}

func compileEmailLayoutTargetValues(
	canonicalSource string,
	values []*LayoutUnitValue,
) (*string, *string, error) {
	units, err := emailutil.ExtractLayoutContentUnits(canonicalSource)
	if err != nil {
		return nil, nil, domainerr.Precondition("Email Layout source unit markers require backfill before collaboration editing").WithCause(failurecause.ServerFault(err))
	}
	allowed := make(map[string]struct{}, len(units))
	for _, unit := range units {
		allowed[unit.Handle] = struct{}{}
	}
	requested := make(map[string]string, len(values))
	for _, value := range values {
		if value == nil {
			return nil, nil, domainerr.InvalidField("locale_values", "must not contain null values")
		}
		handle := strings.TrimSpace(value.Handle)
		if _, ok := allowed[handle]; !ok {
			return nil, nil, domainerr.InvalidField("locale_values", "contains an unknown Email Layout unit handle")
		}
		if _, duplicate := requested[handle]; duplicate {
			return nil, nil, domainerr.InvalidField("locale_values", "contains a duplicate Email Layout unit handle")
		}
		requested[handle] = strings.ReplaceAll(value.Value, "\x00", "")
	}
	contentHTML, contentText, err := emailutil.ApplyLayoutLocaleValues(canonicalSource, requested)
	if err != nil {
		return nil, nil, domainerr.InvalidField("locale_values", err.Error())
	}
	return contentHTML, contentText, nil
}

// LoadLayoutDocumentQuery reads an Email Layout room in one locale.
type LoadLayoutDocumentQuery struct {
	LayoutID string
	Locale   string
}

// LayoutDocument is an Email Layout room: the source HTML for the source
// locale, or the source units and the target's stored values for a target
// locale.
type LayoutDocument struct {
	ContentHTML      string
	ContentText      string
	SourceLocale     string
	Locale           string
	DocumentRevision string
	TargetRevision   *string
	Units            []LayoutContentUnit
	LocaleValues     []LayoutUnitValue
}

// LayoutContentUnit is one translatable unit of the source HTML.
type LayoutContentUnit struct {
	Handle      string
	Kind        string
	Element     string
	Attribute   string
	Order       uint32
	SourceValue string
}

// LoadLayoutDocument loads the exact locale room. A target room reads
// read-only against the source when its locale row does not exist.
func (s *InternalService) LoadLayoutDocument(ctx context.Context, query LoadLayoutDocumentQuery) (LayoutDocument, error) {
	roomLocale, err := canonicalEmailLayoutRoomLocale(query.Locale)
	if err != nil {
		return LayoutDocument{}, err
	}
	var document LayoutDocument
	err = s.units.ReadLocked(ctx, func(store Store) error {
		authority, err := loadEmailLayoutDocumentAuthority(ctx, store, query.LayoutID, emailcontent.ShareLock)
		if err != nil {
			return err
		}
		source, err := store.CanonicalLayoutSource(ctx, query.LayoutID, authority.SourceLocale)
		if err != nil {
			return err
		}
		document = LayoutDocument{
			SourceLocale: authority.SourceLocale, Locale: roomLocale, DocumentRevision: authority.DocumentRevision.String(),
		}
		if roomLocale == authority.SourceLocale {
			document.ContentHTML, document.ContentText = derefString(source.ContentHTML), derefString(source.ContentText)
			return nil
		}
		return loadLayoutTargetRoom(ctx, store, query.LayoutID, derefString(source.ContentHTML), &document)
	})
	return document, err
}

// loadLayoutTargetRoom reads the source units of a target room and the
// values its locale row stores.
func loadLayoutTargetRoom(ctx context.Context, store Store, layoutID, sourceHTML string, document *LayoutDocument) error {
	entry, err := store.LayoutTranslationEntry(ctx, layoutID, document.Locale)
	if err != nil {
		return err
	}
	sourceUnits, err := emailutil.ExtractLayoutContentUnits(sourceHTML)
	if err != nil {
		return domainerr.Precondition("Email Layout source unit markers require backfill before collaboration editing").WithCause(failurecause.ServerFault(err))
	}
	document.Units = make([]LayoutContentUnit, 0, len(sourceUnits))
	for _, unit := range sourceUnits {
		document.Units = append(document.Units, LayoutContentUnit{
			Handle: unit.Handle, Kind: unit.Kind, Element: unit.Element,
			Attribute: unit.Attribute, Order: uint32(unit.Order), SourceValue: unit.SourceValue,
		})
	}
	if entry != nil && entry.ContentHTML != nil {
		storedValues, err := emailutil.ExtractLayoutStoredLocaleValues(*entry.ContentHTML)
		if err != nil {
			return domainerr.Precondition("Email Layout target unit markers require backfill before collaboration editing").WithCause(failurecause.ServerFault(err))
		}
		document.LocaleValues = make([]LayoutUnitValue, 0, len(storedValues))
		for _, unit := range sourceUnits {
			if value, present := storedValues[unit.Handle]; present {
				document.LocaleValues = append(document.LocaleValues, LayoutUnitValue{Handle: unit.Handle, Value: value})
			}
		}
	}
	document.TargetRevision, err = deriveEmailLayoutTargetRevision(document.DocumentRevision, entry)
	return err
}
