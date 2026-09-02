package emailauthoring

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// templateKeyRegex validates the format of template keys
func toProtoEmailTemplate(t *model.EmailTemplate) *managev1.EmailTemplate {
	populateSystemEmailTemplateMetadata(t)

	proto := &managev1.EmailTemplate{
		Id:               t.ID,
		Key:              t.Key,
		Name:             t.Name,
		Subject:          t.Subject,
		IsSystem:         t.IsSystem,
		IsActive:         t.IsActive,
		CreatedAt:        timestamppb.New(t.CreatedAt),
		DeliveryRunCount: t.DeliveryRunCount,
	}

	if t.Description != nil {
		proto.Description = t.Description
	}
	if t.UpdatedAt != nil {
		proto.UpdatedAt = timestamppb.New(*t.UpdatedAt)
	}
	if t.EventKey != nil {
		proto.EventKey = t.EventKey
	}
	if t.LayoutID != nil {
		proto.LayoutId = t.LayoutID
	}

	// Convert variables
	if t.Variables != nil {
		proto.Variables = make([]*managev1.EmailTemplateVariable, len(t.Variables))
		for i, v := range t.Variables {
			proto.Variables[i] = &managev1.EmailTemplateVariable{
				Name:        v.Name,
				Description: v.Description,
			}
			if v.DefaultValue != nil {
				proto.Variables[i].DefaultValue = v.DefaultValue
			}
		}
	}

	return proto
}

func (s *EmailTemplateService) toProtoEmailTemplateWithDocument(
	ctx context.Context,
	template *model.EmailTemplate,
) (*managev1.EmailTemplate, error) {
	proto := toProtoEmailTemplate(template)
	projection, err := loadCampaignEmailEditorProjection(
		ctx,
		s.db,
		s.contentBlocks,
		emailTemplateContentEntity,
		template.ID,
	)
	if err != nil {
		return nil, err
	}
	proto.Document = projection.Document
	proto.DocumentRevision = projection.Revision
	proto.SourceLocale = projection.SourceLocale
	return proto, nil
}

func populateSystemEmailTemplateMetadata(template *model.EmailTemplate) {
	if !template.IsSystem {
		return
	}
	eventKey, variables, ok := verifiedSystemEmailTemplateMetadata(
		strings.TrimSpace(firstNonEmptyString(ptrStringValue(template.EventKey), template.Key)),
	)
	if !ok {
		return
	}
	if template.EventKey == nil || strings.TrimSpace(*template.EventKey) == "" {
		template.EventKey = eventKey
	}
	if len(template.Variables) == 0 {
		template.Variables = variables
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
