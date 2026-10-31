package release

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func applyRelationOrderIntent(ids []string, intent *managev1.RelationOrderIntent) ([]string, bool) {
	if intent == nil || intent.GetItemId() == "" {
		return ids, false
	}
	from := indexOfID(ids, intent.GetItemId())
	if from < 0 {
		return ids, false
	}
	remaining := append([]string(nil), ids...)
	remaining = append(remaining[:from], remaining[from+1:]...)

	insertBefore := intent.GetNextItemId()
	if insertBefore != "" && insertBefore != intent.GetItemId() {
		if to := indexOfID(remaining, insertBefore); to >= 0 {
			result := insertID(remaining, to, intent.GetItemId())
			return result, !equalIDs(ids, result)
		}
	}

	insertAfter := intent.GetPreviousItemId()
	if insertAfter != "" && insertAfter != intent.GetItemId() {
		if to := indexOfID(remaining, insertAfter); to >= 0 {
			result := insertID(remaining, to+1, intent.GetItemId())
			return result, !equalIDs(ids, result)
		}
	}

	remaining = append(remaining, intent.GetItemId())
	return remaining, !equalIDs(ids, remaining)
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

func mergeReleaseArtists(
	current []model.ReleaseArtist,
	observed, desired []*managev1.ReleaseArtistInput,
	intent *managev1.RelationOrderIntent,
) []model.ReleaseArtist {
	observedIDs := make(map[string]struct{}, len(observed))
	desiredRows := make(map[string]*managev1.ReleaseArtistInput, len(desired))
	for _, row := range observed {
		if row != nil && row.ArtistId != "" {
			observedIDs[row.ArtistId] = struct{}{}
		}
	}
	for _, row := range desired {
		if row != nil && row.ArtistId != "" {
			desiredRows[row.ArtistId] = row
		}
	}

	next := make([]model.ReleaseArtist, 0, len(current)+len(desiredRows))
	currentIDs := make(map[string]struct{}, len(current))
	membershipChanged := false
	for _, row := range current {
		currentIDs[row.ArtistID] = struct{}{}
		if _, wasObserved := observedIDs[row.ArtistID]; wasObserved {
			if _, isDesired := desiredRows[row.ArtistID]; !isDesired {
				membershipChanged = true
				continue
			}
		}
		next = append(next, row)
	}
	for _, row := range desired {
		if row == nil || row.ArtistId == "" {
			continue
		}
		if _, wasObserved := observedIDs[row.ArtistId]; wasObserved {
			continue
		}
		if _, exists := currentIDs[row.ArtistId]; exists {
			continue
		}
		next = append(next, model.ReleaseArtist{ArtistID: row.ArtistId})
		currentIDs[row.ArtistId] = struct{}{}
		membershipChanged = true
	}

	ids := releaseArtistIDs(next)
	ids, reordered := applyRelationOrderIntent(ids, intent)
	if membershipChanged || reordered {
		next = orderReleaseArtists(next, ids)
		for index := range next {
			next[index].SortOrder = index
		}
	}
	return next
}

func releaseArtistIDs(rows []model.ReleaseArtist) []string {
	ids := make([]string, len(rows))
	for index, row := range rows {
		ids[index] = row.ArtistID
	}
	return ids
}

func orderReleaseArtists(rows []model.ReleaseArtist, ids []string) []model.ReleaseArtist {
	byID := make(map[string]model.ReleaseArtist, len(rows))
	for _, row := range rows {
		byID[row.ArtistID] = row
	}
	ordered := make([]model.ReleaseArtist, 0, len(ids))
	for _, id := range ids {
		if row, ok := byID[id]; ok {
			ordered = append(ordered, row)
		}
	}
	return ordered
}

func mergeReleaseLabels(
	current []model.ReleaseLabel,
	observed, desired []*managev1.ReleaseLabelInput,
	intent *managev1.RelationOrderIntent,
) []model.ReleaseLabel {
	observedRows := make(map[string]*managev1.ReleaseLabelInput, len(observed))
	desiredRows := make(map[string]*managev1.ReleaseLabelInput, len(desired))
	for _, row := range observed {
		if row != nil && row.LabelId != "" {
			observedRows[row.LabelId] = row
		}
	}
	for _, row := range desired {
		if row != nil && row.LabelId != "" {
			desiredRows[row.LabelId] = row
		}
	}

	next := make([]model.ReleaseLabel, 0, len(current)+len(desiredRows))
	currentIDs := make(map[string]struct{}, len(current))
	membershipChanged := false
	for _, row := range current {
		currentIDs[row.LabelID] = struct{}{}
		observedRow, wasObserved := observedRows[row.LabelID]
		desiredRow, isDesired := desiredRows[row.LabelID]
		if wasObserved && !isDesired {
			membershipChanged = true
			continue
		}
		if wasObserved && isDesired && !sameOptionalString(observedRow.CatalogNumber, desiredRow.CatalogNumber) {
			row.CatalogNumber = desiredRow.CatalogNumber
		}
		next = append(next, row)
	}
	for _, row := range desired {
		if row == nil || row.LabelId == "" {
			continue
		}
		if _, wasObserved := observedRows[row.LabelId]; wasObserved {
			continue
		}
		if _, exists := currentIDs[row.LabelId]; exists {
			continue
		}
		next = append(next, model.ReleaseLabel{
			LabelID:       row.LabelId,
			CatalogNumber: row.CatalogNumber,
		})
		currentIDs[row.LabelId] = struct{}{}
		membershipChanged = true
	}

	ids := releaseLabelIDs(next)
	ids, reordered := applyRelationOrderIntent(ids, intent)
	if membershipChanged || reordered {
		next = orderReleaseLabels(next, ids)
		for index := range next {
			next[index].SortOrder = index
		}
	}
	return next
}

func releaseLabelIDs(rows []model.ReleaseLabel) []string {
	ids := make([]string, len(rows))
	for index, row := range rows {
		ids[index] = row.LabelID
	}
	return ids
}

func orderReleaseLabels(rows []model.ReleaseLabel, ids []string) []model.ReleaseLabel {
	byID := make(map[string]model.ReleaseLabel, len(rows))
	for _, row := range rows {
		byID[row.LabelID] = row
	}
	ordered := make([]model.ReleaseLabel, 0, len(ids))
	for _, id := range ids {
		if row, ok := byID[id]; ok {
			ordered = append(ordered, row)
		}
	}
	return ordered
}

func mergeReleaseFormats(
	current []model.ReleaseFormat,
	observed, desired []*managev1.ReleaseFormatInput,
) []model.ReleaseFormat {
	observedRows := make(map[string]*managev1.ReleaseFormatInput, len(observed))
	desiredRows := make(map[string]*managev1.ReleaseFormatInput, len(desired))
	for _, row := range observed {
		if row != nil && row.FormatId != "" {
			observedRows[row.FormatId] = row
		}
	}
	for _, row := range desired {
		if row != nil && row.FormatId != "" {
			desiredRows[row.FormatId] = row
		}
	}

	next := make([]model.ReleaseFormat, 0, len(current)+len(desiredRows))
	currentIDs := make(map[string]struct{}, len(current))
	for _, row := range current {
		currentIDs[row.FormatID] = struct{}{}
		observedRow, wasObserved := observedRows[row.FormatID]
		desiredRow, isDesired := desiredRows[row.FormatID]
		if wasObserved && !isDesired {
			continue
		}
		if wasObserved && isDesired && !sameOptionalString(observedRow.FormatDescription, desiredRow.FormatDescription) {
			row.FormatDescription = desiredRow.FormatDescription
		}
		next = append(next, row)
	}
	for _, row := range desired {
		if row == nil || row.FormatId == "" {
			continue
		}
		if _, wasObserved := observedRows[row.FormatId]; wasObserved {
			continue
		}
		if _, exists := currentIDs[row.FormatId]; exists {
			continue
		}
		next = append(next, model.ReleaseFormat{FormatID: row.FormatId, FormatDescription: row.FormatDescription})
		currentIDs[row.FormatId] = struct{}{}
	}
	return next
}

func mergeReleaseCredits(
	current []model.ReleaseCredit,
	observed, desired []*managev1.ReleaseCreditInput,
	intent *managev1.RelationOrderIntent,
) []model.ReleaseCredit {
	observedRows := make(map[string]*managev1.ReleaseCreditInput, len(observed))
	desiredRows := make(map[string]*managev1.ReleaseCreditInput, len(desired))
	for _, row := range observed {
		if row != nil && row.GetId() != "" {
			observedRows[row.GetId()] = row
		}
	}
	for _, row := range desired {
		if row != nil && row.GetId() != "" {
			desiredRows[row.GetId()] = row
		}
	}

	next := make([]model.ReleaseCredit, 0, len(current)+len(desiredRows))
	currentIDs := make(map[string]struct{}, len(current))
	membershipChanged := false
	for _, row := range current {
		currentIDs[row.ID] = struct{}{}
		observedRow, wasObserved := observedRows[row.ID]
		desiredRow, isDesired := desiredRows[row.ID]
		if wasObserved && !isDesired {
			membershipChanged = true
			continue
		}
		if wasObserved && isDesired {
			applyReleaseCreditAttributeDelta(&row, observedRow, desiredRow)
		}
		next = append(next, row)
	}
	for _, row := range desired {
		if row == nil {
			continue
		}
		id := row.GetId()
		if _, wasObserved := observedRows[id]; wasObserved {
			continue
		}
		if id != "" {
			if _, exists := currentIDs[id]; exists {
				continue
			}
		}
		credit := releaseCreditFromInput(row)
		next = append(next, credit)
		if id != "" {
			currentIDs[id] = struct{}{}
		}
		membershipChanged = true
	}

	ids := releaseCreditIDs(next)
	ids, reordered := applyRelationOrderIntent(ids, intent)
	if membershipChanged || reordered {
		next = orderReleaseCredits(next, ids)
		for index := range next {
			next[index].SortOrder = index
		}
	}
	return next
}

func applyReleaseCreditAttributeDelta(
	current *model.ReleaseCredit,
	observed, desired *managev1.ReleaseCreditInput,
) {
	if !sameOptionalString(observed.ArtistId, desired.ArtistId) {
		current.ArtistID = desired.ArtistId
	}
	if !sameOptionalString(observed.MemberId, desired.MemberId) {
		current.MemberID = desired.MemberId
	}
	if !sameOptionalString(observed.CreditedName, desired.CreditedName) {
		current.CreditedName = desired.CreditedName
	}
	if !sameOptionalString(observed.CreditRole, desired.CreditRole) {
		current.CreditRole = desired.CreditRole
	}
}

func releaseCreditFromInput(input *managev1.ReleaseCreditInput) model.ReleaseCredit {
	credit := model.ReleaseCredit{
		CreditRole:   input.CreditRole,
		SortOrder:    int(input.SortOrder),
		ArtistID:     input.ArtistId,
		MemberID:     input.MemberId,
		CreditedName: input.CreditedName,
	}
	if id := input.GetId(); id != "" {
		credit.ID = id
	}
	return credit
}

func releaseCreditIDs(rows []model.ReleaseCredit) []string {
	ids := make([]string, len(rows))
	for index, row := range rows {
		ids[index] = row.ID
	}
	return ids
}

func orderReleaseCredits(rows []model.ReleaseCredit, ids []string) []model.ReleaseCredit {
	byID := make(map[string]model.ReleaseCredit, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]model.ReleaseCredit, 0, len(ids))
	for _, id := range ids {
		if row, ok := byID[id]; ok {
			ordered = append(ordered, row)
		}
	}
	return ordered
}

func mergeTrackCredits(
	trackID string,
	current []model.TrackCredit,
	observed, desired []*managev1.TrackCreditInput,
) []model.TrackCredit {
	observedRows := make(map[string]*managev1.TrackCreditInput, len(observed))
	desiredRows := make(map[string]*managev1.TrackCreditInput, len(desired))
	for _, row := range observed {
		if row != nil && row.GetId() != "" {
			observedRows[row.GetId()] = row
		}
	}
	for _, row := range desired {
		if row != nil && row.GetId() != "" {
			desiredRows[row.GetId()] = row
		}
	}

	next := make([]model.TrackCredit, 0, len(current)+len(desiredRows))
	currentIDs := make(map[string]struct{}, len(current))
	membershipChanged := false
	for _, row := range current {
		currentIDs[row.ID] = struct{}{}
		observedRow, wasObserved := observedRows[row.ID]
		desiredRow, isDesired := desiredRows[row.ID]
		if wasObserved && !isDesired {
			membershipChanged = true
			continue
		}
		if wasObserved && isDesired {
			applyTrackCreditAttributeDelta(&row, observedRow, desiredRow)
		}
		next = append(next, row)
	}
	for _, row := range desired {
		if row == nil {
			continue
		}
		id := row.GetId()
		if _, wasObserved := observedRows[id]; wasObserved {
			continue
		}
		if id != "" {
			if _, exists := currentIDs[id]; exists {
				continue
			}
		}
		credit, err := newTrackCredit(trackID, row)
		if err != nil {
			continue
		}
		credit.ID = id
		next = append(next, credit)
		if id != "" {
			currentIDs[id] = struct{}{}
		}
		membershipChanged = true
	}
	if membershipChanged {
		for index := range next {
			next[index].SortOrder = index
		}
	}
	return next
}

func applyTrackCreditAttributeDelta(
	current *model.TrackCredit,
	observed, desired *managev1.TrackCreditInput,
) {
	if !sameOptionalString(observed.ArtistId, desired.ArtistId) {
		current.ArtistID = desired.ArtistId
	}
	if !sameOptionalString(observed.MemberId, desired.MemberId) {
		current.MemberID = desired.MemberId
	}
	if !sameOptionalString(observed.CreditedName, desired.CreditedName) {
		current.CreditedName = desired.CreditedName
	}
	if !sameOptionalString(observed.CreditRole, desired.CreditRole) {
		current.CreditRole = desired.CreditRole
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
		updates := map[string]any{}
		if !sameOptionalString(previous.ArtistID, row.ArtistID) {
			updates["artist_id"] = row.ArtistID
		}
		if !sameOptionalString(previous.MemberID, row.MemberID) {
			updates["member_id"] = row.MemberID
		}
		if !sameOptionalString(previous.CreditedName, row.CreditedName) {
			updates["credited_name"] = row.CreditedName
		}
		if !sameOptionalString(previous.CreditRole, row.CreditRole) {
			updates["credit_role"] = row.CreditRole
		}
		if previous.SortOrder != row.SortOrder {
			updates["sort_order"] = row.SortOrder
		}
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
