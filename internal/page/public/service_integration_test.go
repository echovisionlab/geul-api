//go:build integration

package public

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestPublicPageServiceReadsPublishedPageAndHomepageIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	adminID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: adminID, Name: "Public Page Admin"})
	adminMemberID := seedPublicAdminMemberIdentityLink(t, db, adminID, "Public Page Admin")
	adminCtx := publicLegalAdminCtx(adminMemberID, adminID)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	blockStore, err := contentblock.NewGeneratedStore(
		publicPageFileReuseAuthorizer{},
	)
	require.NoError(t, err)
	manageFileService := newPublicReferenceManageFileService(db)
	manageSvc := newPublicPageManageService(db, blockStore, manageFileService)
	internalSvc := pagedomain.NewInternalPageService(
		db,
		publicReferenceAsyncPublisher{},
		publicIntegrationSpiceDB,
		newPublicPageRuntimeForTest(db, "https://cdn.example.com"),
		pagedomain.WithInternalPageDomainAuditWriter(apitelemetry.NewDurableWriter(db)),
		pagedomain.WithInternalPageContentBlockStore(blockStore),
		pagedomain.WithInternalPageContentBlockMediaHydrator(manageFileService),
	)
	publicSvc := newPublicPageService(db, blockStore)

	pageSlug := "public-page-" + suffix
	pageSummary := "Published public page summary " + suffix
	showTitle := false
	pageTitle := "Published Public Page " + suffix
	sourceLocale := "en"
	created, err := manageSvc.CreatePage(adminCtx, connect.NewRequest(&managev1.CreatePageRequest{
		Title:        pageTitle,
		Slug:         &pageSlug,
		Summary:      &pageSummary,
		ShowTitle:    &showTitle,
		SourceLocale: sourceLocale,
	}))
	require.NoError(t, err)
	require.NotNil(t, created.Msg.Document)
	require.NotNil(t, created.Msg.DocumentLayout)

	sessionID := insertPublicPageIntegrationSession(t, db, adminID)
	loaded, err := internalSvc.LoadPageBlockDocument(adminCtx, connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
		PageId: created.Msg.Id,
		Locale: sourceLocale,
		Principal: &intrav1.CollaborationPrincipal{
			SessionId: sessionID,
		},
	}))
	require.NoError(t, err)
	require.NotNil(t, loaded.Msg.Document)
	require.Equal(t, created.Msg.Revision, loaded.Msg.DocumentRevision)

	bodyImageFileID, _ := seedCanonicalPublicFileFixture(t, db, "body.webp", "image/webp", "image")
	externalSectionID := uuid.NewString()
	richSectionID := uuid.NewString()
	fileBlockID := uuid.NewString()
	caption := "Published public page video"
	imageAlt := "Published public page image"
	applied, err := internalSvc.ApplyPageBlockBatch(adminCtx, connect.NewRequest(&intrav1.ApplyPageBlockBatchRequest{
		PageId: created.Msg.Id,
		Batch: &contentv1.PageSectionMutationBatch{
			BlockCatalogFingerprint: loaded.Msg.Document.GetBlockCatalogFingerprint(),
			ExpectedRevision:        loaded.Msg.DocumentRevision,
			BaseMutations: []*contentv1.PageSectionMutation{{
				Operation: &contentv1.PageSectionMutation_Upsert{Upsert: &contentv1.UpsertPageSection{
					Node: &contentv1.PageSectionNode{
						Section: &contentv1.PageSection{
							Id: externalSectionID,
							Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{
								Props: &contentv1.ExternalVideoSectionProps{Uri: "https://video.example.test/watch"},
							}},
						},
						Placement: &contentv1.PageSectionPlacement{Index: 0},
					},
				}},
			}, {
				Operation: &contentv1.PageSectionMutation_Upsert{Upsert: &contentv1.UpsertPageSection{
					Node: &contentv1.PageSectionNode{
						Section: &contentv1.PageSection{
							Id: richSectionID,
							Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
								Props:  &contentv1.RichTextSectionProps{},
								Blocks: &contentv1.RichTextBlockGraph{},
							}},
						},
						Placement: &contentv1.PageSectionPlacement{Index: 1},
					},
				}},
			}, {
				Operation: &contentv1.PageSectionMutation_MutateRichTextBlock{MutateRichTextBlock: &contentv1.MutatePageRichTextBlock{
					SectionId: richSectionID,
					Mutation: &contentv1.RichTextBlockMutation{
						Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
							Node: &contentv1.RichTextBlockNode{
								Block: &contentv1.RichTextBlock{
									Id: fileBlockID,
									Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{
										Props: &contentv1.FileProps{Attachment: &contentv1.FileAttachment{
											State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: bodyImageFileID},
										}},
									}},
								},
								Placement: &contentv1.ContentBlockPlacement{Index: 0},
							},
						}},
					},
				}},
			}},
			LocaleMutationGroups: []*contentv1.PageLocaleMutationGroup{{
				Locale: sourceLocale,
				Mutations: []*contentv1.PageSectionLocaleMutation{{
					Operation: &contentv1.PageSectionLocaleMutation_Upsert{Upsert: &contentv1.UpsertPageSectionLocale{
						Section: &contentv1.PageSectionLocale{
							SectionId: externalSectionID,
							Value: &contentv1.PageSectionLocale_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSectionLocale{
								Props: &contentv1.ExternalVideoSectionLocaleProps{Caption: &caption},
							}},
						},
					}},
				}, {
					Operation: &contentv1.PageSectionLocaleMutation_Upsert{Upsert: &contentv1.UpsertPageSectionLocale{
						Section: &contentv1.PageSectionLocale{
							SectionId: richSectionID,
							Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
								Props:  &contentv1.RichTextSectionLocaleProps{},
								Blocks: &contentv1.RichTextLocaleOverlay{},
							}},
						},
					}},
				}, {
					Operation: &contentv1.PageSectionLocaleMutation_MutateRichTextBlock{MutateRichTextBlock: &contentv1.MutatePageRichTextBlockLocale{
						SectionId: richSectionID,
						Mutation: &contentv1.RichTextBlockLocaleMutation{
							Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
								Block: &contentv1.RichTextBlockLocale{
									BlockId: fileBlockID,
									Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{
										Props: &contentv1.FileLocaleProps{Alt: &imageAlt},
									}},
								},
							}},
						},
					}},
				}},
			}},
			ContributorMemberIds: []string{adminMemberID},
		},
		Locale: sourceLocale,
	}))
	require.NoError(t, err)
	require.True(t, applied.Msg.Changed)
	setPublicDownloadAudience(t, db, fileBlockID, "public")
	require.NotEqual(t, loaded.Msg.DocumentRevision, applied.Msg.DocumentRevision)
	afterApplied, err := internalSvc.LoadPageBlockDocument(adminCtx, connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
		PageId:    created.Msg.Id,
		Locale:    sourceLocale,
		Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	requirePageExternalVideoLocalized(t, afterApplied.Msg.Document, externalSectionID, sourceLocale, caption)

	draftAsAdmin, err := publicSvc.Get(adminCtx, connect.NewRequest(&openv1.GetPageRequest{Slug: pageSlug}))
	require.NoError(t, err)
	require.Equal(t, pageTitle, draftAsAdmin.Msg.Page.GetTitle())
	require.Equal(t, applied.Msg.DocumentRevision, draftAsAdmin.Msg.Page.GetRevision())
	requirePageExternalVideoLocalized(t, draftAsAdmin.Msg.Page.GetDocument(), externalSectionID, sourceLocale, caption)
	requirePageBlockMediaItem(t, draftAsAdmin.Msg.GetBlockMedia(), fileBlockID, bodyImageFileID, true)

	managed, err := manageSvc.GetPage(adminCtx, connect.NewRequest(&managev1.GetPageRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, applied.Msg.DocumentRevision, managed.Msg.GetRevision())
	requirePageExternalVideoAggregate(t, managed.Msg.GetDocument(), externalSectionID, sourceLocale, caption)
	requirePageBlockMediaItem(t, managed.Msg.GetBlockMedia(), fileBlockID, bodyImageFileID, true)

	imageFileID, _ := seedCanonicalPublicFileFixture(t, db, "featured.webp", "image/webp", "image")
	imageResp, err := manageSvc.SetPageFeaturedImage(adminCtx, connect.NewRequest(&managev1.SetPageFeaturedImageRequest{
		PageId: created.Msg.Id,
		FileId: imageFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, imageFileID, imageResp.Msg.GetImageDelivery().GetFileId())
	require.NotEmpty(t, imageResp.Msg.GetImageDelivery().GetInline().GetUrl())

	published, err := manageSvc.PublishPage(adminCtx, connect.NewRequest(&managev1.PublishPageRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.PageStatus_PAGE_STATUS_PUBLISHED, published.Msg.Status)
	require.NotNil(t, published.Msg.PublishedAt)

	setPublicHomepagePage(t, db, adminCtx, created.Msg.Id)

	bySlug, err := publicSvc.Get(context.Background(), connect.NewRequest(&openv1.GetPageRequest{Slug: pageSlug}))
	require.NoError(t, err)
	require.NotNil(t, bySlug.Msg.Page)
	require.Equal(t, created.Msg.Id, bySlug.Msg.Page.GetId())
	require.Equal(t, pageSlug, bySlug.Msg.Page.GetSlug())
	require.Equal(t, "Published Public Page "+suffix, bySlug.Msg.Page.GetTitle())
	require.Equal(t, pageSummary, bySlug.Msg.Page.GetSummary())
	require.False(t, bySlug.Msg.Page.GetShowTitle())
	require.Equal(t, openv1.PageStatus_PAGE_STATUS_PUBLISHED, bySlug.Msg.Page.GetStatus())
	require.Equal(t, created.Msg.DocumentLayout, bySlug.Msg.Page.DocumentLayout)
	require.NotNil(t, bySlug.Msg.Page.PublishedAt)
	require.Equal(t, imageFileID, bySlug.Msg.Page.GetFeaturedImageDelivery().GetFileId())
	require.Empty(t, bySlug.Msg.Page.GetFeaturedImageDelivery().GetInline().GetUrl())
	require.Equal(t, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", imageFileID), bySlug.Msg.Page.GetFeaturedImageDelivery().GetAsset().GetUrl())
	requirePageBlockMediaItem(t, bySlug.Msg.BlockMedia, fileBlockID, bodyImageFileID, false)
	require.Equal(t, applied.Msg.DocumentRevision, bySlug.Msg.Page.GetRevision())
	requirePageExternalVideoLocalized(t, bySlug.Msg.Page.GetDocument(), externalSectionID, sourceLocale, caption)
	require.NotNil(t, bySlug.Msg.Page.LocalizationInfo)
	require.Equal(t, "en", bySlug.Msg.Page.LocalizationInfo.GetDisplayedLocale())
	require.True(t, bySlug.Msg.Page.LocalizationInfo.GetIsOriginal())

	byID, err := publicSvc.Get(context.Background(), connect.NewRequest(&openv1.GetPageRequest{Slug: created.Msg.Id}))
	require.NoError(t, err)
	require.NotNil(t, byID.Msg.Page)
	require.Equal(t, created.Msg.Id, byID.Msg.Page.GetId())
	require.Equal(t, imageFileID, byID.Msg.Page.GetFeaturedImageDelivery().GetFileId())

	homepageBySlash, err := publicSvc.Get(context.Background(), connect.NewRequest(&openv1.GetPageRequest{Slug: "/"}))
	require.NoError(t, err)
	require.NotNil(t, homepageBySlash.Msg.Page)
	require.Equal(t, created.Msg.Id, homepageBySlash.Msg.Page.GetId())
	require.Equal(t, "Published Public Page "+suffix, homepageBySlash.Msg.Page.GetTitle())

	homepageByEmptySlug, err := publicSvc.Get(context.Background(), connect.NewRequest(&openv1.GetPageRequest{}))
	require.NoError(t, err)
	require.NotNil(t, homepageByEmptySlug.Msg.Page)
	require.Equal(t, created.Msg.Id, homepageByEmptySlug.Msg.Page.GetId())

	require.NoError(t, db.Exec("DELETE FROM site_settings WHERE id = 1").Error)
	missingSettingsHomepage, err := publicSvc.Get(context.Background(), connect.NewRequest(&openv1.GetPageRequest{Slug: "/"}))
	require.NoError(t, err)
	require.Nil(t, missingSettingsHomepage.Msg.Page)
}

func newPublicPageManageService(
	db *gorm.DB,
	blocks *contentblock.Store,
	fileService *publicPageFileGateway,
) *pagedomain.PageService {
	return pagedomain.NewPageService(
		db,
		newPublicPageRuntimeForTest(db, "https://cdn.example.com"),
		fileService,
		publicReferenceAsyncPublisher{},
		&publicReferenceIdentityManager{identity: &auth.Identity{}},
		publicIntegrationSpiceDB,
		pagedomain.WithPageContentBlockStore(blocks),
		pagedomain.WithPageContentBlockMediaHydrator(fileService),
	)
}

func newPublicPageService(db *gorm.DB, blocks *contentblock.Store) *PageService {
	files := &publicPageFileGateway{db: db, cdnDomain: "https://cdn.example.com"}
	access := publicPageAccessFixture{}
	return NewPageService(
		db,
		access,
		access,
		publicPageMediaFixture{files: files},
		WithPageContentBlockStore(blocks),
	)
}

type publicPageAccessFixture struct{}

func (publicPageAccessFixture) CanViewPageDraft(ctx context.Context, pageID string) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := policyv1.Page.View(pageID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return publicIntegrationSpiceDB.Can(ctx, decision)
}

func (publicPageAccessFixture) RequirePageShareLinkAccess(
	ctx context.Context,
	db *gorm.DB,
	token string,
	password string,
	pageID string,
) (*model.ShareLink, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errs.NotFoundMsg("page not found")
	}
	link, err := sharelink.ValidateForEntity(
		ctx, db, token, password,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, pageID,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.NotFoundMsg("page not found")
	}
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("validate page share link: %w", err))
	}
	return link, nil
}

type publicPageMediaFixture struct{ files *publicPageFileGateway }

func (f publicPageMediaFixture) HydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return f.files.HydrateAuthorizedContentBlockMedia(ctx, items)
}

func (f publicPageMediaFixture) ResolvePageFeaturedImageDelivery(
	ctx context.Context,
	fileID *string,
) (*commonv1.MediaDelivery, error) {
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil, nil
	}
	id := strings.TrimSpace(*fileID)
	deliveries, err := f.files.ResolvePublicDisplayMedia(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	return deliveries[id], nil
}

func (f publicPageMediaFixture) ResolvePageOGAsset(
	ctx context.Context,
	sourceAssetID *string,
	localizedAssetID *string,
) (*commonv1.AssetRef, error) {
	return f.files.ResolveReadyOGAsset(ctx, sourceAssetID, localizedAssetID)
}

func insertPublicPageIntegrationSession(t *testing.T, db *gorm.DB, identityID string) string {
	t.Helper()
	sessionID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.sessions (
			id, identity_id, active, authenticated_at, expires_at,
			created_at, updated_at, nid, authentication_methods
		)
		SELECT ?::uuid, id, TRUE, NOW(), NOW() + INTERVAL '1 hour',
		       NOW(), NOW(), nid, '[]'::jsonb
		FROM kratos.identities
		WHERE id = ?::uuid
	`, sessionID, identityID).Error)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM kratos.sessions WHERE id = ?::uuid", sessionID).Error })
	return sessionID
}

func requirePageExternalVideoAggregate(
	t *testing.T,
	document *contentv1.PageDocument,
	sectionID string,
	locale string,
	caption string,
) {
	t.Helper()
	require.NotNil(t, document)
	require.Equal(t, contentv1.ContentBlockCatalogFingerprint, document.GetBlockCatalogFingerprint())
	require.Equal(t, locale, document.GetSourceLocale())
	section := pageSectionByID(t, document.GetBase().GetNodes(), sectionID)
	require.Equal(t, "https://video.example.test/watch", section.GetExternalVideo().GetProps().GetUri())
	require.Len(t, document.GetLocaleOverlays(), 1)
	require.Equal(t, locale, document.GetLocaleOverlays()[0].GetLocale())
	localizedSection := pageLocaleSectionByID(t, document.GetLocaleOverlays()[0].GetSections(), sectionID)
	require.Equal(t, caption, localizedSection.GetExternalVideo().GetProps().GetCaption())
}

func requirePageExternalVideoLocalized(
	t *testing.T,
	document *contentv1.LocalizedPageDocument,
	sectionID string,
	locale string,
	caption string,
) {
	t.Helper()
	require.NotNil(t, document)
	require.Equal(t, contentv1.ContentBlockCatalogFingerprint, document.GetBlockCatalogFingerprint())
	require.Equal(t, locale, document.GetLocale())
	section := pageSectionByID(t, document.GetBase().GetNodes(), sectionID)
	require.Equal(t, "https://video.example.test/watch", section.GetExternalVideo().GetProps().GetUri())
	require.Equal(t, locale, document.GetLocaleOverlay().GetLocale())
	localizedSection := pageLocaleSectionByID(t, document.GetLocaleOverlay().GetSections(), sectionID)
	require.Equal(t, caption, localizedSection.GetExternalVideo().GetProps().GetCaption())
}

func pageSectionByID(
	t *testing.T,
	nodes []*contentv1.PageSectionNode,
	sectionID string,
) *contentv1.PageSection {
	t.Helper()
	for _, node := range nodes {
		if node.GetSection().GetId() == sectionID {
			return node.GetSection()
		}
	}
	t.Fatalf("Page section %s not found", sectionID)
	return nil
}

func pageLocaleSectionByID(
	t *testing.T,
	sections []*contentv1.PageSectionLocale,
	sectionID string,
) *contentv1.PageSectionLocale {
	t.Helper()
	for _, section := range sections {
		if section.GetSectionId() == sectionID {
			return section
		}
	}
	t.Fatalf("localized Page section %s not found", sectionID)
	return nil
}

func requirePageBlockMediaItem(
	t *testing.T,
	items []*contentv1.ContentBlockMediaItem,
	blockID string,
	fileID string,
	expectDownload bool,
) {
	t.Helper()
	require.Len(t, items, 1)
	item := items[0]
	require.Equal(t, blockID, item.GetSelector().GetBlockId())
	require.Equal(t, "file", item.GetSelector().GetReferencePath())
	require.Equal(t, fileID, item.GetAttachment().GetActiveFileId())
	require.NotNil(t, item.GetDelivery())
	require.Equal(t, contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE, item.GetDownloadAvailability())
	if expectDownload {
		require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD, item.GetDownloadAction())
	} else {
		require.NotEqual(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_UNSPECIFIED, item.GetDownloadAction())
	}
}

func setPublicHomepagePage(t *testing.T, db *gorm.DB, ctx context.Context, pageID string) {
	t.Helper()
	result := db.WithContext(ctx).Exec(`
		INSERT INTO site_settings (id, homepage_page_id, default_map_theme_id)
		SELECT 1, ?::uuid, id
		FROM map_theme
		ORDER BY created_at, id
		LIMIT 1
		ON CONFLICT (id) DO UPDATE SET homepage_page_id = EXCLUDED.homepage_page_id
	`, pageID)
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)
}
