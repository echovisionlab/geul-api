// Package hls validates and uploads generated HLS packages.
package hls

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type hlsFile struct {
	name        string
	path        string
	contentType string
	size        int64
}

// Package is a validated HLS output package.
type Package struct {
	files        []hlsFile
	manifest     hlsFile
	manifestHash []byte
	totalSize    int64
}

// MasterManifestName is the required HLS master playlist filename.
const MasterManifestName = "master.m3u8"

// Inspect validates and indexes an HLS output directory.
func Inspect(dir string) (*Package, error) {
	return inspectHLSPackageWithFS(osHLSFileSystem{}, dir)
}

type hlsFileSystem interface {
	ReadDir(string) ([]os.DirEntry, error)
	ReadFile(string) ([]byte, error)
}

type osHLSFileSystem struct{}

func (osHLSFileSystem) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }
func (osHLSFileSystem) ReadFile(path string) ([]byte, error)       { return os.ReadFile(path) }

func inspectHLSPackageWithFS(fs hlsFileSystem, dir string) (*Package, error) {
	files, err := readHLSFiles(fs, dir)
	if err != nil {
		return nil, err
	}
	manifest, ok := files[MasterManifestName]
	if !ok {
		return nil, fmt.Errorf("HLS manifest %s is missing", MasterManifestName)
	}
	if err := validateHLSReferences(fs, files); err != nil {
		return nil, err
	}

	ordered, totalSize := orderHLSFiles(files)
	manifestBytes, err := fs.ReadFile(manifest.path)
	if err != nil {
		return nil, fmt.Errorf("read HLS manifest: %w", err)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	return &Package{
		files:        ordered,
		manifest:     manifest,
		manifestHash: manifestHash[:],
		totalSize:    totalSize,
	}, nil
}

func readHLSFiles(fs hlsFileSystem, dir string) (map[string]hlsFile, error) {
	entries, err := fs.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read HLS output directory: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("HLS output directory is empty")
	}

	files := make(map[string]hlsFile, len(entries))
	for _, entry := range entries {
		file, err := inspectHLSFile(dir, entry)
		if err != nil {
			return nil, err
		}
		files[file.name] = file
	}
	return files, nil
}

func inspectHLSFile(dir string, entry os.DirEntry) (hlsFile, error) {
	name := entry.Name()
	if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
		return hlsFile{}, fmt.Errorf("HLS output must contain regular files only: %s", name)
	}
	if name != filepath.Base(name) || url.PathEscape(name) != name || strings.HasSuffix(strings.ToLower(name), ".tmp") {
		return hlsFile{}, fmt.Errorf("invalid HLS object name %q", name)
	}
	contentType, err := hlsContentType(name)
	if err != nil {
		return hlsFile{}, err
	}
	info, err := entry.Info()
	if err != nil {
		return hlsFile{}, fmt.Errorf("stat HLS object %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return hlsFile{}, fmt.Errorf("HLS object must be a non-empty regular file: %s", name)
	}
	return hlsFile{
		name:        name,
		path:        filepath.Join(dir, name),
		contentType: contentType,
		size:        info.Size(),
	}, nil
}

func validateHLSReferences(fs hlsFileSystem, files map[string]hlsFile) error {
	walker := referenceWalker{
		fs:        fs,
		files:     files,
		reachable: make(map[string]struct{}, len(files)),
		visiting:  make(map[string]bool),
	}
	if err := walker.visit(MasterManifestName); err != nil {
		return err
	}
	orphaned := orphanedHLSFiles(files, walker.reachable)
	if len(orphaned) > 0 {
		return fmt.Errorf("HLS output contains unreferenced objects: %s", strings.Join(orphaned, ", "))
	}
	return nil
}

type referenceWalker struct {
	fs        hlsFileSystem
	files     map[string]hlsFile
	reachable map[string]struct{}
	visiting  map[string]bool
}

func (w *referenceWalker) visit(name string) error {
	if w.visiting[name] {
		return fmt.Errorf("HLS playlist cycle at %s", name)
	}
	if _, seen := w.reachable[name]; seen {
		return nil
	}
	w.visiting[name] = true
	references, err := w.readReferences(name)
	if err != nil {
		return err
	}
	w.reachable[name] = struct{}{}
	for _, reference := range references {
		if err := w.visitReference(name, reference); err != nil {
			return err
		}
	}
	w.visiting[name] = false
	return nil
}

func (w *referenceWalker) readReferences(name string) ([]string, error) {
	playlistBytes, err := w.fs.ReadFile(w.files[name].path)
	if err != nil {
		return nil, fmt.Errorf("read HLS playlist %s: %w", name, err)
	}
	references, err := parseHLSPlaylist(playlistBytes)
	if err != nil {
		return nil, fmt.Errorf("validate HLS playlist %s: %w", name, err)
	}
	return references, nil
}

func (w *referenceWalker) visitReference(playlist, reference string) error {
	if _, found := w.files[reference]; !found {
		return fmt.Errorf("HLS reference %s from %s is missing", reference, playlist)
	}
	if !strings.EqualFold(filepath.Ext(reference), ".m3u8") {
		w.reachable[reference] = struct{}{}
		return nil
	}
	return w.visit(reference)
}

func orphanedHLSFiles(files map[string]hlsFile, reachable map[string]struct{}) []string {
	var orphaned []string
	for name := range files {
		if _, ok := reachable[name]; !ok {
			orphaned = append(orphaned, name)
		}
	}
	sort.Strings(orphaned)
	return orphaned
}

func orderHLSFiles(files map[string]hlsFile) ([]hlsFile, int64) {
	ordered := make([]hlsFile, 0, len(files)-1)
	var totalSize int64
	for name, file := range files {
		totalSize += file.size
		if name != MasterManifestName {
			ordered = append(ordered, file)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		leftPlaylist := strings.EqualFold(filepath.Ext(ordered[i].name), ".m3u8")
		rightPlaylist := strings.EqualFold(filepath.Ext(ordered[j].name), ".m3u8")
		if leftPlaylist != rightPlaylist {
			return !leftPlaylist
		}
		return ordered[i].name < ordered[j].name
	})
	return ordered, totalSize
}
