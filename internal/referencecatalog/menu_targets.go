package referencecatalog

// MenuTarget is a catalog row that menu items can link to.
type MenuTarget struct {
	LinkType string
	ID       string
	Slug     string
}

// MenuTargetSlugChange moves the menu items linking to Target to NextSlug.
type MenuTargetSlugChange struct {
	Target   MenuTarget
	NextSlug string
}
