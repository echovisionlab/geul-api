package release

import (
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// RelationOrderIntent moves ItemID before NextItemID, otherwise after
// PreviousItemID, otherwise to the end.
type RelationOrderIntent struct {
	ItemID         string
	PreviousItemID *string
	NextItemID     *string
}

// Snapshot is the relation entries a client observed. A relation set
// without its observation cannot merge with concurrent edits, so a nil
// Snapshot is rejected.
type Snapshot[T any] struct {
	Items []T
}

// ReleaseArtistInput is one Artist of a Release.
type ReleaseArtistInput struct {
	ArtistID string
}

// ReleaseLabelInput is one Label of a Release.
type ReleaseLabelInput struct {
	LabelID       string
	CatalogNumber *string
}

// ReleaseFormatInput is one Format of a Release.
type ReleaseFormatInput struct {
	FormatID    string
	Description *string
}

// CreditInput is one Release or Track credit. ID is empty for a new credit.
type CreditInput struct {
	ID           *string
	ArtistID     *string
	MemberID     *string
	CreditedName *string
	CreditRole   *string
	SortOrder    int32
}

func (c *CreditInput) id() string {
	if c == nil || c.ID == nil {
		return ""
	}
	return *c.ID
}

func applyRelationOrderIntent(ids []string, intent *RelationOrderIntent) ([]string, bool) {
	if intent == nil || intent.ItemID == "" {
		return ids, false
	}
	from := indexOfID(ids, intent.ItemID)
	if from < 0 {
		return ids, false
	}
	remaining := append([]string(nil), ids...)
	remaining = append(remaining[:from], remaining[from+1:]...)

	if insertBefore := optionalValue(intent.NextItemID); insertBefore != "" && insertBefore != intent.ItemID {
		if to := indexOfID(remaining, insertBefore); to >= 0 {
			result := insertID(remaining, to, intent.ItemID)
			return result, !equalIDs(ids, result)
		}
	}
	if insertAfter := optionalValue(intent.PreviousItemID); insertAfter != "" && insertAfter != intent.ItemID {
		if to := indexOfID(remaining, insertAfter); to >= 0 {
			result := insertID(remaining, to+1, intent.ItemID)
			return result, !equalIDs(ids, result)
		}
	}

	remaining = append(remaining, intent.ItemID)
	return remaining, !equalIDs(ids, remaining)
}

func optionalValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func indexOfID(ids []string, id string) int {
	for index, candidate := range ids {
		if candidate == id {
			return index
		}
	}
	return -1
}

func insertID(ids []string, index int, id string) []string {
	result := make([]string, 0, len(ids)+1)
	result = append(result, ids[:index]...)
	result = append(result, id)
	result = append(result, ids[index:]...)
	return result
}

func equalIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// observedMerge applies a desired relation set to the current rows against
// the set the client observed: an observed entry the client no longer wants
// is removed, an observed entry it still wants takes the client's attribute
// changes, and a wanted entry that is neither observed nor current is
// appended. Entries a concurrent editor added keep their place.
type observedMerge[Row any, Input any] struct {
	rowKey   func(Row) string
	inputKey func(*Input) string
	// update applies the attribute delta between observed and desired; nil
	// keeps the current row.
	update func(row *Row, observed, desired *Input)
	// add returns the row of a new desired entry; false skips the entry.
	add func(*Input) (Row, bool)
}

func (m observedMerge[Row, Input]) merge(current []Row, observed, desired []*Input) ([]Row, bool) {
	observedByKey := m.keyed(observed)
	desiredByKey := m.keyed(desired)
	next := make([]Row, 0, len(current)+len(desiredByKey))
	currentKeys := make(map[string]struct{}, len(current))
	membershipChanged := false
	for _, row := range current {
		key := m.rowKey(row)
		currentKeys[key] = struct{}{}
		observedInput, wasObserved := observedByKey[key]
		desiredInput, isDesired := desiredByKey[key]
		if wasObserved && !isDesired {
			membershipChanged = true
			continue
		}
		if wasObserved && m.update != nil {
			m.update(&row, observedInput, desiredInput)
		}
		next = append(next, row)
	}
	for _, input := range desired {
		if input == nil {
			continue
		}
		key := m.inputKey(input)
		if _, wasObserved := observedByKey[key]; wasObserved {
			continue
		}
		if _, exists := currentKeys[key]; exists && key != "" {
			continue
		}
		row, ok := m.add(input)
		if !ok {
			continue
		}
		next = append(next, row)
		if key != "" {
			currentKeys[key] = struct{}{}
		}
		membershipChanged = true
	}
	return next, membershipChanged
}

func (m observedMerge[Row, Input]) keyed(inputs []*Input) map[string]*Input {
	keyed := make(map[string]*Input, len(inputs))
	for _, input := range inputs {
		if input == nil {
			continue
		}
		if key := m.inputKey(input); key != "" {
			keyed[key] = input
		}
	}
	return keyed
}

// reorderRelation applies the order intent and renumbers the rows when the
// membership changed or the intent moved an entry; otherwise the stored
// order stands.
func reorderRelation[Row any](
	rows []Row,
	key func(Row) string,
	setSortOrder func(*Row, int),
	intent *RelationOrderIntent,
	membershipChanged bool,
) []Row {
	ids := make([]string, len(rows))
	byID := make(map[string]Row, len(rows))
	for index, row := range rows {
		ids[index] = key(row)
		byID[ids[index]] = row
	}
	ids, reordered := applyRelationOrderIntent(ids, intent)
	if !membershipChanged && !reordered {
		return rows
	}
	ordered := make([]Row, 0, len(ids))
	for _, id := range ids {
		if row, ok := byID[id]; ok {
			ordered = append(ordered, row)
		}
	}
	for index := range ordered {
		setSortOrder(&ordered[index], index)
	}
	return ordered
}

func mergeReleaseArtists(
	current []model.ReleaseArtist,
	observed, desired []*ReleaseArtistInput,
	intent *RelationOrderIntent,
) []model.ReleaseArtist {
	next, membershipChanged := observedMerge[model.ReleaseArtist, ReleaseArtistInput]{
		rowKey:   func(row model.ReleaseArtist) string { return row.ArtistID },
		inputKey: func(input *ReleaseArtistInput) string { return input.ArtistID },
		add: func(input *ReleaseArtistInput) (model.ReleaseArtist, bool) {
			return model.ReleaseArtist{ArtistID: input.ArtistID}, input.ArtistID != ""
		},
	}.merge(current, observed, desired)
	return reorderRelation(next, func(row model.ReleaseArtist) string { return row.ArtistID },
		func(row *model.ReleaseArtist, order int) { row.SortOrder = order }, intent, membershipChanged)
}

func mergeReleaseLabels(
	current []model.ReleaseLabel,
	observed, desired []*ReleaseLabelInput,
	intent *RelationOrderIntent,
) []model.ReleaseLabel {
	next, membershipChanged := observedMerge[model.ReleaseLabel, ReleaseLabelInput]{
		rowKey:   func(row model.ReleaseLabel) string { return row.LabelID },
		inputKey: func(input *ReleaseLabelInput) string { return input.LabelID },
		update: func(row *model.ReleaseLabel, observed, desired *ReleaseLabelInput) {
			if !sameOptionalString(observed.CatalogNumber, desired.CatalogNumber) {
				row.CatalogNumber = desired.CatalogNumber
			}
		},
		add: func(input *ReleaseLabelInput) (model.ReleaseLabel, bool) {
			return model.ReleaseLabel{LabelID: input.LabelID, CatalogNumber: input.CatalogNumber}, input.LabelID != ""
		},
	}.merge(current, observed, desired)
	return reorderRelation(next, func(row model.ReleaseLabel) string { return row.LabelID },
		func(row *model.ReleaseLabel, order int) { row.SortOrder = order }, intent, membershipChanged)
}

func mergeReleaseFormats(
	current []model.ReleaseFormat,
	observed, desired []*ReleaseFormatInput,
) []model.ReleaseFormat {
	next, _ := observedMerge[model.ReleaseFormat, ReleaseFormatInput]{
		rowKey:   func(row model.ReleaseFormat) string { return row.FormatID },
		inputKey: func(input *ReleaseFormatInput) string { return input.FormatID },
		update: func(row *model.ReleaseFormat, observed, desired *ReleaseFormatInput) {
			if !sameOptionalString(observed.Description, desired.Description) {
				row.FormatDescription = desired.Description
			}
		},
		add: func(input *ReleaseFormatInput) (model.ReleaseFormat, bool) {
			return model.ReleaseFormat{FormatID: input.FormatID, FormatDescription: input.Description}, input.FormatID != ""
		},
	}.merge(current, observed, desired)
	return next
}

func mergeReleaseCredits(
	current []model.ReleaseCredit,
	observed, desired []*CreditInput,
	intent *RelationOrderIntent,
) []model.ReleaseCredit {
	next, membershipChanged := observedMerge[model.ReleaseCredit, CreditInput]{
		rowKey:   func(row model.ReleaseCredit) string { return row.ID },
		inputKey: (*CreditInput).id,
		update: func(row *model.ReleaseCredit, observed, desired *CreditInput) {
			applyCreditAttributeDelta(
				creditAttributes{&row.ArtistID, &row.MemberID, &row.CreditedName, &row.CreditRole}, observed, desired,
			)
		},
		add: func(input *CreditInput) (model.ReleaseCredit, bool) {
			return model.ReleaseCredit{
				ID: input.id(), CreditRole: input.CreditRole, SortOrder: int(input.SortOrder),
				ArtistID: input.ArtistID, MemberID: input.MemberID, CreditedName: input.CreditedName,
			}, true
		},
	}.merge(current, observed, desired)
	return reorderRelation(next, func(row model.ReleaseCredit) string { return row.ID },
		func(row *model.ReleaseCredit, order int) { row.SortOrder = order }, intent, membershipChanged)
}

func mergeTrackCredits(
	trackID string,
	current []model.TrackCredit,
	observed, desired []*CreditInput,
) []model.TrackCredit {
	next, membershipChanged := observedMerge[model.TrackCredit, CreditInput]{
		rowKey:   func(row model.TrackCredit) string { return row.ID },
		inputKey: (*CreditInput).id,
		update: func(row *model.TrackCredit, observed, desired *CreditInput) {
			applyCreditAttributeDelta(
				creditAttributes{&row.ArtistID, &row.MemberID, &row.CreditedName, &row.CreditRole}, observed, desired,
			)
		},
		add: func(input *CreditInput) (model.TrackCredit, bool) {
			credit, err := newTrackCredit(trackID, input)
			credit.ID = input.id()
			return credit, err == nil
		},
	}.merge(current, observed, desired)
	if membershipChanged {
		for index := range next {
			next[index].SortOrder = index
		}
	}
	return next
}

// creditAttributes points at the attributes a Release or Track credit row
// shares.
type creditAttributes struct {
	artistID, memberID, creditedName, creditRole **string
}

// applyCreditAttributeDelta takes each attribute the client changed between
// its observation and its desired credit; untouched attributes keep a
// concurrent editor's value.
func applyCreditAttributeDelta(current creditAttributes, observed, desired *CreditInput) {
	for _, attribute := range []struct {
		current                **string
		observed, desiredValue *string
	}{
		{current.artistID, observed.ArtistID, desired.ArtistID},
		{current.memberID, observed.MemberID, desired.MemberID},
		{current.creditedName, observed.CreditedName, desired.CreditedName},
		{current.creditRole, observed.CreditRole, desired.CreditRole},
	} {
		if !sameOptionalString(attribute.observed, attribute.desiredValue) {
			*attribute.current = attribute.desiredValue
		}
	}
}

func sameReleaseArtistRows(left, right []model.ReleaseArtist) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ArtistID != right[index].ArtistID || left[index].SortOrder != right[index].SortOrder {
			return false
		}
	}
	return true
}

func persistReleaseArtists(tx *gorm.DB, releaseID string, current, next []model.ReleaseArtist) error {
	nextByID := make(map[string]model.ReleaseArtist, len(next))
	for _, row := range next {
		nextByID[row.ArtistID] = row
	}
	currentByID := make(map[string]model.ReleaseArtist, len(current))
	for _, row := range current {
		currentByID[row.ArtistID] = row
		if _, exists := nextByID[row.ArtistID]; !exists {
			if err := tx.Delete(&model.ReleaseArtist{}, "release_id = ? AND artist_id = ?", releaseID, row.ArtistID).Error; err != nil {
				return err
			}
		}
	}
	for _, row := range next {
		previous, exists := currentByID[row.ArtistID]
		if !exists {
			row.ReleaseID = releaseID
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			continue
		}
		if previous.SortOrder != row.SortOrder {
			if err := tx.Model(&model.ReleaseArtist{}).
				Where("release_id = ? AND artist_id = ?", releaseID, row.ArtistID).
				Update("sort_order", row.SortOrder).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func sameReleaseLabelRows(left, right []model.ReleaseLabel) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].LabelID != right[index].LabelID || left[index].SortOrder != right[index].SortOrder ||
			!sameOptionalString(left[index].CatalogNumber, right[index].CatalogNumber) {
			return false
		}
	}
	return true
}

func persistReleaseLabels(tx *gorm.DB, releaseID string, current, next []model.ReleaseLabel) error {
	nextByID := make(map[string]model.ReleaseLabel, len(next))
	for _, row := range next {
		nextByID[row.LabelID] = row
	}
	currentByID := make(map[string]model.ReleaseLabel, len(current))
	for _, row := range current {
		currentByID[row.LabelID] = row
		if _, exists := nextByID[row.LabelID]; !exists {
			if err := tx.Delete(&model.ReleaseLabel{}, "release_id = ? AND label_id = ?", releaseID, row.LabelID).Error; err != nil {
				return err
			}
		}
	}
	for _, row := range next {
		previous, exists := currentByID[row.LabelID]
		if !exists {
			row.ReleaseID = releaseID
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			continue
		}
		updates := map[string]any{}
		if !sameOptionalString(previous.CatalogNumber, row.CatalogNumber) {
			updates["catalog_number"] = row.CatalogNumber
		}
		if previous.SortOrder != row.SortOrder {
			updates["sort_order"] = row.SortOrder
		}
		if len(updates) > 0 {
			if err := tx.Model(&model.ReleaseLabel{}).
				Where("release_id = ? AND label_id = ?", releaseID, row.LabelID).
				Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func sameReleaseFormatRows(left, right []model.ReleaseFormat) bool {
	if len(left) != len(right) {
		return false
	}
	byID := make(map[string]model.ReleaseFormat, len(left))
	for _, row := range left {
		byID[row.FormatID] = row
	}
	for _, row := range right {
		previous, exists := byID[row.FormatID]
		if !exists || !sameOptionalString(previous.FormatDescription, row.FormatDescription) {
			return false
		}
	}
	return true
}

func persistReleaseFormats(tx *gorm.DB, releaseID string, current, next []model.ReleaseFormat) error {
	nextByID := make(map[string]model.ReleaseFormat, len(next))
	for _, row := range next {
		nextByID[row.FormatID] = row
	}
	currentByID := make(map[string]model.ReleaseFormat, len(current))
	for _, row := range current {
		currentByID[row.FormatID] = row
		if _, exists := nextByID[row.FormatID]; !exists {
			if err := tx.Delete(&model.ReleaseFormat{}, "release_id = ? AND format_id = ?", releaseID, row.FormatID).Error; err != nil {
				return err
			}
		}
	}
	for _, row := range next {
		previous, exists := currentByID[row.FormatID]
		if !exists {
			row.ReleaseID = releaseID
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			continue
		}
		if !sameOptionalString(previous.FormatDescription, row.FormatDescription) {
			if err := tx.Model(&model.ReleaseFormat{}).
				Where("release_id = ? AND format_id = ?", releaseID, row.FormatID).
				Update("format_description", row.FormatDescription).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func sameReleaseCreditRows(left, right []model.ReleaseCredit) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].SortOrder != right[index].SortOrder ||
			!sameOptionalString(left[index].ArtistID, right[index].ArtistID) ||
			!sameOptionalString(left[index].MemberID, right[index].MemberID) ||
			!sameOptionalString(left[index].CreditedName, right[index].CreditedName) ||
			!sameOptionalString(left[index].CreditRole, right[index].CreditRole) {
			return false
		}
	}
	return true
}

func persistReleaseCredits(tx *gorm.DB, releaseID string, current, next []model.ReleaseCredit) error {
	nextByID := make(map[string]model.ReleaseCredit, len(next))
	for _, row := range next {
		nextByID[row.ID] = row
	}
	currentByID := make(map[string]model.ReleaseCredit, len(current))
	for _, row := range current {
		currentByID[row.ID] = row
		if _, exists := nextByID[row.ID]; !exists {
			if err := tx.Delete(&model.ReleaseCredit{}, "release_id = ? AND id = ?", releaseID, row.ID).Error; err != nil {
				return err
			}
		}
	}
	for _, row := range next {
		previous, exists := currentByID[row.ID]
		if !exists {
			row.ReleaseID = releaseID
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			continue
		}
		updates := creditUpdateMap(
			creditValues{previous.ArtistID, previous.MemberID, previous.CreditedName, previous.CreditRole, previous.SortOrder},
			creditValues{row.ArtistID, row.MemberID, row.CreditedName, row.CreditRole, row.SortOrder},
		)
		if len(updates) > 0 {
			if err := tx.Model(&model.ReleaseCredit{}).
				Where("release_id = ? AND id = ?", releaseID, row.ID).
				Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// creditValues are the stored values a Release or Track credit row shares.
type creditValues struct {
	artistID, memberID, creditedName, creditRole *string
	sortOrder                                    int
}

// creditUpdateMap is the column update from previous to next.
func creditUpdateMap(previous, next creditValues) map[string]any {
	updates := make(map[string]any)
	if !sameOptionalString(previous.artistID, next.artistID) {
		updates["artist_id"] = next.artistID
	}
	if !sameOptionalString(previous.memberID, next.memberID) {
		updates["member_id"] = next.memberID
	}
	if !sameOptionalString(previous.creditedName, next.creditedName) {
		updates["credited_name"] = next.creditedName
	}
	if !sameOptionalString(previous.creditRole, next.creditRole) {
		updates["credit_role"] = next.creditRole
	}
	if previous.sortOrder != next.sortOrder {
		updates["sort_order"] = next.sortOrder
	}
	return updates
}
