package domain

import "strconv"

type UploadType int32

const (
	UploadUnspecified         UploadType = 0
	UploadUserAvatar          UploadType = 1
	UploadArtistImage         UploadType = 2
	UploadEditorImage         UploadType = 3
	UploadEditorVideo         UploadType = 4
	UploadEditorAudio         UploadType = 5
	UploadEditorAttachment    UploadType = 6
	UploadFeaturedImage       UploadType = 7
	UploadWorkFeaturedImage   UploadType = 8
	UploadReleaseArtwork      UploadType = 9
	UploadTrackAudio          UploadType = 10
	UploadLabelImage          UploadType = 11
	UploadClientLogo          UploadType = 12
	UploadSiteLogo            UploadType = 13
	UploadSiteFavicon         UploadType = 14
	UploadSiteLoader          UploadType = 15
	UploadSeriesFeaturedImage UploadType = 16
	UploadFormFeaturedImage   UploadType = 17
	UploadSiteOGBackground    UploadType = 18
	UploadProgramEventPoster  UploadType = 19
	UploadEditorMesh          UploadType = 20
	UploadMapImage            UploadType = 21
	UploadGeneralFile         UploadType = 22
)

func (value UploadType) String() string {
	switch value {
	case UploadUnspecified:
		return "UPLOAD_TYPE_UNSPECIFIED"
	case UploadUserAvatar:
		return "UPLOAD_TYPE_USER_AVATAR"
	case UploadArtistImage:
		return "UPLOAD_TYPE_ARTIST_IMAGE"
	case UploadEditorImage:
		return "UPLOAD_TYPE_EDITOR_IMAGE"
	case UploadEditorVideo:
		return "UPLOAD_TYPE_EDITOR_VIDEO"
	case UploadEditorAudio:
		return "UPLOAD_TYPE_EDITOR_AUDIO"
	case UploadEditorAttachment:
		return "UPLOAD_TYPE_EDITOR_ATTACHMENT"
	case UploadFeaturedImage:
		return "UPLOAD_TYPE_FEATURED_IMAGE"
	case UploadWorkFeaturedImage:
		return "UPLOAD_TYPE_WORK_FEATURED_IMAGE"
	case UploadReleaseArtwork:
		return "UPLOAD_TYPE_RELEASE_ARTWORK"
	case UploadTrackAudio:
		return "UPLOAD_TYPE_TRACK_AUDIO"
	case UploadLabelImage:
		return "UPLOAD_TYPE_LABEL_IMAGE"
	case UploadClientLogo:
		return "UPLOAD_TYPE_CLIENT_LOGO"
	case UploadSiteLogo:
		return "UPLOAD_TYPE_SITE_LOGO"
	case UploadSiteFavicon:
		return "UPLOAD_TYPE_SITE_FAVICON"
	case UploadSiteLoader:
		return "UPLOAD_TYPE_SITE_LOADER"
	case UploadSeriesFeaturedImage:
		return "UPLOAD_TYPE_SERIES_FEATURED_IMAGE"
	case UploadFormFeaturedImage:
		return "UPLOAD_TYPE_FORM_FEATURED_IMAGE"
	case UploadSiteOGBackground:
		return "UPLOAD_TYPE_SITE_OG_BACKGROUND"
	case UploadProgramEventPoster:
		return "UPLOAD_TYPE_PROGRAM_EVENT_POSTER"
	case UploadEditorMesh:
		return "UPLOAD_TYPE_EDITOR_MESH"
	case UploadMapImage:
		return "UPLOAD_TYPE_MAP_IMAGE"
	case UploadGeneralFile:
		return "UPLOAD_TYPE_GENERAL_FILE"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

func ParseUploadType(stored string) (UploadType, bool) {
	switch stored {
	case "UPLOAD_TYPE_UNSPECIFIED":
		return UploadUnspecified, true
	case "UPLOAD_TYPE_USER_AVATAR":
		return UploadUserAvatar, true
	case "UPLOAD_TYPE_ARTIST_IMAGE":
		return UploadArtistImage, true
	case "UPLOAD_TYPE_EDITOR_IMAGE":
		return UploadEditorImage, true
	case "UPLOAD_TYPE_EDITOR_VIDEO":
		return UploadEditorVideo, true
	case "UPLOAD_TYPE_EDITOR_AUDIO":
		return UploadEditorAudio, true
	case "UPLOAD_TYPE_EDITOR_ATTACHMENT":
		return UploadEditorAttachment, true
	case "UPLOAD_TYPE_FEATURED_IMAGE":
		return UploadFeaturedImage, true
	case "UPLOAD_TYPE_WORK_FEATURED_IMAGE":
		return UploadWorkFeaturedImage, true
	case "UPLOAD_TYPE_RELEASE_ARTWORK":
		return UploadReleaseArtwork, true
	case "UPLOAD_TYPE_TRACK_AUDIO":
		return UploadTrackAudio, true
	case "UPLOAD_TYPE_LABEL_IMAGE":
		return UploadLabelImage, true
	case "UPLOAD_TYPE_CLIENT_LOGO":
		return UploadClientLogo, true
	case "UPLOAD_TYPE_SITE_LOGO":
		return UploadSiteLogo, true
	case "UPLOAD_TYPE_SITE_FAVICON":
		return UploadSiteFavicon, true
	case "UPLOAD_TYPE_SITE_LOADER":
		return UploadSiteLoader, true
	case "UPLOAD_TYPE_SERIES_FEATURED_IMAGE":
		return UploadSeriesFeaturedImage, true
	case "UPLOAD_TYPE_FORM_FEATURED_IMAGE":
		return UploadFormFeaturedImage, true
	case "UPLOAD_TYPE_SITE_OG_BACKGROUND":
		return UploadSiteOGBackground, true
	case "UPLOAD_TYPE_PROGRAM_EVENT_POSTER":
		return UploadProgramEventPoster, true
	case "UPLOAD_TYPE_EDITOR_MESH":
		return UploadEditorMesh, true
	case "UPLOAD_TYPE_MAP_IMAGE":
		return UploadMapImage, true
	case "UPLOAD_TYPE_GENERAL_FILE":
		return UploadGeneralFile, true
	default:
		return 0, false
	}
}

func (value UploadType) Valid() bool {
	switch value {
	case UploadUnspecified, UploadUserAvatar, UploadArtistImage, UploadEditorImage, UploadEditorVideo, UploadEditorAudio, UploadEditorAttachment, UploadFeaturedImage, UploadWorkFeaturedImage, UploadReleaseArtwork, UploadTrackAudio, UploadLabelImage, UploadClientLogo, UploadSiteLogo, UploadSiteFavicon, UploadSiteLoader, UploadSeriesFeaturedImage, UploadFormFeaturedImage, UploadSiteOGBackground, UploadProgramEventPoster, UploadEditorMesh, UploadMapImage, UploadGeneralFile:
		return true
	default:
		return false
	}
}

type EntityType int32

const (
	EntityUnspecified  EntityType = 0
	EntityPost         EntityType = 1
	EntityPage         EntityType = 2
	EntityWork         EntityType = 3
	EntityTrack        EntityType = 4
	EntityProgramEvent EntityType = 5
	EntityFile         EntityType = 6
)

func (value EntityType) String() string {
	switch value {
	case EntityUnspecified:
		return "TRANSCODE_ENTITY_TYPE_UNSPECIFIED"
	case EntityPost:
		return "TRANSCODE_ENTITY_TYPE_POST"
	case EntityPage:
		return "TRANSCODE_ENTITY_TYPE_PAGE"
	case EntityWork:
		return "TRANSCODE_ENTITY_TYPE_WORK"
	case EntityTrack:
		return "TRANSCODE_ENTITY_TYPE_TRACK"
	case EntityProgramEvent:
		return "TRANSCODE_ENTITY_TYPE_PROGRAM_EVENT"
	case EntityFile:
		return "TRANSCODE_ENTITY_TYPE_FILE"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

func ParseEntityType(stored string) (EntityType, bool) {
	switch stored {
	case "TRANSCODE_ENTITY_TYPE_UNSPECIFIED":
		return EntityUnspecified, true
	case "TRANSCODE_ENTITY_TYPE_POST":
		return EntityPost, true
	case "TRANSCODE_ENTITY_TYPE_PAGE":
		return EntityPage, true
	case "TRANSCODE_ENTITY_TYPE_WORK":
		return EntityWork, true
	case "TRANSCODE_ENTITY_TYPE_TRACK":
		return EntityTrack, true
	case "TRANSCODE_ENTITY_TYPE_PROGRAM_EVENT":
		return EntityProgramEvent, true
	case "TRANSCODE_ENTITY_TYPE_FILE":
		return EntityFile, true
	default:
		return 0, false
	}
}

func (value EntityType) Valid() bool {
	switch value {
	case EntityUnspecified, EntityPost, EntityPage, EntityWork, EntityTrack, EntityProgramEvent, EntityFile:
		return true
	default:
		return false
	}
}

type IngestSource int32

const (
	IngestSourceUnspecified  IngestSource = 0
	IngestSourceDirectUpload IngestSource = 1
	IngestSourceRemoteURL    IngestSource = 2
)

func (value IngestSource) String() string {
	switch value {
	case IngestSourceUnspecified:
		return "FILE_INGEST_SOURCE_UNSPECIFIED"
	case IngestSourceDirectUpload:
		return "FILE_INGEST_SOURCE_DIRECT_UPLOAD"
	case IngestSourceRemoteURL:
		return "FILE_INGEST_SOURCE_REMOTE_URL"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

type IngestMediaKind int32

const (
	IngestMediaUnspecified      IngestMediaKind = 0
	IngestMediaEditorImage      IngestMediaKind = 1
	IngestMediaEditorAudio      IngestMediaKind = 2
	IngestMediaEditorVideo      IngestMediaKind = 3
	IngestMediaEditorAttachment IngestMediaKind = 4
	IngestMediaTrackAudio       IngestMediaKind = 5
	IngestMediaOther            IngestMediaKind = 6
	IngestMediaEditorMesh       IngestMediaKind = 7
)

func (value IngestMediaKind) String() string {
	switch value {
	case IngestMediaUnspecified:
		return "FILE_INGEST_MEDIA_KIND_UNSPECIFIED"
	case IngestMediaEditorImage:
		return "FILE_INGEST_MEDIA_KIND_EDITOR_IMAGE"
	case IngestMediaEditorAudio:
		return "FILE_INGEST_MEDIA_KIND_EDITOR_AUDIO"
	case IngestMediaEditorVideo:
		return "FILE_INGEST_MEDIA_KIND_EDITOR_VIDEO"
	case IngestMediaEditorAttachment:
		return "FILE_INGEST_MEDIA_KIND_EDITOR_ATTACHMENT"
	case IngestMediaTrackAudio:
		return "FILE_INGEST_MEDIA_KIND_TRACK_AUDIO"
	case IngestMediaOther:
		return "FILE_INGEST_MEDIA_KIND_OTHER"
	case IngestMediaEditorMesh:
		return "FILE_INGEST_MEDIA_KIND_EDITOR_MESH"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

type IngestFailureReason int32

const (
	IngestFailureUnspecified   IngestFailureReason = 0
	IngestFailureAborted       IngestFailureReason = 1
	IngestFailureRejected      IngestFailureReason = 2
	IngestFailureStorageFailed IngestFailureReason = 3
	IngestFailureInternal      IngestFailureReason = 4
	IngestFailureExpired       IngestFailureReason = 5
)

func (value IngestFailureReason) String() string {
	switch value {
	case IngestFailureUnspecified:
		return "FILE_INGEST_FAILURE_REASON_UNSPECIFIED"
	case IngestFailureAborted:
		return "FILE_INGEST_FAILURE_REASON_ABORTED"
	case IngestFailureRejected:
		return "FILE_INGEST_FAILURE_REASON_REJECTED"
	case IngestFailureStorageFailed:
		return "FILE_INGEST_FAILURE_REASON_STORAGE_FAILED"
	case IngestFailureInternal:
		return "FILE_INGEST_FAILURE_REASON_INTERNAL"
	case IngestFailureExpired:
		return "FILE_INGEST_FAILURE_REASON_EXPIRED"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

type ClientMediaKind int32

const (
	ClientMediaUnspecified ClientMediaKind = 0
	ClientMediaAudio       ClientMediaKind = 1
	ClientMediaVideo       ClientMediaKind = 2
)

func (value ClientMediaKind) String() string {
	switch value {
	case ClientMediaUnspecified:
		return "CLIENT_MEDIA_KIND_UNSPECIFIED"
	case ClientMediaAudio:
		return "CLIENT_MEDIA_KIND_AUDIO"
	case ClientMediaVideo:
		return "CLIENT_MEDIA_KIND_VIDEO"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

type MeshCompressionMethod int32

const (
	MeshCompressionUnspecified MeshCompressionMethod = 0
	MeshCompressionDraco       MeshCompressionMethod = 1
)

func (value MeshCompressionMethod) String() string {
	switch value {
	case MeshCompressionUnspecified:
		return "MESH_OPTIMIZATION_COMPRESSION_METHOD_UNSPECIFIED"
	case MeshCompressionDraco:
		return "MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

type MeshProfile int32

const (
	MeshProfileUnspecified    MeshProfile = 0
	MeshProfileDracoWebpV1    MeshProfile = 1
	MeshProfileParticleMeshV1 MeshProfile = 2
)

func (value MeshProfile) String() string {
	switch value {
	case MeshProfileUnspecified:
		return "MESH_OPTIMIZATION_PROFILE_UNSPECIFIED"
	case MeshProfileDracoWebpV1:
		return "MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1"
	case MeshProfileParticleMeshV1:
		return "MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

type LibrarySortField int32

const (
	LibrarySortUnspecified LibrarySortField = 0
	LibrarySortName        LibrarySortField = 1
	LibrarySortCreatedAt   LibrarySortField = 2
	LibrarySortUpdatedAt   LibrarySortField = 3
	LibrarySortFileSize    LibrarySortField = 4
)

func (value LibrarySortField) String() string {
	switch value {
	case LibrarySortUnspecified:
		return "FILE_MANAGER_SORT_FIELD_UNSPECIFIED"
	case LibrarySortName:
		return "FILE_MANAGER_SORT_FIELD_NAME"
	case LibrarySortCreatedAt:
		return "FILE_MANAGER_SORT_FIELD_CREATED_AT"
	case LibrarySortUpdatedAt:
		return "FILE_MANAGER_SORT_FIELD_UPDATED_AT"
	case LibrarySortFileSize:
		return "FILE_MANAGER_SORT_FIELD_FILE_SIZE"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

type SortOrder int32

const (
	SortUnspecified SortOrder = 0
	SortAsc         SortOrder = 1
	SortDesc        SortOrder = 2
)

func (value SortOrder) String() string {
	switch value {
	case SortUnspecified:
		return "SORT_ORDER_UNSPECIFIED"
	case SortAsc:
		return "SORT_ORDER_ASC"
	case SortDesc:
		return "SORT_ORDER_DESC"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}

type DownloadAudience int32

const (
	DownloadAudienceUnspecified   DownloadAudience = 0
	DownloadAudienceDisabled      DownloadAudience = 1
	DownloadAudiencePublic        DownloadAudience = 2
	DownloadAudienceAuthenticated DownloadAudience = 3
	DownloadAudienceRestricted    DownloadAudience = 4
)

func (value DownloadAudience) String() string {
	switch value {
	case DownloadAudienceUnspecified:
		return "FILE_DOWNLOAD_AUDIENCE_UNSPECIFIED"
	case DownloadAudienceDisabled:
		return "FILE_DOWNLOAD_AUDIENCE_DISABLED"
	case DownloadAudiencePublic:
		return "FILE_DOWNLOAD_AUDIENCE_PUBLIC"
	case DownloadAudienceAuthenticated:
		return "FILE_DOWNLOAD_AUDIENCE_AUTHENTICATED"
	case DownloadAudienceRestricted:
		return "FILE_DOWNLOAD_AUDIENCE_RESTRICTED"
	default:
		return strconv.FormatInt(int64(value), 10)
	}
}
