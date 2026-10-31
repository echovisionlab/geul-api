package sharelink

import managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

// EntityType is the stored kind of a Share Link target. The values are the
// strings share_link.entity_type already holds.
type EntityType string

const (
	EntityPost          EntityType = "SHARE_LINK_ENTITY_TYPE_POST"
	EntityPage          EntityType = "SHARE_LINK_ENTITY_TYPE_PAGE"
	EntityWork          EntityType = "SHARE_LINK_ENTITY_TYPE_WORK"
	EntityRelease       EntityType = "SHARE_LINK_ENTITY_TYPE_RELEASE"
	EntityForm          EntityType = "SHARE_LINK_ENTITY_TYPE_FORM"
	EntityFormDashboard EntityType = "SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD"
	EntityPrivacy       EntityType = "SHARE_LINK_ENTITY_TYPE_PRIVACY"
	EntityTerms         EntityType = "SHARE_LINK_ENTITY_TYPE_TERMS"
	EntityLabel         EntityType = "SHARE_LINK_ENTITY_TYPE_LABEL"
	EntityArtist        EntityType = "SHARE_LINK_ENTITY_TYPE_ARTIST"
)

var entityTypes = map[EntityType]managev1.ShareLinkEntityType{
	EntityPost:          managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST,
	EntityPage:          managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE,
	EntityWork:          managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK,
	EntityRelease:       managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE,
	EntityForm:          managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM,
	EntityFormDashboard: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD,
	EntityPrivacy:       managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY,
	EntityTerms:         managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS,
	EntityLabel:         managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL,
	EntityArtist:        managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST,
}

// EntityTypeToProto maps a stored entity type to the API enum. A value the
// API does not know is UNSPECIFIED.
func EntityTypeToProto(entityType EntityType) managev1.ShareLinkEntityType {
	return entityTypes[entityType]
}

// EntityTypeFromProto maps an API entity type to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func EntityTypeFromProto(entityType managev1.ShareLinkEntityType) (EntityType, bool) {
	for stored, value := range entityTypes {
		if value == entityType {
			return stored, true
		}
	}
	return "", false
}

func (t EntityType) known() bool {
	_, ok := entityTypes[t]
	return ok
}
