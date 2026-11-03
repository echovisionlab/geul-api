package mediaasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/criteriamatch"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type FileDownloadAccess string

const (
	FileDownloadAccessDisabled      FileDownloadAccess = "disabled"
	FileDownloadAccessPublic        FileDownloadAccess = "public"
	FileDownloadAccessAuthenticated FileDownloadAccess = "authenticated"
	FileDownloadAccessRestricted    FileDownloadAccess = "restricted"
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
	Access        FileDownloadAccess
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
		if !source.Access.Valid() {
			return nil, errors.New("content Block attachment has invalid download access")
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
		if !source.Access.Valid() {
			return nil, errors.New("track has invalid download access")
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

func RestrictedFileDownloadCriteriaPresence(
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
	Kind          FileDownloadPolicyKind             `json:"kind"`
	BlockID       string                             `json:"block_id,omitempty"`
	ReferencePath string                             `json:"reference_path,omitempty"`
	TrackID       string                             `json:"track_id,omitempty"`
	FileID        string                             `json:"file_id"`
	Extension     string                             `json:"extension"`
	MimeType      string                             `json:"mime_type"`
	FileSize      int64                              `json:"file_size"`
	FileName      *string                            `json:"file_name,omitempty"`
	Access        FileDownloadAccess                 `json:"access"`
	CriteriaSet   []fileDownloadPolicyCriteriaRecord `json:"member_criteria"`
}

type fileDownloadPolicyCriteriaRecord struct {
	ID         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	ArchivedAt *time.Time             `json:"archived_at,omitempty"`
	UpdatedAt  *time.Time             `json:"updated_at,omitempty"`
	Config     DownloadCriteriaConfig `json:"config"`
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
// metadata, access, and normalized Criteria policy used by a download
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
	criteriaIDsByPolicy := make(map[string][]string, len(sources))
	allCriteriaIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		criteriaIDsByPolicy[association.PolicyKey] = append(criteriaIDsByPolicy[association.PolicyKey], association.MemberCriteriaID)
		allCriteriaIDs = append(allCriteriaIDs, association.MemberCriteriaID)
	}
	allCriteriaIDs = sliceutil.SortedUniqueTrimmed(allCriteriaIDs)
	criteriaSetByID := make(map[string]DownloadCriteria, len(allCriteriaIDs))
	if len(allCriteriaIDs) > 0 {
		criteriaSet, err := store.CriteriaSet(ctx, allCriteriaIDs, false)
		if err != nil {
			return nil, err
		}
		for _, criteria := range criteriaSet {
			criteria.Config.MemberTagIDs = sliceutil.SortedUniqueTrimmed(criteria.Config.MemberTagIDs)
			criteria.Config.AccountRoles = sliceutil.SortedUniqueTrimmed(criteria.Config.AccountRoles)
			criteria.Config.ExcludeMemberIDs = sliceutil.SortedUniqueTrimmed(criteria.Config.ExcludeMemberIDs)
			criteriaSetByID[criteria.ID] = criteria
		}
	}

	result := make(map[string]string, len(sources))
	for key, source := range sources {
		criteriaIDs := sliceutil.SortedUniqueTrimmed(criteriaIDsByPolicy[key])
		record := fileDownloadPolicyFingerprint{
			Kind: source.PolicyKind, BlockID: source.BlockID, ReferencePath: source.ReferencePath,
			TrackID: source.TrackID, FileID: source.FileID, Extension: source.Extension,
			MimeType: source.MimeType, FileSize: source.FileSize, FileName: source.FileName,
			Access: source.Access, CriteriaSet: make([]fileDownloadPolicyCriteriaRecord, 0, len(criteriaIDs)),
		}
		for _, criteriaID := range criteriaIDs {
			criteria, exists := criteriaSetByID[criteriaID]
			if !exists {
				record.CriteriaSet = append(record.CriteriaSet, fileDownloadPolicyCriteriaRecord{ID: criteriaID})
				continue
			}
			record.CriteriaSet = append(record.CriteriaSet, fileDownloadPolicyCriteriaRecord{
				ID: criteria.ID, Kind: string(criteria.Kind), ArchivedAt: criteria.ArchivedAt,
				UpdatedAt: criteria.UpdatedAt, Config: criteria.Config,
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
		if source.Access == FileDownloadAccessAuthenticated || source.Access == FileDownloadAccessRestricted {
			requiresMember = true
		}
		if source.Access == FileDownloadAccessRestricted {
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

// LockFileDownloadPolicyCriteriaSet establishes the shared policy lock order used
// before an owning root and exact relation are locked for final signing.
func LockFileDownloadPolicyCriteriaSet(
	ctx context.Context,
	store DownloadStore,
	sources map[string]FileDownloadSource,
) error {
	associations, err := store.Associations(ctx, sources, false)
	if err != nil {
		return err
	}
	criteriaIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		criteriaIDs = append(criteriaIDs, association.MemberCriteriaID)
	}
	criteriaIDs = sliceutil.SortedUniqueTrimmed(criteriaIDs)
	if len(criteriaIDs) == 0 {
		return nil
	}
	return store.LockCriteriaSet(ctx, criteriaIDs)
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
		if source.Access == FileDownloadAccessAuthenticated || source.Access == FileDownloadAccessRestricted {
			requiresMember = true
		}
		if source.Access == FileDownloadAccessRestricted {
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
	result, restrictedPolicyKeys, err := fileDownloadAccessDecisions(sources, hasAuthenticatedUser && activeMemberFound)
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
	criteriaIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		criteriaIDs = append(criteriaIDs, association.MemberCriteriaID)
	}
	criteriaIDs = sliceutil.SortedUniqueTrimmed(criteriaIDs)
	if len(criteriaIDs) == 0 {
		return result, nil
	}
	criteriaSet, err := store.CriteriaSet(ctx, criteriaIDs, true)
	if err != nil {
		return nil, err
	}
	criteriaSetByID := make(map[string]DownloadCriteria, len(criteriaSet))
	for _, criteria := range criteriaSet {
		criteriaSetByID[criteria.ID] = criteria
	}
	memberFacts := activeMember
	existingTagIDs, memberTagIDs, err := loadRestrictedFileDownloadTagFacts(
		ctx,
		store,
		memberFacts.MemberID,
		criteriaSet,
	)
	if err != nil {
		return nil, err
	}
	permissions, err := resolveRestrictedFileDownloadPermissions(ctx, spiceDB, user, criteriaSet)
	if err != nil {
		return nil, err
	}
	matchesByCriteriaID := make(map[string]bool, len(criteriaIDs))
	for _, criteriaID := range criteriaIDs {
		criteria, exists := criteriaSetByID[criteriaID]
		if !exists {
			continue
		}
		matchesByCriteriaID[criteriaID] = criteriamatch.Matches(
			criteriamatch.Member{ID: memberFacts.MemberID, CreatedAt: memberFacts.CreatedAt},
			matchCriteria(criteria),
			criteriamatch.Facts{ExistingTagIDs: existingTagIDs, MemberTagIDs: memberTagIDs, Roles: permissions},
		)
	}
	for _, association := range associations {
		if matchesByCriteriaID[association.MemberCriteriaID] {
			result[association.PolicyKey] = true
		}
	}
	return result, nil
}

// fileDownloadAccessDecisions decides each source whose access needs no
// criteria lookup and returns the restricted policy keys left to match, in
// order. eligible is whether the viewer is an active authenticated Member.
func fileDownloadAccessDecisions(sources map[string]FileDownloadSource, eligible bool) (map[string]bool, []string, error) {
	result := make(map[string]bool, len(sources))
	restrictedPolicyKeys := make([]string, 0, len(sources))
	for policyKey, source := range sources {
		if !source.Access.Valid() {
			return nil, nil, errors.New("file has invalid download access")
		}
		switch source.Access {
		case FileDownloadAccessDisabled:
			result[policyKey] = false
		case FileDownloadAccessPublic:
			result[policyKey] = true
		case FileDownloadAccessAuthenticated:
			result[policyKey] = eligible
		case FileDownloadAccessRestricted:
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

func (access FileDownloadAccess) Valid() bool {
	switch access {
	case FileDownloadAccessDisabled,
		FileDownloadAccessPublic,
		FileDownloadAccessAuthenticated,
		FileDownloadAccessRestricted:
		return true
	default:
		return false
	}
}

// matchCriteria is the criteria as the shared Member criteria rule reads it.
func matchCriteria(criteria DownloadCriteria) criteriamatch.Criteria {
	return criteriamatch.Criteria{ID: criteria.ID, Kind: criteria.Kind, Config: criteria.Config}
}

func matchCriteriaSet(criteriaSet []DownloadCriteria) []criteriamatch.Criteria {
	result := make([]criteriamatch.Criteria, len(criteriaSet))
	for i, criteria := range criteriaSet {
		result[i] = matchCriteria(criteria)
	}
	return result
}

// loadRestrictedFileDownloadTagFacts reports which Member tags the restricted
// criteria select still exist and which of them the Member has.
func loadRestrictedFileDownloadTagFacts(
	ctx context.Context,
	store DownloadStore,
	memberID string,
	criteriaSet []DownloadCriteria,
) (map[string]struct{}, map[string]struct{}, error) {
	referencedTagIDs := criteriamatch.ReferencedTagIDs(matchCriteriaSet(criteriaSet))
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

// resolveRestrictedFileDownloadPermissions checks the platform roles the
// restricted filter criteria test.
func resolveRestrictedFileDownloadPermissions(
	ctx context.Context,
	spiceDB DownloadAuthorizer,
	user *auth.UserInfo,
	criteriaSet []DownloadCriteria,
) (map[policyv1.RoleID]bool, error) {
	requested, err := criteriamatch.RequestedRoles(matchCriteriaSet(criteriaSet))
	if err != nil {
		// The roles come from a stored criteria, not from this request.
		return nil, err
	}
	resolved := make(map[policyv1.RoleID]bool, len(requested))
	if len(requested) == 0 {
		return resolved, nil
	}
	if spiceDB == nil || user == nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB")
	}
	for _, role := range requested {
		can, err := criteriamatch.PlatformCan(role)
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
		resolved[role] = allowed
	}
	return resolved, nil
}
