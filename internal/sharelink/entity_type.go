package sharelink

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

// EntityTypes returns the target kinds a link may name.
func EntityTypes() []EntityType {
	return []EntityType{
		EntityPost, EntityPage, EntityWork, EntityRelease, EntityForm,
		EntityFormDashboard, EntityPrivacy, EntityTerms, EntityLabel, EntityArtist,
	}
}

// Known reports whether t is one of EntityTypes. A stored value outside them
// is legacy or corrupt and names no target.
func (t EntityType) Known() bool {
	_, ok := targetPolicies[t]
	return ok
}
