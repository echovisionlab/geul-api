package emailauthoring

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/google/uuid"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
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
	LocaleValues             []*intrav1.EmailLayoutLocaleValue
	Contributors             []string
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
	authority       emailLayoutDocumentAuthority
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
	saved, err := mutation.Run(ctx, s.mutations, mutation.Spec[layoutCollaborationSave]{
		Access: emailcontent.CollaborationAccess,
		Change: func(ctx context.Context, tx *gorm.DB) (layoutCollaborationSave, error) {
			if err := s.lockLayoutRoom(ctx, tx, &write); err != nil {
				return layoutCollaborationSave{}, err
			}
			if roomLocale != write.authority.SourceLocale {
				return s.saveLayoutTarget(ctx, tx, write)
			}
			return s.saveLayoutSource(ctx, tx, write)
		},
		Audit: func(saved layoutCollaborationSave) mutation.Audit { return saved.record },
	})
	return saved.LayoutDocumentSave, err
}

// lockLayoutRoom locks the layout authority of a room save, proves the
// expected document revision and the delivery lifecycle, and reads the
// canonical source.
func (s *InternalService) lockLayoutRoom(ctx context.Context, tx *gorm.DB, write *layoutRoomWrite) error {
	authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, write.command.LayoutID, clause.LockingStrengthUpdate)
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
	if err := s.references.RequireLayoutMutable(ctx, tx, authority.LayoutID); err != nil {
		return err
	}
	write.authority = authority
	write.canonicalSource, err = emailutil.LoadCanonicalLayoutTranslationDocument(ctx, emailpostgres.Bind(tx), write.command.LayoutID, authority.SourceLocale)
	return err
}

// requireLayoutContributors locks the room's single origin Member and proves
// the room's contributors current.
func (s *InternalService) requireLayoutContributors(ctx context.Context, tx *gorm.DB, write *layoutRoomWrite) error {
	originMemberID, err := s.requireOriginMember(ctx, tx, write.command.Contributors)
	if err != nil {
		return err
	}
	write.originMemberID = originMemberID
	return s.checkpoints.RequireCurrentContributors(
		ctx, tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_LAYOUT, write.command.LayoutID, write.command.Contributors,
	)
}

// saveLayoutSource stores the source room's HTML as the next document
// revision.
func (s *InternalService) saveLayoutSource(ctx context.Context, tx *gorm.DB, write layoutRoomWrite) (layoutCollaborationSave, error) {
	if len(write.command.LocaleValues) != 0 {
		return layoutCollaborationSave{}, domainerr.InvalidField("locale_values", "source locale uses content_html")
	}
	if write.command.ExpectedTargetRevision != nil {
		return layoutCollaborationSave{}, domainerr.InvalidField("expected_target_revision", "source locale has no target revision")
	}
	if err := s.requireLayoutContributors(ctx, tx, &write); err != nil {
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
	advance, err := saveEmailLayoutSource(ctx, tx, s.contentBlocks, s.references, write.command.LayoutID, write.authority, write.expected, document, write.now)
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
func (s *InternalService) saveLayoutTarget(ctx context.Context, tx *gorm.DB, write layoutRoomWrite) (layoutCollaborationSave, error) {
	if err := s.requireLayoutContributors(ctx, tx, &write); err != nil {
		return layoutCollaborationSave{}, err
	}
	entry, err := emailutil.LoadLayoutTranslationEntry(ctx, emailpostgres.Bind(tx), write.command.LayoutID, write.locale)
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
		if err := emailutil.UpsertLayoutTranslationEntry(
			ctx, emailpostgres.Bind(tx), write.command.LayoutID, write.locale,
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
	values []*intrav1.EmailLayoutLocaleValue,
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
	Units            []*intrav1.EmailLayoutContentUnit
	LocaleValues     []*intrav1.EmailLayoutLocaleValue
}

// LoadLayoutDocument loads the exact locale room. A target room reads
// read-only against the source when its locale row does not exist.
func (s *InternalService) LoadLayoutDocument(ctx context.Context, query LoadLayoutDocumentQuery) (LayoutDocument, error) {
	roomLocale, err := canonicalEmailLayoutRoomLocale(query.Locale)
	if err != nil {
		return LayoutDocument{}, err
	}
	var document LayoutDocument
	err = mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		authority, err := loadEmailLayoutDocumentAuthority(ctx, tx, query.LayoutID, clause.LockingStrengthShare)
		if err != nil {
			return err
		}
		source, err := emailutil.LoadCanonicalLayoutTranslationDocument(ctx, emailpostgres.Bind(tx), query.LayoutID, authority.SourceLocale)
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
		return loadLayoutTargetRoom(ctx, tx, query.LayoutID, derefString(source.ContentHTML), &document)
	})
	return document, err
}

// loadLayoutTargetRoom reads the source units of a target room and the
// values its locale row stores.
func loadLayoutTargetRoom(ctx context.Context, tx *gorm.DB, layoutID, sourceHTML string, document *LayoutDocument) error {
	entry, err := emailutil.LoadLayoutTranslationEntry(ctx, emailpostgres.Bind(tx), layoutID, document.Locale)
	if err != nil {
		return err
	}
	sourceUnits, err := emailutil.ExtractLayoutContentUnits(sourceHTML)
	if err != nil {
		return domainerr.Precondition("Email Layout source unit markers require backfill before collaboration editing").WithCause(failurecause.ServerFault(err))
	}
	document.Units = make([]*intrav1.EmailLayoutContentUnit, 0, len(sourceUnits))
	for _, unit := range sourceUnits {
		document.Units = append(document.Units, &intrav1.EmailLayoutContentUnit{
			Handle: unit.Handle, Kind: unit.Kind, Element: unit.Element,
			Attribute: unit.Attribute, Order: uint32(unit.Order), SourceValue: unit.SourceValue,
		})
	}
	if entry != nil && entry.ContentHTML != nil {
		storedValues, err := emailutil.ExtractLayoutStoredLocaleValues(*entry.ContentHTML)
		if err != nil {
			return domainerr.Precondition("Email Layout target unit markers require backfill before collaboration editing").WithCause(failurecause.ServerFault(err))
		}
		document.LocaleValues = make([]*intrav1.EmailLayoutLocaleValue, 0, len(storedValues))
		for _, unit := range sourceUnits {
			if value, present := storedValues[unit.Handle]; present {
				document.LocaleValues = append(document.LocaleValues, &intrav1.EmailLayoutLocaleValue{Handle: unit.Handle, Value: value})
			}
		}
	}
	document.TargetRevision, err = deriveEmailLayoutTargetRevision(document.DocumentRevision, entry)
	return err
}
