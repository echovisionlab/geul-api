package post

// OverlaySourceLocale shows the source locale row's title, summary and OG
// image on stored.
func OverlaySourceLocale(stored *Post, state SourceLocaleState) {
	if stored == nil {
		return
	}
	if state.Title != nil {
		stored.Title = *state.Title
	}
	if state.Summary != nil {
		stored.Summary = state.Summary
	}
	stored.SourceLocaleOgAssetID = state.OgAssetID
}

func collectManagePostIDs(posts []Post) []string {
	ids := make([]string, 0, len(posts))
	for _, stored := range posts {
		ids = append(ids, stored.ID)
	}
	return ids
}
