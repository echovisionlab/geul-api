package translationadapter

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/proto"
)

type creativeInterchangeTarget struct {
	state     application.TranslationInterchangeTargetState
	localized *contentv1.LocalizedRichTextDocument
}

func validateCreativeInterchangeLoad(
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
	expectedType string,
) error {
	if db == nil || store == nil || plan == nil {
		return errors.New("creative translation interchange load dependencies are required")
	}
	if entityType != expectedType || plan.EntityType != entityType ||
		plan.EntityID != entityID || plan.TargetLocale != locale {
		return errs.InvalidArgument("target", expectedType+" translation interchange identity does not match the extraction plan")
	}
	return nil
}

func projectCreativeInterchangeTarget(
	plan *core.ExtractionPlan,
	exists bool,
	revision string,
	document *contentv1.LocalizedRichTextDocument,
) (creativeInterchangeTarget, error) {
	if document == nil {
		return creativeInterchangeTarget{}, errors.New("creative translation interchange document is required")
	}
	if document.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT {
		return creativeInterchangeTarget{}, errs.FailedPrecondition("creative translation interchange requires the compact content profile")
	}
	if !exists && len(document.GetLocaleOverlay().GetBlocks()) != 0 {
		return creativeInterchangeTarget{}, errs.InternalMsg("creative target Blocks exist without owning locale metadata")
	}
	targets := make(map[string]core.UnitResult)
	var err error
	if exists {
		targets, err = ProjectRichTextInterchangeTargets(plan, document)
		if err != nil {
			return creativeInterchangeTarget{}, err
		}
	}
	state := application.TranslationInterchangeTargetState{Exists: exists, Targets: targets}
	if exists {
		state.Revision = revision
	}
	return creativeInterchangeTarget{state: state, localized: document}, nil
}

func buildCreativeInterchangeCandidate(
	command application.TranslationInterchangeApply,
	current *contentv1.LocalizedRichTextDocument,
) (*core.Candidate, error) {
	patchCurrent := current
	if command.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		patchCurrent = &contentv1.LocalizedRichTextDocument{
			BlockCatalogFingerprint: current.GetBlockCatalogFingerprint(),
			Profile:                 current.GetProfile(),
			Locale:                  current.GetLocale(),
			Base:                    proto.Clone(current.GetBase()).(*contentv1.RichTextBlockGraph),
			LocaleOverlay:           &contentv1.RichTextLocaleOverlay{Locale: current.GetLocale()},
		}
	}
	overlay, err := BuildRichTextInterchangePatch(
		command.Plan, command.Source.ContentBlockDocument, patchCurrent, command.Targets,
	)
	if err != nil {
		return nil, err
	}
	if patchCurrent != current {
		return &core.Candidate{
			ContentDocumentRevision:   command.Source.ContentDocumentRevision,
			ContentBlockLocaleOverlay: overlay,
			ContentBlockLocaleDeletes: exactReplacementBlockDeletes(
				overlay, current, command.Source.ContentBlockDocument,
			),
		}, nil
	}
	return &core.Candidate{
		ContentDocumentRevision:   command.Source.ContentDocumentRevision,
		ContentBlockLocaleOverlay: overlay,
	}, nil
}

func exactReplacementBlockDeletes(
	overlay *contentv1.RichTextLocaleOverlay,
	current *contentv1.LocalizedRichTextDocument,
	source *contentv1.LocalizedRichTextDocument,
) []string {
	present := richTextLocaleBlocks(overlay)
	sourceBlocks := richTextBaseBlocks(source.GetBase())
	deletes := make([]string, 0)
	for _, block := range current.GetLocaleOverlay().GetBlocks() {
		blockID := strings.TrimSpace(block.GetBlockId())
		if _, currentSource := sourceBlocks[blockID]; !currentSource {
			continue
		}
		if _, replaced := present[blockID]; replaced {
			continue
		}
		deletes = append(deletes, blockID)
	}
	return deletes
}

func appendLocaleContentInterchangeAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	builder LocaleContentAuditBuilder,
	action sharedtelemetry.AuditAction,
	memberID string,
	entityID string,
	locale string,
	targetPreviouslyExists bool,
) error {
	operation := sharedtelemetry.AuditItemOperationUpdated
	if !targetPreviouslyExists {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	return domainaudit.AppendMember(ctx, tx, writer, memberID, action, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return builder(metadata, entityID, locale, operation)
	})
}
