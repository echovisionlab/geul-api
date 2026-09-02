package mediaasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FileDownloadAudience string

const (
	FileDownloadAudienceDisabled      FileDownloadAudience = "disabled"
	FileDownloadAudiencePublic        FileDownloadAudience = "public"
	FileDownloadAudienceAuthenticated FileDownloadAudience = "authenticated"
	FileDownloadAudienceRestricted    FileDownloadAudience = "restricted"
)

type FileDownloadSource struct {
	PolicyKey     string
	PolicyKind    FileDownloadPolicyKind
	BlockID       string               `gorm:"column:block_id"`
	ReferencePath string               `gorm:"column:reference_path"`
	TrackID       string               `gorm:"column:track_id"`
	FileID        string               `gorm:"column:id"`
	Extension     string               `gorm:"column:extension"`
	MimeType      string               `gorm:"column:mime_type"`
	FileSize      int64                `gorm:"column:file_size"`
	FileName      *string              `gorm:"column:file_name"`
	Audience      FileDownloadAudience `gorm:"column:download_audience"`
}

type FileDownloadPolicyKind string

const (
	FileDownloadPolicyContentBlock FileDownloadPolicyKind = "content_block"
	FileDownloadPolicyTrack        FileDownloadPolicyKind = "track"
)

type ContentBlockDownloadSelector struct {
	BlockID       string
	ReferencePath string
}

type ContentDownloadOwnerAccessMode string

const (
	ContentDownloadOwnerAccessPublic             ContentDownloadOwnerAccessMode = "public"
	ContentDownloadOwnerAccessAuthenticatedDraft ContentDownloadOwnerAccessMode = "authenticated_draft"
	ContentDownloadOwnerAccessShare              ContentDownloadOwnerAccessMode = "share"
)

type ContentDownloadShareLinkWitness struct {
	ID         string
	EntityType string
	EntityID   string
	ExpiresAt  *time.Time
}

type ContentDownloadOwnerAuthorization struct {
	ResourceType string
	ResourceID   string
	Status       string
	DocumentID   string
	Mode         ContentDownloadOwnerAccessMode
	IdentityID   string
	MemberID     string
	ShareLink    *ContentDownloadShareLinkWitness
}

type contentDownloadOwnerAuthorizationContextKey struct{}

func WithContentDownloadOwnerAuthorization(
	ctx context.Context,
	authorization ContentDownloadOwnerAuthorization,
) context.Context {
	return context.WithValue(ctx, contentDownloadOwnerAuthorizationContextKey{}, authorization)
}

func ContentDownloadOwnerAuthorizationFromContext(ctx context.Context) (ContentDownloadOwnerAuthorization, bool) {
	authorization, ok := ctx.Value(contentDownloadOwnerAuthorizationContextKey{}).(ContentDownloadOwnerAuthorization)
	return authorization, ok
}

func ContentDownloadShareLinkWitnessFromModel(link *model.ShareLink) *ContentDownloadShareLinkWitness {
	if link == nil {
		return nil
	}
	return &ContentDownloadShareLinkWitness{
		ID: link.ID, EntityType: link.EntityType, EntityID: link.EntityID,
		ExpiresAt: link.ExpiresAt,
	}
}

func ContentBlockDownloadPolicyKey(blockID, referencePath string) string {
	return strings.TrimSpace(blockID) + "\x00" + strings.TrimSpace(referencePath)
}

type restrictedFileDownloadMemberFacts struct {
	MemberID   string    `gorm:"column:member_id"`
	IdentityID string    `gorm:"column:identity_id"`
	Onboarded  bool      `gorm:"column:onboarded"`
	CreatedAt  time.Time `gorm:"column:member_created_at"`
}

type restrictedFileDownloadTagFact struct {
	TagID          string  `gorm:"column:tag_id"`
	MappedMemberID *string `gorm:"column:mapped_member_id"`
}

// SegmentConfigLoader hydrates the Audience-owned segment configuration used
// by File download policy without coupling File/media authority to Audience.
type SegmentConfigLoader interface {
	LoadSegmentConfigs(context.Context, *gorm.DB, []*model.AudienceSegment) error
}

func LoadContentBlockDownloadSources(
	ctx context.Context,
	db *gorm.DB,
	selectors []ContentBlockDownloadSelector,
) (map[string]FileDownloadSource, error) {
	requested := make(map[string]struct{}, len(selectors))
	blockIDs := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		blockID := strings.TrimSpace(selector.BlockID)
		referencePath := strings.TrimSpace(selector.ReferencePath)
		if blockID == "" || referencePath == "" {
			continue
		}
		requested[ContentBlockDownloadPolicyKey(blockID, referencePath)] = struct{}{}
		blockIDs = append(blockIDs, blockID)
	}
	blockIDs = sortedUniqueNonEmptyStrings(blockIDs)
	if len(blockIDs) == 0 {
		return map[string]FileDownloadSource{}, nil
	}
	var rows []FileDownloadSource
	err := db.WithContext(ctx).Raw(`
		SELECT attachment.block_id, attachment.reference_path,
		       file.id, file.extension, file.mime_type, file.file_size, file.file_name,
		       attachment.download_audience
		FROM content_block_attachment AS attachment
		JOIN content_block AS block ON block.id = attachment.block_id
		JOIN file ON file.id = attachment.file_id
		WHERE attachment.block_id IN ?
		  AND block.kind = 'file'
		  AND attachment.reference_path = 'file'
		  AND attachment.selector_kind = 'active'
		  AND file.delete_requested_at IS NULL
	`, blockIDs).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	sources := make(map[string]FileDownloadSource, len(rows))
	for _, source := range rows {
		key := ContentBlockDownloadPolicyKey(source.BlockID, source.ReferencePath)
		if _, ok := requested[key]; !ok {
			continue
		}
		if !source.Audience.Valid() {
			return nil, errors.New("content Block attachment has invalid download audience")
		}
		source.PolicyKey = key
		source.PolicyKind = FileDownloadPolicyContentBlock
		sources[key] = source
	}
	return sources, nil
}

func LoadTrackDownloadSources(
	ctx context.Context,
	db *gorm.DB,
	trackIDs []string,
) (map[string]FileDownloadSource, error) {
	trackIDs = sortedUniqueNonEmptyStrings(trackIDs)
	if len(trackIDs) == 0 {
		return map[string]FileDownloadSource{}, nil
	}
	var rows []FileDownloadSource
	if err := db.WithContext(ctx).Raw(`
		SELECT track.id AS track_id, file.id, file.extension, file.mime_type,
		       file.file_size, file.file_name, track.download_audience
		FROM track
		JOIN file ON file.id = track.audio_original_file_id
		WHERE track.id IN ? AND file.delete_requested_at IS NULL
	`, trackIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	sources := make(map[string]FileDownloadSource, len(rows))
	for _, source := range rows {
		if !source.Audience.Valid() {
			return nil, errors.New("track has invalid download audience")
		}
		source.PolicyKey = source.TrackID
		source.PolicyKind = FileDownloadPolicyTrack
		sources[source.TrackID] = source
	}
	return sources, nil
}

func EvaluateFileDownloadAccess(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	source FileDownloadSource,
	user *auth.UserInfo,
	loaders ...SegmentConfigLoader,
) (bool, error) {
	access, err := evaluateFileDownloadAccessBatchAt(
		ctx,
		db,
		spiceDB,
		map[string]FileDownloadSource{source.PolicyKey: source},
		user,
		time.Now().UTC(),
		loaders...,
	)
	if err != nil {
		return false, err
	}
	return access[source.PolicyKey], nil
}

func EvaluateFileDownloadAccessBatch(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	sources map[string]FileDownloadSource,
	user *auth.UserInfo,
	loaders ...SegmentConfigLoader,
) (map[string]bool, error) {
	return evaluateFileDownloadAccessBatchAt(ctx, db, spiceDB, sources, user, time.Now().UTC(), loaders...)
}

func RestrictedFileDownloadSegmentPresence(
	ctx context.Context,
	db *gorm.DB,
	sources map[string]FileDownloadSource,
) (map[string]bool, error) {
	result := make(map[string]bool, len(sources))
	associations, err := loadFileDownloadAudienceAssociations(ctx, db, sources, true)
	if err != nil {
		return nil, err
	}
	for _, association := range associations {
		result[association.PolicyKey] = true
	}
	return result, nil
}

type fileDownloadAudienceAssociation struct {
	PolicyKey         string
	AudienceSegmentID string
}

type fileDownloadPolicyFingerprint struct {
	Kind          FileDownloadPolicyKind            `json:"kind"`
	BlockID       string                            `json:"block_id,omitempty"`
	ReferencePath string                            `json:"reference_path,omitempty"`
	TrackID       string                            `json:"track_id,omitempty"`
	FileID        string                            `json:"file_id"`
	Extension     string                            `json:"extension"`
	MimeType      string                            `json:"mime_type"`
	FileSize      int64                             `json:"file_size"`
	FileName      *string                           `json:"file_name,omitempty"`
	Audience      FileDownloadAudience              `json:"audience"`
	Segments      []fileDownloadPolicySegmentRecord `json:"segments"`
}

type fileDownloadPolicySegmentRecord struct {
	ID          string                      `json:"id"`
	SegmentType string                      `json:"segment_type"`
	ArchivedAt  *time.Time                  `json:"archived_at,omitempty"`
	UpdatedAt   *time.Time                  `json:"updated_at,omitempty"`
	Config      model.AudienceSegmentConfig `json:"config"`
}

type fileDownloadViewerFingerprint struct {
	Authenticated bool      `json:"authenticated"`
	MemberFound   bool      `json:"member_found"`
	MemberID      string    `json:"member_id,omitempty"`
	IdentityID    string    `json:"identity_id,omitempty"`
	Onboarded     bool      `json:"onboarded"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	TagIDs        []string  `json:"tag_ids"`
}

// LoadFileDownloadPolicyFingerprints captures the exact relation, File
// metadata, audience, and normalized Segment policy used by a download
// decision. Callers can evaluate external entitlements without a relation lock,
// then lock and compare this value before local signing.
func LoadFileDownloadPolicyFingerprints(
	ctx context.Context,
	db *gorm.DB,
	sources map[string]FileDownloadSource,
	loader SegmentConfigLoader,
) (map[string]string, error) {
	associations, err := loadFileDownloadAudienceAssociations(ctx, db, sources, false)
	if err != nil {
		return nil, err
	}
	segmentIDsByPolicy := make(map[string][]string, len(sources))
	allSegmentIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		segmentIDsByPolicy[association.PolicyKey] = append(segmentIDsByPolicy[association.PolicyKey], association.AudienceSegmentID)
		allSegmentIDs = append(allSegmentIDs, association.AudienceSegmentID)
	}
	allSegmentIDs = sortedUniqueNonEmptyStrings(allSegmentIDs)
	segmentsByID := make(map[string]model.AudienceSegment, len(allSegmentIDs))
	if len(allSegmentIDs) > 0 {
		var segments []model.AudienceSegment
		if err := db.WithContext(ctx).Where("id IN ?", allSegmentIDs).Find(&segments).Error; err != nil {
			return nil, err
		}
		if loader == nil {
			return nil, errs.DependencyUnavailable("Audience segment configuration")
		}
		pointers := make([]*model.AudienceSegment, 0, len(segments))
		for i := range segments {
			pointers = append(pointers, &segments[i])
		}
		if err := loader.LoadSegmentConfigs(ctx, db, pointers); err != nil {
			return nil, err
		}
		for _, segment := range segments {
			segment.Config.MemberTagIDs = sortedUniqueNonEmptyStrings(segment.Config.MemberTagIDs)
			segment.Config.AccountRoles = sortedUniqueNonEmptyStrings(segment.Config.AccountRoles)
			segment.Config.ExcludeMemberIDs = sortedUniqueNonEmptyStrings(segment.Config.ExcludeMemberIDs)
			segmentsByID[segment.ID] = segment
		}
	}

	result := make(map[string]string, len(sources))
	for key, source := range sources {
		segmentIDs := sortedUniqueNonEmptyStrings(segmentIDsByPolicy[key])
		record := fileDownloadPolicyFingerprint{
			Kind: source.PolicyKind, BlockID: source.BlockID, ReferencePath: source.ReferencePath,
			TrackID: source.TrackID, FileID: source.FileID, Extension: source.Extension,
			MimeType: source.MimeType, FileSize: source.FileSize, FileName: source.FileName,
			Audience: source.Audience, Segments: make([]fileDownloadPolicySegmentRecord, 0, len(segmentIDs)),
		}
		for _, segmentID := range segmentIDs {
			segment, exists := segmentsByID[segmentID]
			if !exists {
				record.Segments = append(record.Segments, fileDownloadPolicySegmentRecord{ID: segmentID})
				continue
			}
			record.Segments = append(record.Segments, fileDownloadPolicySegmentRecord{
				ID: segment.ID, SegmentType: segment.SegmentType, ArchivedAt: segment.ArchivedAt,
				UpdatedAt: segment.UpdatedAt, Config: segment.Config,
			})
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(encoded)
		result[key] = hex.EncodeToString(digest[:])
	}
	return result, nil
}

// LoadFileDownloadViewerFingerprint captures the database-owned viewer facts
// used by restricted policy evaluation. External role entitlements remain
// evaluated outside the relation lock; this value is rechecked in the short
// signing transaction so Member/Identity or tag revocation cannot precede a
// newly issued stale URL.
func LoadFileDownloadViewerFingerprint(
	ctx context.Context,
	db *gorm.DB,
	sources map[string]FileDownloadSource,
	user *auth.UserInfo,
) (string, error) {
	requiresMember := false
	restricted := false
	for _, source := range sources {
		if source.Audience == FileDownloadAudienceAuthenticated || source.Audience == FileDownloadAudienceRestricted {
			requiresMember = true
		}
		if source.Audience == FileDownloadAudienceRestricted {
			restricted = true
		}
	}
	record := fileDownloadViewerFingerprint{TagIDs: []string{}}
	if !requiresMember || user == nil || !user.Authenticated || user.Banned ||
		strings.TrimSpace(user.MemberID.String()) == "" || strings.TrimSpace(user.IdentityID.String()) == "" {
		encoded, err := json.Marshal(record)
		if err != nil {
			return "", err
		}
		digest := sha256.Sum256(encoded)
		return hex.EncodeToString(digest[:]), nil
	}
	record.Authenticated = true
	facts, found, err := loadRestrictedFileDownloadMemberFacts(
		ctx, db, user.MemberID.String(), user.IdentityID.String(),
	)
	if err != nil {
		return "", err
	}
	record.MemberFound = found
	if found {
		record.MemberID = facts.MemberID
		record.IdentityID = facts.IdentityID
		record.Onboarded = facts.Onboarded
		record.CreatedAt = facts.CreatedAt
		if restricted {
			if err := db.WithContext(ctx).
				Table("user_tag_mapping AS mapping").
				Joins("JOIN user_tag AS tag ON tag.id = mapping.tag_id").
				Where("mapping.member_id = ?", facts.MemberID).
				Pluck("mapping.tag_id", &record.TagIDs).Error; err != nil {
				return "", err
			}
			record.TagIDs = sortedUniqueNonEmptyStrings(record.TagIDs)
		}
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// LockFileDownloadPolicySegments establishes the shared policy lock order used
// before an owning root and exact relation are locked for final signing.
func LockFileDownloadPolicySegments(
	ctx context.Context,
	db *gorm.DB,
	sources map[string]FileDownloadSource,
) error {
	associations, err := loadFileDownloadAudienceAssociations(ctx, db, sources, false)
	if err != nil {
		return err
	}
	segmentIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		segmentIDs = append(segmentIDs, association.AudienceSegmentID)
	}
	segmentIDs = sortedUniqueNonEmptyStrings(segmentIDs)
	if len(segmentIDs) == 0 {
		return nil
	}
	var locked []struct {
		ID string `gorm:"column:id"`
	}
	return db.WithContext(ctx).
		Table("audience_segment").
		Select("id").
		Where("id IN ?", segmentIDs).
		Order("id").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Find(&locked).Error
}

// LockFileDownloadViewerFacts locks the database-owned authenticated viewer
// witness after Segment locks and before the exact relation lock. The boolean
// reports whether the exact Member and Identity pair remains active.
func LockFileDownloadViewerFacts(
	ctx context.Context,
	db *gorm.DB,
	sources map[string]FileDownloadSource,
	user *auth.UserInfo,
) (bool, error) {
	requiresMember := false
	restricted := false
	for _, source := range sources {
		if source.Audience == FileDownloadAudienceAuthenticated || source.Audience == FileDownloadAudienceRestricted {
			requiresMember = true
		}
		if source.Audience == FileDownloadAudienceRestricted {
			restricted = true
		}
	}
	if !requiresMember || user == nil || !user.Authenticated {
		return true, nil
	}
	if strings.TrimSpace(user.MemberID.String()) == "" {
		return false, nil
	}
	if strings.TrimSpace(user.IdentityID.String()) == "" || user.Banned {
		return false, nil
	}
	active := false
	if db.Dialector.Name() == "sqlite" {
		_, found, err := loadRestrictedFileDownloadMemberFacts(
			ctx, db, user.MemberID.String(), user.IdentityID.String(),
		)
		if err != nil {
			return false, err
		}
		active = found
	} else {
		var err error
		active, err = identitystate.LockActivePrincipal(ctx, db, user)
		if err != nil {
			return false, err
		}
	}
	if !active || !restricted {
		return active, nil
	}
	var rows []struct {
		MemberID string `gorm:"column:member_id"`
	}
	err := db.WithContext(ctx).
		Table("user_tag_mapping AS mapping").
		Select("mapping.member_id").
		Joins("JOIN user_tag AS tag ON tag.id = mapping.tag_id").
		Where("mapping.member_id = ?", user.MemberID.String()).
		Order("mapping.tag_id").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Find(&rows).Error
	return err == nil, err
}

func loadFileDownloadAudienceAssociations(
	ctx context.Context,
	db *gorm.DB,
	sources map[string]FileDownloadSource,
	activeOnly bool,
) ([]fileDownloadAudienceAssociation, error) {
	contentSources := make(map[string]FileDownloadSource)
	trackIDs := make([]string, 0)
	blockIDs := make([]string, 0)
	for key, source := range sources {
		switch source.PolicyKind {
		case FileDownloadPolicyContentBlock:
			contentSources[key] = source
			blockIDs = append(blockIDs, source.BlockID)
		case FileDownloadPolicyTrack:
			trackIDs = append(trackIDs, source.TrackID)
		}
	}
	associations := make([]fileDownloadAudienceAssociation, 0)
	if blockIDs = sortedUniqueNonEmptyStrings(blockIDs); len(blockIDs) > 0 {
		var rows []struct {
			BlockID           string `gorm:"column:block_id"`
			ReferencePath     string `gorm:"column:reference_path"`
			AudienceSegmentID string `gorm:"column:audience_segment_id"`
		}
		query := db.WithContext(ctx).
			Table("content_block_attachment_download_audience_segment AS association").
			Select("association.block_id, association.reference_path, association.audience_segment_id").
			Where("association.block_id IN ?", blockIDs)
		if activeOnly {
			query = query.Joins("JOIN audience_segment AS segment ON segment.id = association.audience_segment_id").
				Where("segment.archived_at IS NULL")
		}
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			key := ContentBlockDownloadPolicyKey(row.BlockID, row.ReferencePath)
			if _, ok := contentSources[key]; ok {
				associations = append(associations, fileDownloadAudienceAssociation{PolicyKey: key, AudienceSegmentID: row.AudienceSegmentID})
			}
		}
	}
	if trackIDs = sortedUniqueNonEmptyStrings(trackIDs); len(trackIDs) > 0 {
		var rows []model.TrackDownloadAudienceSegment
		query := db.WithContext(ctx).
			Table("track_download_audience_segment AS association").
			Select("association.track_id, association.audience_segment_id").
			Where("association.track_id IN ?", trackIDs)
		if activeOnly {
			query = query.Joins("JOIN audience_segment AS segment ON segment.id = association.audience_segment_id").
				Where("segment.archived_at IS NULL")
		}
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			associations = append(associations, fileDownloadAudienceAssociation{PolicyKey: row.TrackID, AudienceSegmentID: row.AudienceSegmentID})
		}
	}
	return associations, nil
}

func evaluateFileDownloadAccessBatchAt(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	sources map[string]FileDownloadSource,
	user *auth.UserInfo,
	_ time.Time,
	loaders ...SegmentConfigLoader,
) (map[string]bool, error) {
	result := make(map[string]bool, len(sources))
	restrictedPolicyKeys := make([]string, 0, len(sources))
	hasAuthenticatedUser := user != nil &&
		user.Authenticated &&
		!user.Banned &&
		strings.TrimSpace(user.MemberID.String()) != "" &&
		strings.TrimSpace(user.IdentityID.String()) != ""
	activeMember := restrictedFileDownloadMemberFacts{}
	activeMemberFound := false
	if hasAuthenticatedUser {
		var err error
		activeMember, activeMemberFound, err = loadRestrictedFileDownloadMemberFacts(
			ctx, db, user.MemberID.String(), user.IdentityID.String(),
		)
		if err != nil {
			return nil, err
		}
	}
	for policyKey, source := range sources {
		if !source.Audience.Valid() {
			return nil, errors.New("file has invalid download audience")
		}
		switch source.Audience {
		case FileDownloadAudienceDisabled:
			result[policyKey] = false
		case FileDownloadAudiencePublic:
			result[policyKey] = true
		case FileDownloadAudienceAuthenticated:
			result[policyKey] = hasAuthenticatedUser && activeMemberFound
		case FileDownloadAudienceRestricted:
			if !hasAuthenticatedUser || !activeMemberFound {
				result[policyKey] = false
			} else {
				restrictedPolicyKeys = append(restrictedPolicyKeys, policyKey)
			}
		}
	}
	if len(restrictedPolicyKeys) == 0 {
		return result, nil
	}
	sort.Strings(restrictedPolicyKeys)
	restrictedSources := make(map[string]FileDownloadSource, len(restrictedPolicyKeys))
	for _, key := range restrictedPolicyKeys {
		restrictedSources[key] = sources[key]
	}
	associations, err := loadFileDownloadAudienceAssociations(ctx, db, restrictedSources, false)
	if err != nil {
		return nil, err
	}
	segmentIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		segmentIDs = append(segmentIDs, association.AudienceSegmentID)
	}
	segmentIDs = sortedUniqueNonEmptyStrings(segmentIDs)
	if len(segmentIDs) == 0 {
		return result, nil
	}
	var segments []model.AudienceSegment
	if err := db.WithContext(ctx).
		Where("id IN ? AND archived_at IS NULL", segmentIDs).
		Find(&segments).Error; err != nil {
		return nil, err
	}
	segmentPointers := make([]*model.AudienceSegment, 0, len(segments))
	for i := range segments {
		segmentPointers = append(segmentPointers, &segments[i])
	}
	if len(loaders) != 1 || loaders[0] == nil {
		return nil, errs.DependencyUnavailable("Audience segment configuration")
	}
	if err := loaders[0].LoadSegmentConfigs(ctx, db, segmentPointers); err != nil {
		return nil, err
	}
	segmentsByID := make(map[string]model.AudienceSegment, len(segments))
	for _, segment := range segments {
		segmentsByID[segment.ID] = segment
	}
	memberFacts := activeMember
	existingTagIDs, memberTagIDs, err := loadRestrictedFileDownloadTagFacts(
		ctx,
		db,
		memberFacts.MemberID,
		segments,
	)
	if err != nil {
		return nil, err
	}
	permissions, err := resolveRestrictedFileDownloadPermissions(ctx, spiceDB, user, segments)
	if err != nil {
		return nil, err
	}
	matchesBySegmentID := make(map[string]bool, len(segmentIDs))
	for _, segmentID := range segmentIDs {
		segment, exists := segmentsByID[segmentID]
		if !exists {
			continue
		}
		matchesBySegmentID[segmentID] = restrictedFileDownloadMemberMatchesSegment(
			memberFacts,
			&segment,
			existingTagIDs,
			memberTagIDs,
			permissions,
		)
	}
	for _, association := range associations {
		if matchesBySegmentID[association.AudienceSegmentID] {
			result[association.PolicyKey] = true
		}
	}
	return result, nil
}

func loadRestrictedFileDownloadMemberFacts(
	ctx context.Context,
	db *gorm.DB,
	memberID string,
	identityID string,
) (restrictedFileDownloadMemberFacts, bool, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return restrictedFileDownloadMemberFacts{}, false, nil
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return restrictedFileDownloadMemberFacts{}, false, nil
	}

	query := db.WithContext(ctx).
		Table("member AS member").
		Joins(`JOIN kratos.identities AS identity
			ON identity.id = member.account_identity_id`).
		Where("member.id = ?", memberID).
		Where("member.account_identity_id = ?", identityID).
		Where("identity.id = ?", identityID).
		Where("member.deleted_at IS NULL").
		Where("member.onboarded = ?", true).
		Where("identity.state = ?", auth.KratosStateActive)
	if db.Dialector.Name() == "sqlite" {
		query = query.Select(`
			member.id AS member_id,
			identity.id AS identity_id,
			member.onboarded AS onboarded,
			member.created_at AS member_created_at`)
		query = query.Where("identity.external_id = ?", memberID)
		query = query.Where(
			"(json_extract(identity.metadata_admin, '$.banned') IS NULL OR json_extract(identity.metadata_admin, '$.banned') = 0)",
		)
	} else {
		query = query.Select(`
			member.id AS member_id,
			identity.id::text AS identity_id,
			member.onboarded AS onboarded,
			member.created_at AS member_created_at`)
		query = query.Where("identity.external_id = ?", memberID)
		query = query.Where(
			"(identity.metadata_admin->>'banned' IS NULL OR identity.metadata_admin->>'banned' = 'false')",
		)
	}
	var facts restrictedFileDownloadMemberFacts
	if err := query.Take(&facts).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return restrictedFileDownloadMemberFacts{}, false, nil
		}
		return restrictedFileDownloadMemberFacts{}, false, err
	}
	return facts, true, nil
}

func loadRestrictedFileDownloadTagFacts(
	ctx context.Context,
	db *gorm.DB,
	memberID string,
	segments []model.AudienceSegment,
) (map[string]struct{}, map[string]struct{}, error) {
	referencedTagIDs := make([]string, 0)
	for _, segment := range segments {
		if strings.TrimSpace(segment.SegmentType) !=
			managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS.String() {
			continue
		}
		tagIDs, valid := validUUIDSet(segment.Config.MemberTagIDs)
		if valid {
			referencedTagIDs = append(referencedTagIDs, tagIDs...)
		}
	}
	referencedTagIDs = sortedUniqueNonEmptyStrings(referencedTagIDs)
	existingTagIDs := make(map[string]struct{}, len(referencedTagIDs))
	memberTagIDs := make(map[string]struct{}, len(referencedTagIDs))
	if len(referencedTagIDs) == 0 {
		return existingTagIDs, memberTagIDs, nil
	}

	var rows []restrictedFileDownloadTagFact
	if err := db.WithContext(ctx).
		Table("user_tag AS tag").
		Select(`
			tag.id AS tag_id,
			mapping.member_id AS mapped_member_id`).
		Joins(`LEFT JOIN user_tag_mapping AS mapping
			ON mapping.tag_id = tag.id AND mapping.member_id = ?`, memberID).
		Where("tag.id IN ?", referencedTagIDs).
		Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		existingTagIDs[row.TagID] = struct{}{}
		if row.MappedMemberID != nil {
			memberTagIDs[row.TagID] = struct{}{}
		}
	}
	return existingTagIDs, memberTagIDs, nil
}

func restrictedFileDownloadMemberMatchesSegment(
	member restrictedFileDownloadMemberFacts,
	segment *model.AudienceSegment,
	existingTagIDs map[string]struct{},
	memberTagIDs map[string]struct{},
	permissions map[policyv1.RoleID]bool,
) bool {
	if segment == nil {
		return false
	}

	config := segment.Config
	switch strings.TrimSpace(segment.SegmentType) {
	case managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS.String():
		return true
	case managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS.String():
		return restrictedFileDownloadMemberMatchesTagSegment(config, existingTagIDs, memberTagIDs)
	case managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String():
		return restrictedFileDownloadMemberMatchesFilterSegment(member, config, permissions)
	default:
		return false
	}
}

func restrictedFileDownloadMemberMatchesTagSegment(
	config model.AudienceSegmentConfig,
	existingTagIDs map[string]struct{},
	memberTagIDs map[string]struct{},
) bool {
	if len(config.AccountRoles) > 0 || config.CreatedAfter != nil || config.CreatedBefore != nil || len(config.ExcludeMemberIDs) > 0 {
		return false
	}
	tagIDs, valid := validUUIDSet(config.MemberTagIDs)
	if !valid || len(tagIDs) == 0 {
		return false
	}
	for _, tagID := range tagIDs {
		if _, exists := existingTagIDs[tagID]; !exists {
			return false
		}
	}
	for _, tagID := range tagIDs {
		if _, exists := memberTagIDs[tagID]; exists {
			return true
		}
	}
	return false
}

func restrictedFileDownloadMemberMatchesFilterSegment(
	member restrictedFileDownloadMemberFacts,
	config model.AudienceSegmentConfig,
	permissions map[policyv1.RoleID]bool,
) bool {
	if len(config.MemberTagIDs) > 0 {
		return false
	}
	requested, err := normalizeFileDownloadAccountPermissions(config.AccountRoles)
	if err != nil {
		return false
	}
	if len(requested) > 0 {
		allowed := false
		for _, permission := range requested {
			if permissions[permission] {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	if config.CreatedAfter != nil && member.CreatedAt.Before(*config.CreatedAfter) {
		return false
	}
	if config.CreatedBefore != nil && member.CreatedAt.After(*config.CreatedBefore) {
		return false
	}
	excludedMemberIDs, valid := validUUIDSet(config.ExcludeMemberIDs)
	return valid && !slices.Contains(excludedMemberIDs, member.MemberID)
}

func resolveRestrictedFileDownloadPermissions(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	user *auth.UserInfo,
	segments []model.AudienceSegment,
) (map[policyv1.RoleID]bool, error) {
	requested := make(map[policyv1.RoleID]struct{})
	for _, segment := range segments {
		if strings.TrimSpace(segment.SegmentType) != managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String() {
			continue
		}
		permissions, err := normalizeFileDownloadAccountPermissions(segment.Config.AccountRoles)
		if err != nil {
			return nil, err
		}
		for _, permission := range permissions {
			requested[permission] = struct{}{}
		}
	}
	resolved := make(map[policyv1.RoleID]bool, len(requested))
	if len(requested) == 0 {
		return resolved, nil
	}
	if spiceDB == nil || user == nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	for permission := range requested {
		can, err := restrictedDownloadPlatformCan(permission)
		if err != nil {
			return nil, errs.Internal(err)
		}
		decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, user), can)
		if err != nil {
			return nil, errs.AuthenticationRequired()
		}
		allowed, checkErr := spiceDB.Can(ctx, decision)
		if checkErr != nil {
			return nil, errs.DependencyUnavailable("SpiceDB")
		}
		resolved[permission] = allowed
	}
	return resolved, nil
}

func restrictedDownloadPlatformCan(role policyv1.RoleID) (policyv1.Can, error) {
	switch {
	case role == policyv1.Role.Admin():
		return policyv1.Platform.IsAdmin()
	case role == policyv1.Role.Author():
		return policyv1.Platform.IsAuthor()
	case role == policyv1.Role.User():
		return policyv1.Platform.IsUser()
	default:
		return policyv1.Can{}, fmt.Errorf("unsupported File download account role %q", role.ID())
	}
}

func validUUIDSet(values []string) ([]string, bool) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, rawValue := range values {
		value := strings.TrimSpace(rawValue)
		if _, err := uuid.Parse(value); err != nil {
			return nil, false
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, true
}

func normalizeFileDownloadAccountPermissions(values []string) ([]policyv1.RoleID, error) {
	seen := make(map[policyv1.RoleID]struct{}, len(values))
	roles := make([]policyv1.RoleID, 0, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(strings.ToLower(value))
		if normalized == "" {
			continue
		}
		role, ok := policyv1.Role.Parse(normalized)
		if !ok {
			return nil, errs.InvalidArgumentMsg(fmt.Sprintf("invalid account permission: %s", value))
		}
		if _, exists := seen[role]; exists {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	return roles, nil
}

func sortedUniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, rawValue := range values {
		value := strings.TrimSpace(rawValue)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (audience FileDownloadAudience) Valid() bool {
	switch audience {
	case FileDownloadAudienceDisabled,
		FileDownloadAudiencePublic,
		FileDownloadAudienceAuthenticated,
		FileDownloadAudienceRestricted:
		return true
	default:
		return false
	}
}
