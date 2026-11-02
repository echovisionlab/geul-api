package application

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// Target names one translatable entity by its catalog kind.
type Target struct {
	EntityType string
	EntityID   string
}

func parseTranslationTarget(target *Target) (string, string, error) {
	if target == nil {
		return "", "", domainerr.InvalidField("target", "target is required")
	}
	definition, ok := translation.DefinitionForKind(target.EntityType)
	if !ok {
		return "", "", domainerr.InvalidField("target.entity_type", "unsupported translation entity type")
	}
	entityType := string(definition.Kind)
	entityID := strings.TrimSpace(target.EntityID)
	if entityID == "" {
		return "", "", domainerr.InvalidField("target.entity_id", "entity id is required")
	}
	return entityType, entityID, nil
}
