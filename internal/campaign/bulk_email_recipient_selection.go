package campaign

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

type bulkEmailRecipientCandidateQuery struct {
	SQL  string
	Args structured.Values
}

type bulkEmailRecipientSelection struct {
	Mode                          string
	RequireNewsletterSubscription bool
	MemberTagIDs                  []string
	Filters                       *bulkEmailRecipientFilters
}

type bulkEmailRecipientFilters struct {
	// AccountRoles are generated SpiceDB platform permission IDs. They are a
	// product filter request, not a database role projection or authority.
	AccountRoles       []string
	AccountIdentityIDs []string
	CreatedAfter       *time.Time
	CreatedBefore      *time.Time
	ExcludedMemberIDs  []string
}

const (
	BulkEmailContextAccountCurrent         = "account_current"
	BulkEmailContextNewsletterSubscription = "newsletter_subscription"
)

type accountRecipientCandidateOptions struct {
	ContextKind                   string
	RequireNewsletterSubscription bool
	MemberTagIDs                  []string
	Filters                       *bulkEmailRecipientFilters
}

func recipientSelectionFromAudienceSegment(
	segment *model.AudienceSegment,
) (*bulkEmailRecipientSelection, error) {
	if segment == nil {
		return &bulkEmailRecipientSelection{
			Mode: CampaignDeliveryTargetModeAllUsers,
		}, nil
	}

	switch strings.TrimSpace(segment.SegmentType) {
	case managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS.String():
		return &bulkEmailRecipientSelection{
			Mode: CampaignDeliveryTargetModeAllUsers,
		}, nil
	case managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS.String():
		return &bulkEmailRecipientSelection{
			Mode:         CampaignDeliveryTargetModeUserTags,
			MemberTagIDs: segment.Config.MemberTagIDs,
		}, nil
	case managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String():
		return &bulkEmailRecipientSelection{
			Mode: CampaignDeliveryTargetModeUsersByFilter,
			Filters: &bulkEmailRecipientFilters{
				AccountRoles:      segment.Config.AccountRoles,
				CreatedAfter:      segment.Config.CreatedAfter,
				CreatedBefore:     segment.Config.CreatedBefore,
				ExcludedMemberIDs: segment.Config.ExcludeMemberIDs,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown segment type: %s", segment.SegmentType)
	}
}

// CountBulkEmailRecipientsForAudienceSegment counts the current audience
// segment through the delivery-owned recipient selection query.
func CountBulkEmailRecipientsForAudienceSegment(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, segment *model.AudienceSegment) (int64, error) {
	if segment != nil {
		switch segment.SegmentType {
		case managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS.String():
			if len(segment.Config.MemberTagIDs) == 0 {
				return 0, nil
			}
		}
	}

	selection, err := recipientSelectionFromAudienceSegment(segment)
	if err != nil {
		return 0, err
	}
	return countBulkEmailRecipients(ctx, db, spiceDB, selection)
}

func countBulkEmailRecipients(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	selection *bulkEmailRecipientSelection,
) (int64, error) {
	if err := resolveBulkEmailRecipientSelectionPermissions(ctx, spiceDB, selection); err != nil {
		return 0, err
	}
	candidates, err := buildBulkEmailRecipientCandidates(selection)
	if err != nil {
		return 0, err
	}

	var count int64
	if err := db.WithContext(ctx).
		Raw(bulkEmailRecipientCountSQL(candidates.SQL), candidates.Args...).
		Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func buildBulkEmailRecipientCandidates(
	selection *bulkEmailRecipientSelection,
) (bulkEmailRecipientCandidateQuery, error) {
	if selection == nil {
		return bulkEmailRecipientCandidateQuery{}, fmt.Errorf(
			"bulk email recipient selection is required",
		)
	}

	switch selection.Mode {
	case CampaignDeliveryTargetModeAllUsers:
		return buildAccountRecipientCandidates(accountRecipientOptionsFromSelection(selection))
	case CampaignDeliveryTargetModeUserTags:
		if len(selection.MemberTagIDs) == 0 {
			return bulkEmailRecipientCandidateQuery{}, fmt.Errorf("member_tag_ids required for MEMBER_TAGS query")
		}
		options := accountRecipientOptionsFromSelection(selection)
		options.MemberTagIDs = selection.MemberTagIDs
		return buildAccountRecipientCandidates(options)
	case CampaignDeliveryTargetModeUsersByFilter:
		if selection.Filters == nil {
			return bulkEmailRecipientCandidateQuery{}, fmt.Errorf("filters required for MEMBERS_BY_FILTER query")
		}
		options := accountRecipientOptionsFromSelection(selection)
		return buildAccountRecipientCandidates(options)
	default:
		return bulkEmailRecipientCandidateQuery{}, fmt.Errorf(
			"unsupported bulk email recipient selection mode: %s",
			selection.Mode,
		)
	}
}

func accountRecipientOptionsFromSelection(selection *bulkEmailRecipientSelection) accountRecipientCandidateOptions {
	contextKind := BulkEmailContextAccountCurrent
	if selection.RequireNewsletterSubscription {
		contextKind = BulkEmailContextNewsletterSubscription
	}
	return accountRecipientCandidateOptions{
		ContextKind:                   contextKind,
		RequireNewsletterSubscription: selection.RequireNewsletterSubscription,
		Filters:                       selection.Filters,
	}
}

func buildAccountRecipientCandidates(options accountRecipientCandidateOptions) (bulkEmailRecipientCandidateQuery, error) {
	contextKind := strings.TrimSpace(options.ContextKind)
	if contextKind == "" {
		contextKind = BulkEmailContextAccountCurrent
	}

	joins := []string{}
	where := []string{
		"m.deleted_at IS NULL",
		"m.onboarded = TRUE",
		"ki.state = ?",
		"(ki.metadata_admin->>'banned' IS NULL OR ki.metadata_admin->>'banned' = 'false')",
		"m.primary_email IS NOT NULL",
		"m.primary_email <> ''",
		"m.primary_email NOT LIKE ?",
	}
	args := structured.Values{contextKind, auth.KratosStateActive, "%.local"}

	if options.RequireNewsletterSubscription {
		joins = append(joins, "JOIN newsletter_subscription ns ON ns.identity_id = ki.id")
	}
	if len(options.MemberTagIDs) > 0 {
		joins = append(joins, "JOIN user_tag_mapping utm ON utm.member_id = m.id")
		where = append(where, "utm.tag_id IN ?")
		args = append(args, options.MemberTagIDs)
	}
	if options.Filters != nil {
		var err error
		where, args, err = appendBulkEmailFilterConditions(where, args, options.Filters)
		if err != nil {
			return bulkEmailRecipientCandidateQuery{}, err
		}
	}

	return bulkEmailRecipientCandidateQuery{
		SQL: fmt.Sprintf(`
				SELECT
					m.primary_email AS email,
					m.id AS member_id,
					ki.id AS identity_id,
					NULLIF(m.preferred_locale, '') AS locale,
				? AS context_kind,
				0 AS priority,
				m.created_at AS sort_at
			FROM kratos.identities ki
			JOIN member m
				ON m.id::text = ki.external_id
				AND m.account_identity_id = ki.id
			%s
			WHERE %s
			`, strings.Join(joins, "\n"), strings.Join(where, "\n\t\t\t\tAND ")),
		Args: args,
	}, nil
}

func appendBulkEmailFilterConditions(
	where []string,
	args structured.Values,
	filters *bulkEmailRecipientFilters,
) ([]string, structured.Values, error) {
	if len(filters.AccountRoles) > 0 {
		if len(filters.AccountIdentityIDs) == 0 {
			// A valid platform permission with no current subjects matches no
			// recipients; it must never fall back to Kratos metadata.
			where = append(where, "FALSE")
		} else {
			where = append(where, "m.account_identity_id::text IN ?")
			args = append(args, filters.AccountIdentityIDs)
		}
	}
	if filters.CreatedAfter != nil {
		where = append(where, "m.created_at >= ?")
		args = append(args, *filters.CreatedAfter)
	}
	if filters.CreatedBefore != nil {
		where = append(where, "m.created_at <= ?")
		args = append(args, *filters.CreatedBefore)
	}
	if len(filters.ExcludedMemberIDs) > 0 {
		where = append(where, "m.id NOT IN ?")
		args = append(args, filters.ExcludedMemberIDs)
	}
	return where, args, nil
}

func normalizeBulkEmailSubjectLookups(values []string) ([]policyv1.SubjectLookup, error) {
	seen := map[string]bool{}
	lookups := make([]policyv1.SubjectLookup, 0, len(values))
	for _, value := range values {
		lookup, present, err := normalizeBulkEmailSubjectLookup(value)
		if err != nil {
			return nil, err
		}
		if !present || seen[lookup.Permission()] {
			continue
		}
		seen[lookup.Permission()] = true
		lookups = append(lookups, lookup)
	}
	return lookups, nil
}

func normalizeBulkEmailSubjectLookup(value string) (policyv1.SubjectLookup, bool, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "":
		return policyv1.SubjectLookup{}, false, nil
	case policyv1.Role.Admin().ID():
		return policyv1.Platform.LookupAdminSubjects(), true, nil
	case policyv1.Role.Author().ID():
		return policyv1.Platform.LookupAuthorSubjects(), true, nil
	case policyv1.Role.User().ID():
		return policyv1.Platform.LookupUserSubjects(), true, nil
	default:
		return policyv1.SubjectLookup{}, false, errs.InvalidArgumentMsg(fmt.Sprintf("invalid account permission: %s", value))
	}
}

// resolveBulkEmailRecipientSelectionPermissions turns the product's typed
// platform-permission filter into its current fully-consistent SpiceDB
// account_identity subject set before SQL hydrates Member delivery details.
func resolveBulkEmailRecipientSelectionPermissions(ctx context.Context, spiceDB *auth.SpiceDBClient, selection *bulkEmailRecipientSelection) error {
	if selection == nil || selection.Filters == nil || len(selection.Filters.AccountRoles) == 0 {
		return nil
	}
	if spiceDB == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	lookups, err := normalizeBulkEmailSubjectLookups(selection.Filters.AccountRoles)
	if err != nil {
		return err
	}
	identityIDs := make([]string, 0)
	seen := make(map[string]struct{})
	for _, lookup := range lookups {
		subjects, lookupErr := spiceDB.LookupGlobalSubjects(ctx, lookup)
		if lookupErr != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		for _, subject := range subjects {
			identityID := subject.ID.String()
			if _, exists := seen[identityID]; exists {
				continue
			}
			seen[identityID] = struct{}{}
			identityIDs = append(identityIDs, identityID)
		}
	}
	selection.Filters.AccountIdentityIDs = identityIDs
	return nil
}

func bulkEmailRecipientCountSQL(candidatesSQL string) string {
	return fmt.Sprintf(`
		WITH candidates AS (
			%s
		),
		ranked AS (
			SELECT
				*,
				ROW_NUMBER() OVER (
					PARTITION BY LOWER(TRIM(email))
					ORDER BY priority ASC, sort_at ASC, LOWER(TRIM(email)) ASC
				) AS rn
			FROM candidates
			WHERE email IS NOT NULL
				AND TRIM(email) <> ''
		)
		SELECT COUNT(*) FROM ranked WHERE rn = 1
	`, candidatesSQL)
}
