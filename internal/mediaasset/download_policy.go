package mediaasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
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
	BlockID       string
	ReferencePath string
	TrackID       string
	FileID        string
	Extension     string
	MimeType      string
	FileSize      int64
	FileName      *string
	Audience      FileDownloadAudience
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

func ContentBlockDownloadPolicyKey(blockID, referencePath string) string {
	return strings.TrimSpace(blockID) + "\x00" + strings.TrimSpace(referencePath)
}

func LoadContentBlockDownloadSources(
	ctx context.Context,
	store DownloadStore,
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
	blockIDs = sliceutil.SortedUniqueTrimmed(blockIDs)
	if len(blockIDs) == 0 {
		return map[string]FileDownloadSource{}, nil
	}
	rows, err := store.ContentSources(ctx, blockIDs)
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
	store DownloadStore,
	trackIDs []string,
) (map[string]FileDownloadSource, error) {
	trackIDs = sliceutil.SortedUniqueTrimmed(trackIDs)
	if len(trackIDs) == 0 {
		return map[string]FileDownloadSource{}, nil
	}
	rows, err := store.TrackSources(ctx, trackIDs)
	if err != nil {
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
	store DownloadStore,
	spiceDB DownloadAuthorizer,
	source FileDownloadSource,
	user *auth.UserInfo,
) (bool, error) {
	access, err := evaluateFileDownloadAccessBatchAt(
		ctx,
		store,
		spiceDB,
		map[string]FileDownloadSource{source.PolicyKey: source},
		user,
		time.Now().UTC(),
	)
	if err != nil {
		return false, err
	}
	return access[source.PolicyKey], nil
}

func EvaluateFileDownloadAccessBatch(
	ctx context.Context,
	store DownloadStore,
	spiceDB DownloadAuthorizer,
	sources map[string]FileDownloadSource,
	user *auth.UserInfo,
) (map[string]bool, error) {
	return evaluateFileDownloadAccessBatchAt(ctx, store, spiceDB, sources, user, time.Now().UTC())
}

func RestrictedFileDownloadSegmentPresence(
	ctx context.Context,
	store DownloadStore,
	sources map[string]FileDownloadSource,
) (map[string]bool, error) {
	result := make(map[string]bool, len(sources))
	associations, err := store.Associations(ctx, sources, true)
	if err != nil {
		return nil, err
	}
	for _, association := range associations {
		result[association.PolicyKey] = true
	}
	return result, nil
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
	ID          string                `json:"id"`
	SegmentType string                `json:"segment_type"`
	ArchivedAt  *time.Time            `json:"archived_at,omitempty"`
	UpdatedAt   *time.Time            `json:"updated_at,omitempty"`
	Config      DownloadSegmentConfig `json:"config"`
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
	store DownloadStore,
	sources map[string]FileDownloadSource,
) (map[string]string, error) {
	associations, err := store.Associations(ctx, sources, false)
	if err != nil {
		return nil, err
	}
	segmentIDsByPolicy := make(map[string][]string, len(sources))
	allSegmentIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		segmentIDsByPolicy[association.PolicyKey] = append(segmentIDsByPolicy[association.PolicyKey], association.AudienceSegmentID)
		allSegmentIDs = append(allSegmentIDs, association.AudienceSegmentID)
	}
	allSegmentIDs = sliceutil.SortedUniqueTrimmed(allSegmentIDs)
	segmentsByID := make(map[string]DownloadSegment, len(allSegmentIDs))
	if len(allSegmentIDs) > 0 {
		segments, err := store.Segments(ctx, allSegmentIDs, false)
		if err != nil {
			return nil, err
		}
		for _, segment := range segments {
			segment.Config.MemberTagIDs = sliceutil.SortedUniqueTrimmed(segment.Config.MemberTagIDs)
			segment.Config.AccountRoles = sliceutil.SortedUniqueTrimmed(segment.Config.AccountRoles)
			segment.Config.ExcludeMemberIDs = sliceutil.SortedUniqueTrimmed(segment.Config.ExcludeMemberIDs)
			segmentsByID[segment.ID] = segment
		}
	}

	result := make(map[string]string, len(sources))
	for key, source := range sources {
		segmentIDs := sliceutil.SortedUniqueTrimmed(segmentIDsByPolicy[key])
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
				ID: segment.ID, SegmentType: string(segment.SegmentType), ArchivedAt: segment.ArchivedAt,
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
	store DownloadStore,
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
	facts, found, err := store.MemberFacts(
		ctx, user.MemberID.String(), user.IdentityID.String(),
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
			var err error
			record.TagIDs, err = store.ViewerTagIDs(ctx, facts.MemberID)
			if err != nil {
				return "", err
			}
			record.TagIDs = sliceutil.SortedUniqueTrimmed(record.TagIDs)
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
	store DownloadStore,
	sources map[string]FileDownloadSource,
) error {
	associations, err := store.Associations(ctx, sources, false)
	if err != nil {
		return err
	}
	segmentIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		segmentIDs = append(segmentIDs, association.AudienceSegmentID)
	}
	segmentIDs = sliceutil.SortedUniqueTrimmed(segmentIDs)
	if len(segmentIDs) == 0 {
		return nil
	}
	return store.LockSegments(ctx, segmentIDs)
}

func LockFileDownloadViewerFacts(
	ctx context.Context,
	store DownloadStore,
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
	return store.LockViewer(ctx, user, restricted)
}

func evaluateFileDownloadAccessBatchAt(
	ctx context.Context,
	store DownloadStore,
	spiceDB DownloadAuthorizer,
	sources map[string]FileDownloadSource,
	user *auth.UserInfo,
	_ time.Time,
) (map[string]bool, error) {
	hasAuthenticatedUser := user != nil &&
		user.Authenticated &&
		!user.Banned &&
		strings.TrimSpace(user.MemberID.String()) != "" &&
		strings.TrimSpace(user.IdentityID.String()) != ""
	activeMember := DownloadMemberFacts{}
	activeMemberFound := false
	if hasAuthenticatedUser {
		var err error
		activeMember, activeMemberFound, err = store.MemberFacts(
			ctx, user.MemberID.String(), user.IdentityID.String(),
		)
		if err != nil {
			return nil, err
		}
	}
	result, restrictedPolicyKeys, err := fileDownloadAudienceDecisions(sources, hasAuthenticatedUser && activeMemberFound)
	if err != nil {
		return nil, err
	}
	if len(restrictedPolicyKeys) == 0 {
		return result, nil
	}
	restrictedSources := make(map[string]FileDownloadSource, len(restrictedPolicyKeys))
	for _, key := range restrictedPolicyKeys {
		restrictedSources[key] = sources[key]
	}
	associations, err := store.Associations(ctx, restrictedSources, false)
	if err != nil {
		return nil, err
	}
	segmentIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		segmentIDs = append(segmentIDs, association.AudienceSegmentID)
	}
	segmentIDs = sliceutil.SortedUniqueTrimmed(segmentIDs)
	if len(segmentIDs) == 0 {
		return result, nil
	}
	segments, err := store.Segments(ctx, segmentIDs, true)
	if err != nil {
		return nil, err
	}
	segmentsByID := make(map[string]DownloadSegment, len(segments))
	for _, segment := range segments {
		segmentsByID[segment.ID] = segment
	}
	memberFacts := activeMember
	existingTagIDs, memberTagIDs, err := loadRestrictedFileDownloadTagFacts(
		ctx,
		store,
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

// fileDownloadAudienceDecisions decides each source whose audience needs no
// segment lookup and returns the restricted policy keys left to match, in
// order. eligible is whether the viewer is an active authenticated Member.
func fileDownloadAudienceDecisions(sources map[string]FileDownloadSource, eligible bool) (map[string]bool, []string, error) {
	result := make(map[string]bool, len(sources))
	restrictedPolicyKeys := make([]string, 0, len(sources))
	for policyKey, source := range sources {
		if !source.Audience.Valid() {
			return nil, nil, errors.New("file has invalid download audience")
		}
		switch source.Audience {
		case FileDownloadAudienceDisabled:
			result[policyKey] = false
		case FileDownloadAudiencePublic:
			result[policyKey] = true
		case FileDownloadAudienceAuthenticated:
			result[policyKey] = eligible
		case FileDownloadAudienceRestricted:
			if !eligible {
				result[policyKey] = false
			} else {
				restrictedPolicyKeys = append(restrictedPolicyKeys, policyKey)
			}
		}
	}
	sort.Strings(restrictedPolicyKeys)
	return result, restrictedPolicyKeys, nil
}

// loadRestrictedFileDownloadSegments loads the live segments a restricted
// policy names, with their configuration.
func loadRestrictedFileDownloadTagFacts(
	ctx context.Context,
	store DownloadStore,
	memberID string,
	segments []DownloadSegment,
) (map[string]struct{}, map[string]struct{}, error) {
	referencedTagIDs := make([]string, 0)
	for _, segment := range segments {
		if strings.TrimSpace(string(segment.SegmentType)) !=
			string(DownloadMemberTags) {
			continue
		}
		tagIDs, valid := validUUIDSet(segment.Config.MemberTagIDs)
		if valid {
			referencedTagIDs = append(referencedTagIDs, tagIDs...)
		}
	}
	referencedTagIDs = sliceutil.SortedUniqueTrimmed(referencedTagIDs)
	existingTagIDs := make(map[string]struct{}, len(referencedTagIDs))
	memberTagIDs := make(map[string]struct{}, len(referencedTagIDs))
	if len(referencedTagIDs) == 0 {
		return existingTagIDs, memberTagIDs, nil
	}

	rows, err := store.TagFacts(ctx, memberID, referencedTagIDs)
	if err != nil {
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
	member DownloadMemberFacts,
	segment *DownloadSegment,
	existingTagIDs map[string]struct{},
	memberTagIDs map[string]struct{},
	permissions map[policyv1.RoleID]bool,
) bool {
	if segment == nil {
		return false
	}

	config := segment.Config
	switch strings.TrimSpace(string(segment.SegmentType)) {
	case string(DownloadAllMembers):
		return true
	case string(DownloadMemberTags):
		return restrictedFileDownloadMemberMatchesTagSegment(config, existingTagIDs, memberTagIDs)
	case string(DownloadMembersByFilter):
		return restrictedFileDownloadMemberMatchesFilterSegment(member, config, permissions)
	default:
		return false
	}
}

func restrictedFileDownloadMemberMatchesTagSegment(
	config DownloadSegmentConfig,
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
	member DownloadMemberFacts,
	config DownloadSegmentConfig,
	permissions map[policyv1.RoleID]bool,
) bool {
	if len(config.MemberTagIDs) > 0 {
		return false
	}
	requested, err := normalizeFileDownloadAccountPermissions(config.AccountRoles)
	if err != nil {
		// resolveRestrictedFileDownloadPermissions already failed the request
		// for these stored permissions; never match on them.
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
	spiceDB DownloadAuthorizer,
	user *auth.UserInfo,
	segments []DownloadSegment,
) (map[policyv1.RoleID]bool, error) {
	requested := make(map[policyv1.RoleID]struct{})
	for _, segment := range segments {
		if strings.TrimSpace(string(segment.SegmentType)) != string(DownloadMembersByFilter) {
			continue
		}
		permissions, err := normalizeFileDownloadAccountPermissions(segment.Config.AccountRoles)
		if err != nil {
			// The permissions come from a stored segment, not from this request.
			return nil, failurecause.WithAttrs(
				fmt.Errorf("read stored File download segment: %w", err),
				slog.String("segment_id", segment.ID),
			)
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
		return nil, domainerr.DependencyUnavailable("SpiceDB")
	}
	for permission := range requested {
		can, err := restrictedDownloadPlatformCan(permission)
		if err != nil {
			return nil, err
		}
		decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, user), can)
		if err != nil {
			return nil, domainerr.AuthenticationRequired().WithCause(err)
		}
		allowed, checkErr := spiceDB.Can(ctx, decision)
		if checkErr != nil {
			return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(checkErr)
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
			return nil, fmt.Errorf("invalid account permission %q", value)
		}
		if _, exists := seen[role]; exists {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	return roles, nil
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
