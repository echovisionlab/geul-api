package programevent

// aiDocumentContentUpdate is the content.updated signal of a committed AI
// document mutation.
func aiDocumentContentUpdate(command AIDocumentCommand, result AIDocumentResult) ContentUpdate {
	fields := make([]string, 0, 3)
	if command.Metadata.SetTitle {
		fields = append(fields, "title")
	}
	if command.Metadata.SetSummary {
		fields = append(fields, "summary")
	}
	if command.Batch == nil || len(command.Batch.Upserts) != 0 || len(command.Batch.Deletes) != 0 ||
		len(command.Batch.Reorders) != 0 || len(command.Batch.LocaleGroups) != 0 {
		fields = append(fields, "content")
	}
	return ContentUpdate{
		EventID:              command.EventID,
		Fields:               fields,
		DocumentRevision:     result.DocumentRevision,
		Contributors:         []string{command.ContributorMemberID.String()},
		Source:               SourceAI,
		Locale:               command.RequestedLocale,
		LocaleDeleted:        command.DeleteTranslation,
		TargetRevision:       result.TargetRevision,
		DocumentStateChanged: command.RequestedLocale == command.ObservedSourceLocale,
	}
}
