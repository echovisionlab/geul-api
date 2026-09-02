package model

import (
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ManagedRasterFinalMaxSize = 30 * 1024 * 1024
)

// UploadConfig represents upload constraints for a specific upload type
type UploadConfig struct {
	PermittedMimeTypes []string `json:"permittedMimeTypes"`
	MaxSize            int64    `json:"maxSize"`
	MinSize            int64    `json:"minSize,omitempty"`
	Label              string   `json:"label,omitempty"`
	Description        string   `json:"description,omitempty"`
}

// DefaultUploadConfigs provides default configuration for each upload type
// MIME types are fixed and cannot be modified by admin - only minSize/maxSize are configurable
var DefaultUploadConfigs = map[managev1.UploadType]*UploadConfig{
	managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE: {
		PermittedMimeTypes: []string{
			"image/jpeg", "image/png", "image/gif", "image/webp", "image/avif", "image/svg+xml", "image/x-icon",
			"audio/mpeg", "audio/wav", "audio/ogg", "audio/webm", "audio/flac", "audio/aac", "audio/mp4", "audio/aiff", "audio/x-aiff",
			"video/mp4", "video/webm", "video/quicktime", "video/x-msvideo", "video/x-matroska",
			"model/gltf-binary",
			"application/pdf", "application/msword", "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			"application/vnd.ms-excel", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"application/vnd.ms-powerpoint", "application/vnd.openxmlformats-officedocument.presentationml.presentation",
			"application/zip", "application/x-zip-compressed", "application/x-rar-compressed", "application/x-7z-compressed",
			"text/plain", "text/csv", "application/json",
		},
		MaxSize:     8 * 1024 * 1024 * 1024,
		MinSize:     1,
		Label:       "General File",
		Description: "Standalone File Manager source",
	},
	managev1.UploadType_UPLOAD_TYPE_USER_AVATAR: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1, // Prevent empty files
		Label:              "User Avatar",
	},
	managev1.UploadType_UPLOAD_TYPE_ARTIST_IMAGE: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1, // Prevent empty files
		Label:              "Artist Image",
	},
	managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/gif", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1, // Prevent empty files
		Label:              "Editor Image",
	},
	managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO: {
		PermittedMimeTypes: []string{
			"video/mp4",
			"video/webm",
			"video/quicktime",
			"video/x-msvideo",
			"video/x-matroska",
		},
		MaxSize: 8 * 1024 * 1024 * 1024, // 8GB
		MinSize: 1,                      // Prevent empty files
		Label:   "Editor Video",
	},
	managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO: {
		PermittedMimeTypes: []string{
			"audio/mpeg",
			"audio/wav",
			"audio/ogg",
			"audio/webm",
			"audio/flac",
			"audio/aac",
			"audio/mp4",
			"audio/aiff",
			"audio/x-aiff",
		},
		MaxSize: 4 * 1024 * 1024 * 1024, // 4GB
		MinSize: 1,                      // Prevent empty files
		Label:   "Editor Audio",
	},
	managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT: {
		PermittedMimeTypes: []string{
			"application/pdf",
			"application/msword",
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			"application/vnd.ms-excel",
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"application/vnd.ms-powerpoint",
			"application/vnd.openxmlformats-officedocument.presentationml.presentation",
			"application/zip",
			"application/x-zip-compressed",
			"application/x-rar-compressed",
			"application/x-7z-compressed",
			"text/plain",
			"text/csv",
			"application/json",
		},
		MaxSize: 500 * 1024 * 1024, // 500MB
		MinSize: 1,                 // Prevent empty files
		Label:   "Editor Attachment",
	},
	managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH: {
		PermittedMimeTypes: []string{
			"model/gltf-binary",
		},
		MaxSize: 50 * 1024 * 1024, // 50MB
		MinSize: 1,                // Prevent empty files
		Label:   "Editor Mesh",
	},
	managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1, // Prevent empty files
		Label:              "Featured Image",
	},
	managev1.UploadType_UPLOAD_TYPE_WORK_FEATURED_IMAGE: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1, // Prevent empty files
		Label:              "Work Featured Image",
	},
	managev1.UploadType_UPLOAD_TYPE_SERIES_FEATURED_IMAGE: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1, // Prevent empty files
		Label:              "Series Featured Image",
	},
	managev1.UploadType_UPLOAD_TYPE_FORM_FEATURED_IMAGE: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1, // Prevent empty files
		Label:              "Form Featured Image",
	},
	managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1,
		Label:              "Program Event Poster",
	},
	managev1.UploadType_UPLOAD_TYPE_MAP_IMAGE: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1,
		Label:              "Map Image",
	},
	managev1.UploadType_UPLOAD_TYPE_RELEASE_ARTWORK: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1, // Prevent empty files
		Label:              "Release Artwork",
	},
	managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO: {
		PermittedMimeTypes: []string{"audio/mpeg", "audio/wav", "audio/flac", "audio/aac", "audio/ogg", "audio/mp4", "audio/aiff", "audio/x-aiff"},
		MaxSize:            4 * 1024 * 1024 * 1024, // 4GB
		MinSize:            1,                      // Prevent empty files
		Label:              "Track Audio",
		Description:        "Original audio file for transcoding",
	},
	managev1.UploadType_UPLOAD_TYPE_LABEL_IMAGE: {
		PermittedMimeTypes: []string{"image/png", "image/webp", "image/svg+xml"},
		MaxSize:            5 * 1024 * 1024, // 5MB
		MinSize:            1,               // Prevent empty files
		Label:              "Label Image",
	},
	managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO: {
		PermittedMimeTypes: []string{"image/png", "image/webp", "image/svg+xml"},
		MaxSize:            5 * 1024 * 1024, // 5MB
		MinSize:            1,               // Prevent empty files
		Label:              "Client Logo",
	},
	managev1.UploadType_UPLOAD_TYPE_SITE_LOGO: {
		PermittedMimeTypes: []string{"image/svg+xml", "image/png"},
		MaxSize:            2 * 1024 * 1024, // 2MB
		MinSize:            1,               // Prevent empty files
		Label:              "Site Logo",
	},
	managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON: {
		PermittedMimeTypes: []string{"image/png", "image/x-icon", "image/svg+xml"},
		MaxSize:            2 * 1024 * 1024, // 2MB
		MinSize:            1,               // Prevent empty files
		Label:              "Site Favicon",
	},
	managev1.UploadType_UPLOAD_TYPE_SITE_LOADER: {
		PermittedMimeTypes: []string{"image/gif", "image/png", "image/webp"},
		MaxSize:            100 * 1024, // 100KB
		MinSize:            1,          // Prevent empty files
		Label:              "Site Loader",
	},
	managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND: {
		PermittedMimeTypes: []string{"image/jpeg", "image/png", "image/webp", "image/avif"},
		MaxSize:            ManagedRasterFinalMaxSize,
		MinSize:            1,
		Label:              "Site OG Background",
	},
}
