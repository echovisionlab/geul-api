// Package postpublicpostgres is the PostgreSQL public.Store: the post
// tables a public Post read locks and lists, and the localization, share
// link and map place runtimes it uses, bound to one gorm transaction.
package postpublicpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/post"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	"github.com/echovisionlab/geul-api/internal/post/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// MapPlaceProjector supplies the public projection for a Post's referenced
// map place. Reference Catalog owns that projection and composition injects
// its adapter here.
type MapPlaceProjector interface {
	Basic(*model.MapPlace) *openv1.MapPlaceBasic
}

// ShareLinkValidator keeps the shared ShareLink proof implementation outside
// Post while Post retains the decision about when a private read may use it.
type ShareLinkValidator interface {
	ValidateShareLinkForEntity(
		context.Context,
		*gorm.DB,
		string,
		string,
		managev1.ShareLinkEntityType,
		string,
	) (*model.ShareLink, error)
}

// LocalizationService resolves a Post's or Series' displayed locale through
// db.
type LocalizationService interface {
	ResolveSelectionWithPolicy(
		context.Context, *gorm.DB, string, string, string, bool,
	) (public.LocalizedContentSelection, error)
	ResolveSelectionsWithPolicy(
		context.Context, *gorm.DB, string, []string, string, bool,
	) (map[string]public.LocalizedContentSelection, error)
	ResolveOgConsistency(
		context.Context, *gorm.DB, string, string, string, public.LocalizedContentSelection,
	) (public.LocalizedContentSelection, error)
}

// Ports are the shared runtimes the Store uses.
type Ports struct {
	ContentBlocks *contentblock.Store
	Localization  LocalizationService
	MapPlaces     MapPlaceProjector
	ShareLinks    ShareLinkValidator
	// CDNDomain builds OG asset URLs; empty serves relative paths.
	CDNDomain string
}

func (d Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": d.ContentBlocks == nil, "Localization": d.Localization == nil,
		"MapPlaces": d.MapPlaces == nil, "ShareLinks": d.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public post store dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the unit of work of the public Post reads.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[public.Store], error) {
	if db == nil {
		return nil, errors.New("public post store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) public.Store {
		return &store{Store: postpostgres.NewStore(tx, postpostgres.Ports{ContentBlocks: ports.ContentBlocks}), tx: tx, ports: ports}
	}), nil
}

// Localizer is the public.Localizer over db, outside any read snapshot.
type Localizer struct {
	db      *gorm.DB
	service LocalizationService
}

// NewLocalizer returns the Localizer of service over db.
func NewLocalizer(db *gorm.DB, service LocalizationService) Localizer {
	return Localizer{db: db, service: service}
}

func (l Localizer) ResolveSelection(ctx context.Context, entityType, entityID, acceptLanguage string, requireNonBlankTitle bool) (public.LocalizedContentSelection, error) {
	return l.service.ResolveSelectionWithPolicy(ctx, l.db, entityType, entityID, acceptLanguage, requireNonBlankTitle)
}

func (l Localizer) ResolveSelections(ctx context.Context, entityType string, entityIDs []string, acceptLanguage string, requireNonBlankTitle bool) (map[string]public.LocalizedContentSelection, error) {
	return l.service.ResolveSelectionsWithPolicy(ctx, l.db, entityType, entityIDs, acceptLanguage, requireNonBlankTitle)
}

// store reuses the Post Store's comment, content, source locale and author
// reads, which a public read shares with the manage reads.
type store struct {
	post.Store
	tx    *gorm.DB
	ports Ports
}

var _ public.Store = (*store)(nil)

type shareLinkNotFoundError struct{ error }

func (err shareLinkNotFoundError) Is(target error) bool { return target == public.ErrShareLinkNotFound }
func (err shareLinkNotFoundError) Unwrap() error        { return err.error }

func (s *store) ShareLockPost(ctx context.Context, slugOrID string, byID bool) (public.PublicPost, error) {
	query := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		Preload("MapPlace")
	var row model.Post
	var err error
	if byID {
		err = query.First(&row, "id = ?", slugOrID).Error
	} else {
		err = query.First(&row, "slug = ?", slugOrID).Error
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return public.PublicPost{}, notFoundError{err}
		}
		return public.PublicPost{}, err
	}
	locked := public.PublicPost{Post: postpostgres.PostFromRow(row)}
	if row.MapPlaceID != nil {
		locked.MapPlace = s.ports.MapPlaces.Basic(row.MapPlace)
	}
	return locked, nil
}

type notFoundError struct{ error }

func (err notFoundError) Is(target error) bool { return target == post.ErrPostNotFound }
func (err notFoundError) Unwrap() error        { return err.error }

func (s *store) ShareLink(ctx context.Context, token, password, postID string) (*mediaasset.ContentDownloadShareLinkWitness, error) {
	link, err := s.ports.ShareLinks.ValidateShareLinkForEntity(ctx, s.tx, token, password, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST, postID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shareLinkNotFoundError{err}
	}
	if err != nil {
		return nil, err
	}
	return mediaassetpostgres.ContentDownloadShareLinkWitnessFromModel(link), nil
}

func (s *store) ResolveLocalization(ctx context.Context, postID, acceptLanguage string) (public.LocalizedContentSelection, error) {
	return s.ports.Localization.ResolveSelectionWithPolicy(ctx, s.tx, "post", postID, acceptLanguage, true)
}

func (s *store) ResolveOgConsistency(ctx context.Context, postID string, selection public.LocalizedContentSelection) (public.LocalizedContentSelection, error) {
	return s.ports.Localization.ResolveOgConsistency(ctx, s.tx, s.ports.CDNDomain, "post", postID, selection)
}

func (s *store) ReadyOgAsset(ctx context.Context, candidates ...*string) (*commonv1.AssetRef, error) {
	for _, candidate := range candidates {
		if candidate == nil || strings.TrimSpace(*candidate) == "" {
			continue
		}
		var asset model.PublicAsset
		if err := s.tx.WithContext(ctx).
			Where("id = ? AND status = ?", strings.TrimSpace(*candidate), model.PublicAssetStatusReady).
			Take(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		return mediaasset.ReferenceResultProto(mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), s.ports.CDNDomain).AssetRef(mediaassetpostgres.Asset(asset)))
	}
	return nil, nil
}
