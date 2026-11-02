package release

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
	current []ReleaseArtist,
	observed, desired []*ReleaseArtistInput,
	intent *RelationOrderIntent,
) []ReleaseArtist {
	next, membershipChanged := observedMerge[ReleaseArtist, ReleaseArtistInput]{
		rowKey:   func(row ReleaseArtist) string { return row.ArtistID },
		inputKey: func(input *ReleaseArtistInput) string { return input.ArtistID },
		add: func(input *ReleaseArtistInput) (ReleaseArtist, bool) {
			return ReleaseArtist{ArtistID: input.ArtistID}, input.ArtistID != ""
		},
	}.merge(current, observed, desired)
	return reorderRelation(next, func(row ReleaseArtist) string { return row.ArtistID },
		func(row *ReleaseArtist, order int) { row.SortOrder = order }, intent, membershipChanged)
}

func mergeReleaseLabels(
	current []ReleaseLabel,
	observed, desired []*ReleaseLabelInput,
	intent *RelationOrderIntent,
) []ReleaseLabel {
	next, membershipChanged := observedMerge[ReleaseLabel, ReleaseLabelInput]{
		rowKey:   func(row ReleaseLabel) string { return row.LabelID },
		inputKey: func(input *ReleaseLabelInput) string { return input.LabelID },
		update: func(row *ReleaseLabel, observed, desired *ReleaseLabelInput) {
			if !sameOptionalString(observed.CatalogNumber, desired.CatalogNumber) {
				row.CatalogNumber = desired.CatalogNumber
			}
		},
		add: func(input *ReleaseLabelInput) (ReleaseLabel, bool) {
			return ReleaseLabel{LabelID: input.LabelID, CatalogNumber: input.CatalogNumber}, input.LabelID != ""
		},
	}.merge(current, observed, desired)
	return reorderRelation(next, func(row ReleaseLabel) string { return row.LabelID },
		func(row *ReleaseLabel, order int) { row.SortOrder = order }, intent, membershipChanged)
}

func mergeReleaseFormats(
	current []ReleaseFormat,
	observed, desired []*ReleaseFormatInput,
) []ReleaseFormat {
	next, _ := observedMerge[ReleaseFormat, ReleaseFormatInput]{
		rowKey:   func(row ReleaseFormat) string { return row.FormatID },
		inputKey: func(input *ReleaseFormatInput) string { return input.FormatID },
		update: func(row *ReleaseFormat, observed, desired *ReleaseFormatInput) {
			if !sameOptionalString(observed.Description, desired.Description) {
				row.FormatDescription = desired.Description
			}
		},
		add: func(input *ReleaseFormatInput) (ReleaseFormat, bool) {
			return ReleaseFormat{FormatID: input.FormatID, FormatDescription: input.Description}, input.FormatID != ""
		},
	}.merge(current, observed, desired)
	return next
}

func mergeReleaseCredits(
	current []ReleaseCredit,
	observed, desired []*CreditInput,
	intent *RelationOrderIntent,
) []ReleaseCredit {
	next, membershipChanged := observedMerge[ReleaseCredit, CreditInput]{
		rowKey:   func(row ReleaseCredit) string { return row.ID },
		inputKey: (*CreditInput).id,
		update: func(row *ReleaseCredit, observed, desired *CreditInput) {
			applyCreditAttributeDelta(
				creditAttributes{&row.ArtistID, &row.MemberID, &row.CreditedName, &row.CreditRole}, observed, desired,
			)
		},
		add: func(input *CreditInput) (ReleaseCredit, bool) {
			return ReleaseCredit{
				ID: input.id(), CreditRole: input.CreditRole, SortOrder: int(input.SortOrder),
				ArtistID: input.ArtistID, MemberID: input.MemberID, CreditedName: input.CreditedName,
			}, true
		},
	}.merge(current, observed, desired)
	return reorderRelation(next, func(row ReleaseCredit) string { return row.ID },
		func(row *ReleaseCredit, order int) { row.SortOrder = order }, intent, membershipChanged)
}

func mergeTrackCredits(
	trackID string,
	current []TrackCredit,
	observed, desired []*CreditInput,
) []TrackCredit {
	next, membershipChanged := observedMerge[TrackCredit, CreditInput]{
		rowKey:   func(row TrackCredit) string { return row.ID },
		inputKey: (*CreditInput).id,
		update: func(row *TrackCredit, observed, desired *CreditInput) {
			applyCreditAttributeDelta(
				creditAttributes{&row.ArtistID, &row.MemberID, &row.CreditedName, &row.CreditRole}, observed, desired,
			)
		},
		add: func(input *CreditInput) (TrackCredit, bool) {
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

func sameReleaseArtistRows(left, right []ReleaseArtist) bool {
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

func sameReleaseLabelRows(left, right []ReleaseLabel) bool {
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

func sameReleaseFormatRows(left, right []ReleaseFormat) bool {
	if len(left) != len(right) {
		return false
	}
	byID := make(map[string]ReleaseFormat, len(left))
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

func sameReleaseCreditRows(left, right []ReleaseCredit) bool {
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
