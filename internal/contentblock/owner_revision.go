package contentblock

import "gorm.io/gorm"

// ReadOwnerDocumentRevision joins the document to the owner's scoped query.
// Both relations are read in the caller's transaction and one statement.
func ReadOwnerDocumentRevision(owner *gorm.DB) (string, error) {
	var row struct{ Revision string }
	err := owner.Select("document.revision").Joins("JOIN content_document AS document ON document.id = owner.content_document_id").Take(&row).Error
	return row.Revision, err
}
