package programevent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ContentBlockMediaHydrator adds request-scoped delivery to already-authorized Block references.
type ContentBlockMediaHydrator interface {
	HydrateAuthorizedProgramEventBlockMediaWithDB(
		context.Context,
		*gorm.DB,
		string,
		uuid.UUID,
		*auth.UserInfo,
		[]*contentv1.ContentBlockMediaItem,
	) ([]*contentv1.ContentBlockMediaItem, error)
}

// CreditMemberSummaries projects Member-owned identity details for Program Event credits.
type CreditMemberSummaries interface {
	LoadCreditMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

// MediaAssets is the narrow File/media lifecycle used by Program Event.
// Program Event owns transaction and binding policy; the adapter owns the
// concrete public-asset implementation and CDN projection.
type MediaAssets interface {
	LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error
	BindReadyAssetForSourceFile(context.Context, *gorm.DB, string, string, string, string, string) (*commonv1.AssetRef, error)
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	ReadyPublicAssetRefForSourceFile(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
	ResolveSingleReadyInlineAssetForSourceFile(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
}

// Members is the Member-owned surface Program Event reads.
type Members interface {
	// PreferredLocale returns the stored preferred locale of the live Member,
	// or nil.
	PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error)
	// LockMembers takes a key-share lock on the Members memberIDs name and
	// returns how many exist.
	LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) (int, error)
}

// CreditArtists reads the Artist-owned projection of a credited artist.
type CreditArtists interface {
	// CreditArtist returns the artist's ID, source-locale name and slug; an
	// empty ID means the artist does not exist.
	CreditArtist(ctx context.Context, db *gorm.DB, artistID string) (id, name string, slug *string, err error)
	// FirstImageFileID returns the File of the artist's first image, or empty.
	FirstImageFileID(ctx context.Context, db *gorm.DB, artistID string) (string, error)
}

// CollaborationPermissionChecker is the minimal ReBAC boundary used by Program Event collaboration.
type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

// Deps are the collaborators of the Program Event, series, type,
// collaboration and AI document use cases; all are required.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	Audit         domainaudit.Appender
	ContentBlocks *contentblock.Store
	// Signals schedules content.updated in the mutation transaction.
	Signals       contentsignal.Publisher
	Media         MediaAssets
	MediaHydrator ContentBlockMediaHydrator
	CreditMembers CreditMemberSummaries
	CreditArtists CreditArtists
	Members       Members
	// Checkpoints re-resolves the contributors a collaboration write is
	// attributed to.
	Checkpoints persistencecheckpoint.ContributorFence
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil,
		"ContentBlocks": d.ContentBlocks == nil, "Signals": d.Signals == nil, "Media": d.Media == nil,
		"MediaHydrator": d.MediaHydrator == nil, "CreditMembers": d.CreditMembers == nil,
		"Checkpoints": d.Checkpoints == nil, "CreditArtists": d.CreditArtists == nil, "Members": d.Members == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("program event dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Program Event use cases of administrators, of the
// collaboration service and of the AI document adapter. Connect and MCP
// adapters translate their requests into its commands and queries.
type Service struct {
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	audit         domainaudit.Appender
	mutations     *mutation.Runner
	contentBlocks *contentblock.Store
	signals       contentsignal.Publisher
	media         MediaAssets
	mediaHydrator ContentBlockMediaHydrator
	creditMembers CreditMemberSummaries
	creditArtists CreditArtists
	members       Members
	checkpoints   persistencecheckpoint.ContributorFence
}

// NewService validates deps and returns the Program Event use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, audit: deps.Audit, mutations: mutations,
		contentBlocks: deps.ContentBlocks, signals: deps.Signals, media: deps.Media,
		mediaHydrator: deps.MediaHydrator, creditMembers: deps.CreditMembers, creditArtists: deps.CreditArtists,
		members: deps.Members, checkpoints: deps.Checkpoints,
	}, nil
}

// RequireExists verifies the Program Event root at cross-domain attachment boundaries.
func RequireExists(ctx context.Context, db *gorm.DB, eventID string) error {
	var event model.ProgramEvent
	if err := db.WithContext(ctx).Select("id").Take(&event, "id = ?", eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("program event", eventID)
		}
		return err
	}
	return nil
}

// resolveInitialSourceLocale is the source locale of a new document: the
// Member's preferred locale, then acceptLanguage, then the site default.
func (s *Service) resolveInitialSourceLocale(ctx context.Context, acceptLanguage string) string {
	db := s.db
	if user := auth.GetUser(ctx); user != nil && user.Authenticated {
		if preferredLocale, err := s.members.PreferredLocale(ctx, db, user.MemberID.String()); err == nil && preferredLocale != nil {
			if locale := localization.NormalizeSupportedLocale(*preferredLocale); locale != nil {
				return *locale
			}
		} else if err != nil {
			slog.ErrorContext(ctx, "Member preferred locale lookup failed; inferring Program Event source locale from the request", "domain", "programevent", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if settings, err := translation.LoadRuntimeSettings(ctx, db); err == nil {
		if locale := localization.NormalizeSupportedLocale(settings.DefaultLocale); locale != nil {
			return *locale
		}
	} else {
		slog.ErrorContext(ctx, "Translation runtime settings lookup failed; using the default Program Event source locale", "domain", "programevent", "operation", "resolve_initial_source_locale", failurecause.Attr(err))
	}
	return translation.DefaultLocale
}

func lockTranslationEntityRootWithDB(ctx context.Context, db *gorm.DB, entityType, entityID string) error {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok {
		return domainerr.InvalidField("entity_type", "unsupported translation entity type")
	}
	var root struct {
		ID string `gorm:"column:id"`
	}
	result := db.WithContext(ctx).
		Table(definition.RootTable).
		Select("id").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", entityID).
		Take(&root)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound(entityType, entityID)
	}
	return result.Error
}

func (s *Service) requireDocumentContributors(ctx context.Context, tx *gorm.DB, contributors []string) error {
	if len(contributors) == 0 {
		return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires contributors")
	}
	for index, contributor := range contributors {
		if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
			return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires canonical Member UUIDs")
		}
		if index > 0 && contributors[index-1] >= contributor {
			return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires sorted unique Member UUIDs")
		}
	}
	locked, err := s.members.LockMembers(ctx, tx, contributors)
	if err != nil {
		return fmt.Errorf("lock Program Event contributor Members: %w", err)
	}
	if locked != len(contributors) {
		return domainerr.InvalidField("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}
