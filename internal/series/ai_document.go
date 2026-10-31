package series

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"github.com/google/uuid"
)

const (
	postSeriesAIDocumentBlock    core.BlockID          = "series"
	postSeriesAIDocumentKind     core.BlockKind        = "post_series"
	postSeriesAIDocumentPosts    core.RelationID       = "posts"
	postSeriesAIDocumentPostKind core.RelationItemKind = "post"

	postSeriesAIFieldTitle   core.FieldID = "title"
	postSeriesAIFieldSummary core.FieldID = "summary"
	postSeriesAIFieldSlug    core.FieldID = "slug"
	postSeriesAIFieldStatus  core.FieldID = "status"
)

var postSeriesAIDocumentCatalog = core.Catalog{
	Fingerprint: "post-series-metadata-post-order-v1",
	BlockKinds:  []core.BlockKind{postSeriesAIDocumentKind},
	Fields: []core.FieldRule{
		{BlockKind: postSeriesAIDocumentKind, Field: postSeriesAIFieldTitle, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: postSeriesAIDocumentKind, Field: postSeriesAIFieldSummary, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
		{BlockKind: postSeriesAIDocumentKind, Field: postSeriesAIFieldSlug, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipShared},
		{BlockKind: postSeriesAIDocumentKind, Field: postSeriesAIFieldStatus, ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipShared},
	},
	Relations: []core.RelationRule{{
		BlockKind: postSeriesAIDocumentKind,
		Relation:  postSeriesAIDocumentPosts,
		ItemKinds: []core.RelationItemKind{postSeriesAIDocumentPostKind},
	}},
}

// aiDocumentAccess locks a Series root and requires one exact action on it.
type aiDocumentAccess interface {
	RequirePermissionAndLock(context.Context, *gorm.DB, mutation.Require, string, seriesAction) error
}

type seriesAIDocumentAccess struct{}

func (seriesAIDocumentAccess) RequirePermissionAndLock(
	ctx context.Context,
	tx *gorm.DB,
	require mutation.Require,
	seriesID string,
	action seriesAction,
) error {
	return requireSeriesPermissionAndLock(ctx, tx, require, seriesID, action)
}

type postSeriesAIDocumentState struct {
	root              model.Series
	source            model.SeriesTranslation
	locale            *model.SeriesTranslation
	localeExists      bool
	postIDs           []string
	contentDocumentID uuid.UUID
	contentRevision   uuid.UUID
	documentRevision  core.Revision
	targetRevision    *core.Revision
}

type postSeriesAIMutation struct {
	state              postSeriesAIDocumentState
	localeTitle        *string
	localeSummary      *string
	localeValuesLoaded bool
	createTranslation  bool
	deleteTranslation  bool
	slug               *string
	status             *model.SeriesStatus
	postIDs            []string
	localeChanged      bool
	titleChanged       bool
	slugChanged        bool
	statusChanged      bool
	changes            []core.Change
	audits             seriesAudits
}

func (s *Service) applyAIDocumentOperation(
	ctx context.Context,
	tx *gorm.DB,
	mutation *postSeriesAIMutation,
	command core.ValidatedApply,
	index int,
	operation core.Operation,
) (bool, error) {
	mutation.loadLocaleValues()
	switch operation.Kind {
	case core.OperationSetField:
		return s.applyAISetField(ctx, tx, mutation, command, operation.SetField)
	case core.OperationUnsetField:
		return s.applyAIUnsetField(ctx, tx, mutation, command, operation.UnsetField)
	case core.OperationInsertRelationItem:
		return s.applyAIInsertPost(ctx, tx, mutation, operation.InsertRelationItem)
	case core.OperationDeleteRelationItem:
		return s.applyAIDeletePost(ctx, tx, mutation, operation.DeleteRelationItem)
	case core.OperationMoveRelationItem:
		return s.applyAIMovePost(ctx, tx, mutation, operation.MoveRelationItem)
	case core.OperationCreateTranslation:
		mutation.createTranslation = true
		mutation.localeChanged = true
		return true, nil
	case core.OperationDeleteTranslation:
		mutation.deleteTranslation = true
		mutation.localeChanged = true
		return true, nil
	default:
		return false, postSeriesAIOperationError(index, operation, "Post Series does not support this structural operation")
	}
}

func (m *postSeriesAIMutation) loadLocaleValues() {
	if m.localeValuesLoaded {
		return
	}
	m.localeValuesLoaded = true
	if m.state.locale != nil {
		m.localeTitle = cloneString(m.state.locale.Title)
		m.localeSummary = cloneString(m.state.locale.Summary)
	}
	m.postIDs = append([]string(nil), m.state.postIDs...)
}

func (s *Service) applyAISetField(
	ctx context.Context,
	tx *gorm.DB,
	mutation *postSeriesAIMutation,
	command core.ValidatedApply,
	operation *core.SetField,
) (bool, error) {
	if operation == nil || operation.Target.Block != postSeriesAIDocumentBlock || operation.Value.Kind != core.ValueKindText {
		return false, domainerr.InvalidField("operation", "invalid Post Series field mutation")
	}
	value := operation.Value.Text
	switch operation.Target.Field {
	case postSeriesAIFieldTitle:
		if command.LocaleRole == core.LocaleRoleSource {
			value = strings.TrimSpace(value)
			if value == "" {
				return false, domainerr.Required("title")
			}
		}
		if equalOptionalString(mutation.localeTitle, &value) {
			return false, nil
		}
		mutation.localeTitle = &value
		mutation.localeChanged = true
		mutation.titleChanged = true
		return true, nil
	case postSeriesAIFieldSummary:
		if equalOptionalString(mutation.localeSummary, &value) {
			return false, nil
		}
		mutation.localeSummary = &value
		mutation.localeChanged = true
		return true, nil
	case postSeriesAIFieldSlug:
		value, err := validateSeriesSlug(value)
		if err != nil {
			return false, err
		}
		current := mutation.state.root.Slug
		if mutation.slug != nil {
			current = *mutation.slug
		}
		if value == current {
			return false, nil
		}
		if err := validateSeriesUpdateSlug(ctx, tx, mutation.state.root.ID, &value); err != nil {
			return false, err
		}
		mutation.slug = &value
		mutation.slugChanged = value != mutation.state.root.Slug
		return true, nil
	case postSeriesAIFieldStatus:
		status, err := validateSeriesStatus(value)
		if err != nil {
			return false, err
		}
		current := mutation.state.root.Status
		if mutation.status != nil {
			current = *mutation.status
		}
		if status == current {
			return false, nil
		}
		mutation.status = &status
		mutation.statusChanged = status != mutation.state.root.Status
		return true, nil
	default:
		return false, domainerr.InvalidField("field", "unsupported Post Series field")
	}
}

func (s *Service) applyAIUnsetField(
	_ context.Context,
	_ *gorm.DB,
	mutation *postSeriesAIMutation,
	command core.ValidatedApply,
	operation *core.UnsetField,
) (bool, error) {
	if operation == nil || operation.Target.Block != postSeriesAIDocumentBlock {
		return false, domainerr.InvalidField("operation", "invalid Post Series field mutation")
	}
	switch operation.Target.Field {
	case postSeriesAIFieldTitle:
		if command.LocaleRole == core.LocaleRoleSource {
			return false, domainerr.Required("title")
		}
		return false, domainerr.InvalidField("field", "Post Series target fields use explicit empty instead of unset")
	case postSeriesAIFieldSummary:
		if command.LocaleRole == core.LocaleRoleNonSource {
			return false, domainerr.InvalidField("field", "Post Series target fields use explicit empty instead of unset")
		}
		if mutation.localeSummary == nil {
			return false, nil
		}
		mutation.localeSummary = nil
		mutation.localeChanged = true
		return true, nil
	case postSeriesAIFieldSlug, postSeriesAIFieldStatus:
		return false, domainerr.InvalidField("field", "Post Series slug and status cannot be unset")
	default:
		return false, domainerr.InvalidField("field", "unsupported Post Series field")
	}
}

func (s *Service) applyAIInsertPost(
	ctx context.Context,
	tx *gorm.DB,
	mutation *postSeriesAIMutation,
	operation *core.InsertRelationItem,
) (bool, error) {
	if err := validatePostSeriesAIRelation(operation.Block, operation.Relation, operation.Item, operation.Kind); err != nil {
		return false, err
	}
	postID := string(operation.Item)
	observed, err := lockPostSeriesRelation(ctx, tx, s.postMembership, postID)
	if err != nil {
		return false, err
	}
	if err := s.assignPostToSeriesAfterSeriesPermissionWithDB(ctx, tx, &mutation.audits, postID, mutation.state.root.ID, observed); err != nil {
		return false, err
	}
	mutation.postIDs = insertPostAfter(mutation.postIDs, postID, string(operation.After))
	if err := orderSeriesPosts(ctx, tx, s.postMembership, mutation.state.root.ID, mutation.postIDs, dbtime.Now()); err != nil {
		return false, err
	}
	mutation.audits.order(mutation.state.root.ID, mutation.postIDs)
	return true, nil
}

func (s *Service) applyAIDeletePost(
	ctx context.Context,
	tx *gorm.DB,
	mutation *postSeriesAIMutation,
	operation *core.DeleteRelationItem,
) (bool, error) {
	if err := validatePostSeriesAIRelation(operation.Block, operation.Relation, operation.Item, postSeriesAIDocumentPostKind); err != nil {
		return false, err
	}
	postID := string(operation.Item)
	current, err := lockPostSeriesRelation(ctx, tx, s.postMembership, postID)
	if err != nil {
		return false, err
	}
	if current == nil || *current != mutation.state.root.ID {
		return false, domainerr.InvalidField("post", "must belong to the Post Series")
	}
	if err := seriesRelationError(s.postMembership.ClearSeries(ctx, tx, postID, mutation.state.root.ID, dbtime.Now())); err != nil {
		return false, err
	}
	mutation.postIDs = removePostID(mutation.postIDs, postID)
	if err := orderSeriesPosts(ctx, tx, s.postMembership, mutation.state.root.ID, mutation.postIDs, dbtime.Now()); err != nil {
		return false, err
	}
	mutation.audits.membership(mutation.state.root.ID, postID, mutation.state.root.ID, "")
	return true, nil
}

func (s *Service) applyAIMovePost(
	ctx context.Context,
	tx *gorm.DB,
	mutation *postSeriesAIMutation,
	operation *core.MoveRelationItem,
) (bool, error) {
	if operation.TargetBlock != postSeriesAIDocumentBlock || operation.Target != postSeriesAIDocumentPosts {
		return false, domainerr.InvalidField("relation", "Post can only move inside the Post Series order")
	}
	if err := validatePostSeriesAIRelation(operation.Block, operation.Relation, operation.Item, postSeriesAIDocumentPostKind); err != nil {
		return false, err
	}
	next := movePostAfter(mutation.postIDs, string(operation.Item), string(operation.After))
	if sameSeriesPostOrder(mutation.postIDs, next) {
		return false, nil
	}
	if err := orderSeriesPosts(ctx, tx, s.postMembership, mutation.state.root.ID, next, dbtime.Now()); err != nil {
		return false, err
	}
	mutation.audits.order(mutation.state.root.ID, next)
	mutation.postIDs = next
	return true, nil
}

func (s *Service) persistAIDocumentMutation(
	ctx context.Context,
	tx *gorm.DB,
	mutation *postSeriesAIMutation,
	command core.ValidatedApply,
) error {
	now := s.now().UTC()
	seriesID := mutation.state.root.ID
	locale := string(command.Locale)
	if err := persistPostSeriesAILocale(ctx, tx, mutation, locale, now); err != nil {
		return err
	}
	metadataFields, err := s.persistPostSeriesAIRoot(ctx, tx, mutation, now)
	if err != nil {
		return err
	}
	sourceDocumentChanged := mutation.slugChanged || mutation.statusChanged ||
		!sameSeriesPostOrder(mutation.state.postIDs, mutation.postIDs) ||
		(command.LocaleRole == core.LocaleRoleSource && mutation.localeChanged)
	if sourceDocumentChanged {
		if _, err := advanceSeriesContentDocument(
			ctx, tx, seriesID, mutation.state.contentDocumentID,
			mutation.state.contentRevision, now,
		); err != nil {
			return err
		}
	}
	if mutation.localeChanged {
		metadataFields = append(metadataFields, "source_copy")
	}
	if len(metadataFields) != 0 {
		sort.Strings(metadataFields)
		mutation.audits.sourceMetadata(seriesID, metadataFields)
	}
	if mutation.statusChanged {
		mutation.audits.lifecycle(seriesID, postSeriesAuditState(mutation.state.root.Status), postSeriesAuditState(*mutation.status))
	}
	if mutation.titleChanged || mutation.deleteTranslation {
		_, err := s.ogRefresh.RequestCurrent(ctx, tx, seriesID, locale, false, "series_ai_document_title_updated")
		if err != nil {
			return err
		}
	}
	return nil
}

// persistPostSeriesAILocale creates, deletes or rewrites the locale row the
// mutation changed.
func persistPostSeriesAILocale(ctx context.Context, tx *gorm.DB, mutation *postSeriesAIMutation, locale string, now time.Time) error {
	seriesID := mutation.state.root.ID
	if mutation.createTranslation {
		if err := tx.WithContext(ctx).Table("series_translation").Create(&model.SeriesTranslation{
			EntityID: seriesID, Locale: locale, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
	}
	if mutation.deleteTranslation {
		result := tx.WithContext(ctx).Table("series_translation").Where("entity_id = ? AND locale = ?", seriesID, locale).
			Delete(&model.SeriesTranslation{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return domainerr.Precondition("Post Series translation changed; retry")
		}
	}
	if !mutation.localeChanged || mutation.createTranslation || mutation.deleteTranslation {
		return nil
	}
	updatedAt := now
	if mutation.state.locale != nil {
		updatedAt = translation.NextTargetUpdatedAt(now, mutation.state.locale.UpdatedAt)
	}
	if !mutation.state.localeExists {
		return tx.WithContext(ctx).Table("series_translation").Create(&model.SeriesTranslation{
			EntityID: seriesID, Locale: locale, Title: mutation.localeTitle, Summary: mutation.localeSummary,
			ContentText: cloneString(mutation.localeSummary), CreatedAt: updatedAt, UpdatedAt: updatedAt,
		}).Error
	}
	result := tx.WithContext(ctx).Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, locale).
		Updates(structured.Fields{"title": mutation.localeTitle, "summary": mutation.localeSummary, "content_text": mutation.localeSummary, "updated_at": updatedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domainerr.Precondition("Post Series locale changed; retry")
	}
	return nil
}

// persistPostSeriesAIRoot writes a changed slug or status and returns the
// changed metadata audit fields.
func (s *Service) persistPostSeriesAIRoot(ctx context.Context, tx *gorm.DB, mutation *postSeriesAIMutation, now time.Time) ([]string, error) {
	seriesID := mutation.state.root.ID
	metadataFields := make([]string, 0, 2)
	if mutation.slugChanged {
		if err := tx.WithContext(ctx).Model(&model.Series{}).Where("id = ?", seriesID).
			Updates(structured.Fields{"slug": *mutation.slug, "updated_at": now}).Error; err != nil {
			return nil, err
		}
		if err := s.menuTargets.UpdateSlug(ctx, tx, "series", seriesID, mutation.state.root.Slug, *mutation.slug); err != nil {
			return nil, err
		}
		metadataFields = append(metadataFields, "slug")
	}
	if mutation.statusChanged {
		if err := tx.WithContext(ctx).Model(&model.Series{}).Where("id = ?", seriesID).
			Updates(structured.Fields{"status": *mutation.status, "updated_at": now}).Error; err != nil {
			return nil, err
		}
	}
	return metadataFields, nil
}

func (s *Service) loadAIDocumentState(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	locale string,
) (postSeriesAIDocumentState, error) {
	var state postSeriesAIDocumentState
	if err := tx.WithContext(ctx).Where("id = ?", seriesID).Take(&state.root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return state, domainerr.ResourceNotFound("series", seriesID)
		}
		return state, err
	}
	contentDocument, err := loadSeriesContentDocumentState(ctx, tx, seriesID, true)
	if err != nil {
		return state, err
	}
	state.contentDocumentID = contentDocument.ID
	state.contentRevision = contentDocument.Revision
	state.documentRevision = core.Revision(contentDocument.Revision.String())
	if err := tx.WithContext(ctx).Table("series_translation").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("entity_id = ? AND locale = ?", seriesID, state.root.SourceLocale).
		Take(&state.source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return state, domainerr.ResourceNotFound("series_translation", seriesID)
		}
		return state, err
	}
	if locale == state.root.SourceLocale {
		state.locale = &state.source
		state.localeExists = true
	} else {
		var target model.SeriesTranslation
		err := tx.WithContext(ctx).Table("series_translation").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("entity_id = ? AND locale = ?", seriesID, locale).Take(&target).Error
		switch {
		case err == nil:
			state.locale = &target
			state.localeExists = true
		case errors.Is(err, gorm.ErrRecordNotFound):
		case err != nil:
			return state, err
		}
	}
	if state.postIDs, err = s.postMembership.SeriesPostIDs(ctx, tx, seriesID); err != nil {
		return state, err
	}
	if locale != state.root.SourceLocale && state.localeExists {
		updatedAt := state.locale.UpdatedAt
		targetRevision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
			LocaleExists: true, DocumentRevision: string(state.documentRevision), LocaleUpdatedAt: &updatedAt,
		})
		if err != nil {
			return state, err
		}
		revision := core.Revision(targetRevision)
		state.targetRevision = &revision
	}
	return state, nil
}

func (s postSeriesAIDocumentState) document(identity core.DocumentIdentity, locale core.Locale) core.Document {
	node := core.Node{
		ID: postSeriesAIDocumentBlock, Kind: postSeriesAIDocumentKind,
		Shared: []core.FieldValue{
			{ID: postSeriesAIFieldSlug, Value: core.Text(s.root.Slug)},
			{ID: postSeriesAIFieldStatus, Value: core.Text(string(s.root.Status))},
		},
	}
	if s.locale != nil {
		if s.locale.Title != nil {
			node.Localized = append(node.Localized, core.FieldValue{ID: postSeriesAIFieldTitle, Value: core.Text(*s.locale.Title)})
		}
		if s.locale.Summary != nil {
			node.Localized = append(node.Localized, core.FieldValue{ID: postSeriesAIFieldSummary, Value: core.Text(*s.locale.Summary)})
		}
	}
	relation := core.Relation{ID: postSeriesAIDocumentPosts}
	for order, postID := range s.postIDs {
		relation.Items = append(relation.Items, core.RelationItem{
			ID: core.RelationItemID(postID), Kind: postSeriesAIDocumentPostKind, Order: order,
		})
	}
	node.Relations = []core.Relation{relation}
	return core.Document{
		Identity: identity, DocumentRevision: s.documentRevision,
		TargetRevision: clonePostSeriesRevision(s.targetRevision), SourceLocale: core.Locale(s.root.SourceLocale),
		Locale: locale, LocaleExists: s.localeExists, Catalog: postSeriesAIDocumentCatalog,
		Nodes: []core.Node{node},
	}
}

func clonePostSeriesRevision(value *core.Revision) *core.Revision {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validatePostSeriesAIDocumentIdentity(identity core.DocumentIdentity) error {
	if identity.Domain != core.DomainPostSeries {
		return domainerr.InvalidField("domain", "Post Series AI document domain is required")
	}
	_, err := uuidutil.ParseCanonical(string(identity.Reference), "document")
	return err
}

func validatePostSeriesAIRelation(block core.BlockID, relation core.RelationID, item core.RelationItemID, kind core.RelationItemKind) error {
	if block != postSeriesAIDocumentBlock || relation != postSeriesAIDocumentPosts || kind != postSeriesAIDocumentPostKind {
		return domainerr.InvalidField("relation", "invalid Post Series Post relation")
	}
	_, err := uuidutil.ParseCanonical(string(item), "post")
	return err
}

func insertPostAfter(ids []string, postID, after string) []string {
	result := removePostID(ids, postID)
	index := 0
	if after != "" {
		index = len(result)
		for candidate := range result {
			if result[candidate] == after {
				index = candidate + 1
				break
			}
		}
	}
	result = append(result, "")
	copy(result[index+1:], result[index:])
	result[index] = postID
	return result
}

func movePostAfter(ids []string, postID, after string) []string {
	return insertPostAfter(ids, postID, after)
}

func removePostID(ids []string, postID string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != postID {
			result = append(result, id)
		}
	}
	return result
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func postSeriesAIOperationError(index int, operation core.Operation, message string) error {
	return &core.ValidationError{Result: core.ValidationResult{Issues: []core.OperationIssue{{
		Operation: index, Code: core.IssueInvalidOperation,
		Handle: strings.Join(postSeriesAIOperationHandles(operation), ","), Message: message,
	}}}}
}

func postSeriesAIOperationHandles(operation core.Operation) []string {
	switch operation.Kind {
	case core.OperationSetField:
		return []string{fmt.Sprintf("field:%s/%s", operation.SetField.Target.Block, operation.SetField.Target.Field)}
	case core.OperationUnsetField:
		return []string{fmt.Sprintf("field:%s/%s", operation.UnsetField.Target.Block, operation.UnsetField.Target.Field)}
	case core.OperationInsertRelationItem:
		return []string{fmt.Sprintf("relation-item:%s/%s/%s", operation.InsertRelationItem.Block, operation.InsertRelationItem.Relation, operation.InsertRelationItem.Item)}
	case core.OperationDeleteRelationItem:
		return []string{fmt.Sprintf("relation-item:%s/%s/%s", operation.DeleteRelationItem.Block, operation.DeleteRelationItem.Relation, operation.DeleteRelationItem.Item)}
	case core.OperationMoveRelationItem:
		return []string{fmt.Sprintf("relation-item:%s/%s/%s", operation.MoveRelationItem.Block, operation.MoveRelationItem.Relation, operation.MoveRelationItem.Item)}
	case core.OperationCreateTranslation, core.OperationDeleteTranslation:
		return []string{"translation"}
	default:
		return []string{"block:series"}
	}
}

var _ core.DomainPort = (*Service)(nil)
var _ core.ExactMutationPort = (*Service)(nil)
