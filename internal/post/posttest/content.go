package posttest

import (
	"context"
	"maps"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/versionrestore"
	"github.com/google/uuid"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// The fake content document keeps only what the Post use cases branch on:
// the revision compare-and-set, the fence that runs first, whether a batch
// changes anything, and that only a source-locale change advances the
// shared revision. Block validation stays the content store's.

func (s *Store) document(documentID uuid.UUID) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[documentID]
	if !ok {
		return Document{}, contentblock.ErrDocumentNotFound
	}
	return document, nil
}

func (s *Store) putDocument(document Document) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[document.ID] = document
}

func (s *Store) nextResult() (contentblock.Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.contentResult) == 0 {
		return contentblock.Result{}, false
	}
	result := s.contentResult[0]
	s.contentResult = s.contentResult[1:]
	return result, true
}

func snapshotOf(document Document, sourceLocale string) contentblock.Snapshot {
	return contentblock.Snapshot{
		Document:     contentblock.Document{ID: document.ID, Profile: document.Profile, Revision: document.Revision},
		SourceLocale: sourceLocale, Blocks: document.Blocks,
	}
}

func (s *Store) CreateContentDocument(_ context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	if err := s.fail("CreateContentDocument"); err != nil {
		return contentblock.Snapshot{}, err
	}
	document := Document{ID: input.ID, Profile: input.Profile, Revision: uuid.New()}
	if document.ID == uuid.Nil {
		document.ID = uuid.New()
	}
	s.putDocument(document)
	return snapshotOf(document, input.SourceLocale), nil
}

func (s *Store) LockedContentSnapshot(_ context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	if err := s.fail("LockedContentSnapshot"); err != nil {
		return contentblock.Snapshot{}, err
	}
	document, err := s.document(documentID)
	if err != nil {
		return contentblock.Snapshot{}, err
	}
	return snapshotOf(document, sourceLocale), nil
}

func (s *Store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.LockedContentSnapshot(ctx, documentID, sourceLocale)
}

func (s *Store) LockContentDocument(_ context.Context, documentID uuid.UUID) error {
	_, err := s.document(documentID)
	return err
}

func (s *Store) ContentRevision(_ context.Context, documentID uuid.UUID) (uuid.UUID, error) {
	document, err := s.document(documentID)
	return document.Revision, err
}

// admit runs the fence and the revision compare-and-set of a write.
func (s *Store) admit(ctx context.Context, documentID, expected uuid.UUID, fence post.ContentFence) (Document, contentblock.DomainContext, error) {
	domain, err := fence(ctx, s, documentID)
	if err != nil {
		return Document{}, contentblock.DomainContext{}, err
	}
	document, err := s.document(documentID)
	if err != nil {
		return Document{}, contentblock.DomainContext{}, err
	}
	if expected != document.Revision {
		return Document{}, contentblock.DomainContext{}, &contentblock.StaleRevisionError{CurrentRevision: document.Revision}
	}
	return document, domain, nil
}

func batchLocales(batch contentblock.Batch) []string {
	locales := make([]string, 0, len(batch.LocaleGroups))
	for _, group := range batch.LocaleGroups {
		locales = append(locales, group.Locale)
	}
	return locales
}

func batchChanges(batch contentblock.Batch) bool {
	return len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || len(batch.LocaleGroups) != 0
}

// commit stores the document after a changed result, which advances its
// revision unless the result names one.
func (s *Store) commit(document Document, result contentblock.Result) contentblock.Result {
	if result.Changed && result.DocumentRevision == uuid.Nil {
		result.DocumentRevision = uuid.New()
	}
	if result.DocumentRevision == uuid.Nil {
		result.DocumentRevision = document.Revision
	}
	document.Revision = result.DocumentRevision
	document.Locales = maps.Clone(document.Locales)
	for _, locale := range result.ChangedLocales {
		if document.Locales == nil {
			document.Locales = map[string]bool{}
		}
		document.Locales[locale] = true
	}
	s.putDocument(document)
	return result
}

// apply runs a batch: one with any mutation changes the document and
// advances its revision; touching the source locale or the shared graph
// changes the translation source.
func (s *Store) apply(ctx context.Context, batch contentblock.Batch, fence post.ContentFence, effect post.MetadataEffect) (contentblock.Result, error) {
	document, domain, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	var metadata contentblock.MetadataEffect
	if effect != nil {
		if metadata, err = effect(ctx, s); err != nil {
			return contentblock.Result{}, err
		}
	}
	if result, ok := s.nextResult(); ok {
		return s.commit(document, result), nil
	}
	changed := batchChanges(batch)
	if !changed && !metadata.Changed {
		return contentblock.Result{DocumentRevision: document.Revision}, nil
	}
	sourceChanged := len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || metadata.AffectsTranslationSource
	for _, locale := range batchLocales(batch) {
		sourceChanged = sourceChanged || locale == domain.SourceLocale
	}
	return s.commit(document, contentblock.Result{
		Changed: true, ContentChanged: changed, MetadataChanged: metadata.Changed,
		TranslationSourceChanged: sourceChanged, ChangedLocales: append(batchLocales(batch), metadata.ChangedLocales...),
	}), nil
}

func (s *Store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence post.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	return s.apply(ctx, batch, fence, nil)
}

func (s *Store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence post.ContentFence, effect post.MetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatchWithMetadata"); err != nil {
		return contentblock.Result{}, err
	}
	return s.apply(ctx, batch, fence, effect)
}

// ApplyTargetLocaleContentBatch implements post.Store. A target-locale
// batch never advances the shared revision.
func (s *Store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence post.ContentFence, effect post.TargetMetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyTargetLocaleContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	document, _, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	contentChanged := len(batch.LocaleGroups) != 0
	metadata, err := effect(ctx, s, contentChanged)
	if err != nil {
		return contentblock.Result{}, err
	}
	result := contentblock.Result{
		DocumentRevision: document.Revision, Changed: contentChanged || metadata.Changed,
		ContentChanged: contentChanged, MetadataChanged: metadata.Changed,
	}
	if result.Changed {
		result.ChangedLocales = []string{locale}
	}
	return result, nil
}

func (s *Store) ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence post.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ReplaceContentSnapshot"); err != nil {
		return contentblock.Result{}, err
	}
	document, _, err := s.admit(ctx, replace.DocumentID, replace.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	if result, ok := s.nextResult(); ok {
		return s.commit(document, result), nil
	}
	document.Blocks = replace.Blocks
	return s.commit(document, contentblock.Result{Changed: true, ContentChanged: true, TranslationSourceChanged: true}), nil
}

func (s *Store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence post.ContentFence, effect post.MetadataEffect) (contentblock.AdvanceResult, error) {
	if err := s.fail("AdvanceContentRevision"); err != nil {
		return contentblock.AdvanceResult{}, err
	}
	document, _, err := s.admit(ctx, input.DocumentID, input.ExpectedRevision, fence)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	metadata, err := effect(ctx, s)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	if !metadata.Changed {
		return contentblock.AdvanceResult{DocumentRevision: document.Revision}, nil
	}
	result := s.commit(document, contentblock.Result{Changed: true})
	return contentblock.AdvanceResult{
		DocumentRevision: result.DocumentRevision, Changed: true, TranslationSourceChanged: metadata.AffectsTranslationSource,
	}, nil
}

func (s *Store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence post.ContentFence) error {
	if err := s.fail("DeleteContentDocument"); err != nil {
		return err
	}
	if _, err := fence(ctx, s, documentID); err != nil {
		return err
	}
	if _, err := s.document(documentID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.documents, documentID)
	return nil
}

// RestoreVersion runs versionrestore.Restore's steps on the fake.
func (s *Store) RestoreVersion(ctx context.Context, restore post.VersionRestore) (versionrestore.Outcome, error) {
	if err := s.fail("RestoreVersion"); err != nil {
		return versionrestore.Outcome{}, err
	}
	current := func() (versionrestore.Source, Document, error) {
		source, err := restore.LoadSource(ctx, s)
		if err != nil {
			return versionrestore.Source{}, Document{}, err
		}
		document, err := s.document(restore.DocumentID)
		return source, document, err
	}
	source, document, err := current()
	if err != nil {
		return versionrestore.Outcome{}, err
	}
	var outcome versionrestore.Outcome
	if source.Locale != restore.Version.Locale {
		if err := restore.SwitchSourceLocale(ctx, s, source.Locale, document.Revision); err != nil {
			return versionrestore.Outcome{}, err
		}
		outcome.SourceLocaleSwitched = true
		if source, document, err = current(); err != nil {
			return versionrestore.Outcome{}, err
		}
	}
	outcome.TitleChanged = !optional.Equal(source.Title, restore.Version.Title)
	outcome.SummaryChanged = !optional.Equal(source.Summary, restore.Version.Summary)
	input, err := restore.Replacement(ctx, s, document.Revision)
	if err != nil {
		return versionrestore.Outcome{}, err
	}
	replaced, err := s.ReplaceContentSnapshot(ctx, input, restore.Fence)
	if err != nil {
		return versionrestore.Outcome{}, err
	}
	outcome.ContentChanged, outcome.DocumentRevision = replaced.Changed, replaced.DocumentRevision
	if !outcome.TitleChanged && !outcome.SummaryChanged {
		return outcome, nil
	}
	if replaced.Changed {
		return outcome, restore.UpdateMetadata(ctx, s)
	}
	advanced, err := s.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: restore.DocumentID, ExpectedRevision: replaced.DocumentRevision}, restore.Fence,
		func(ctx context.Context, store post.Store) (contentblock.MetadataEffect, error) {
			if err := restore.UpdateMetadata(ctx, store); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
		})
	if err != nil {
		return versionrestore.Outcome{}, err
	}
	outcome.DocumentRevision = advanced.DocumentRevision
	return outcome, nil
}

func (s *Store) UnavailableVersionAttachments(context.Context, *contentv1.LocalizedRichTextDocument) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.unavailable), nil
}

func (s *Store) LockAttachableFiles(_ context.Context, fileIDs []string) error {
	if err := s.fail("LockAttachableFiles"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockedFiles = append(s.lockedFiles, fileIDs...)
	return nil
}

func (s *Store) FindFile(_ context.Context, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.files[fileID] {
		return post.ErrFileNotFound
	}
	return nil
}

func (s *Store) ReadyOgAssets(_ context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ready := map[string]*mediaasset.Reference{}
	for _, assetID := range assetIDs {
		if ref, ok := s.readyAssets[assetID]; ok {
			ready[assetID] = ref
		}
	}
	return ready, nil
}

func (s *Store) BlockMedia(_ context.Context, _ string, documentID uuid.UUID, _ *auth.UserInfo) ([]*contentv1.ContentBlockMediaItem, error) {
	if err := s.fail("BlockMedia"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blockMedia[documentID], nil
}

func (s *Store) RequestOG(_ context.Context, postID, locale string, force bool, reason string) (*string, error) {
	if err := s.fail("RequestOG"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogRequests = append(s.ogRequests, OGRequest{PostID: postID, Locale: locale, Reason: reason, Force: force})
	if s.OGRunID == "" {
		return nil, nil
	}
	return text(s.OGRunID), nil
}

func (s *Store) CancelOG(_ context.Context, postID string) error {
	if err := s.fail("CancelOG"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogCancels = append(s.ogCancels, postID)
	return nil
}

func (s *Store) DeleteShareLinks(_ context.Context, postID string) error {
	if err := s.fail("DeleteShareLinks"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareLinks = append(s.shareLinks, postID)
	return nil
}

func (s *Store) ResolveSessionPrincipal(_ context.Context, sessionID string) (*auth.UserInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	principal, ok := s.sessions[sessionID]
	if !ok {
		return nil, auth.ErrSessionPrincipalInvalid
	}
	return principal, nil
}

func (s *Store) LockContributorMembers(_ context.Context, memberIDs []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	locked := 0
	for _, memberID := range memberIDs {
		if _, ok := s.members[memberID]; ok {
			locked++
		}
	}
	return locked, nil
}

func (s *Store) ActiveContributorIdentities(_ context.Context, memberIDs []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identities := map[string]string{}
	for _, memberID := range memberIDs {
		if member, ok := s.members[memberID]; ok && member.Active && member.IdentityID != "" {
			identities[memberID] = member.IdentityID
		}
	}
	return identities, nil
}

func (s *Store) MemberPreferredLocale(_ context.Context, memberID string) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.members[memberID].PreferredLocale, nil
}

func (s *Store) DefaultTranslationLocale(context.Context) (string, error) {
	if s.DefaultLocale == "" {
		return "en", nil
	}
	return s.DefaultLocale, nil
}
