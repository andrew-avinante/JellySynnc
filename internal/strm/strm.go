package strm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrewavinante/JellySynnc/internal/config"
	"github.com/andrewavinante/JellySynnc/internal/jellyfin"
)

func BuildURL(filePath string, remote config.RemoteConfig) (string, error) {
	if !strings.HasPrefix(filePath, remote.GetRootStart()) {
		return "", fmt.Errorf("file path %q does not start with root_start %q", filePath, remote.GetRootStart())
	}
	remainder := strings.TrimPrefix(filePath, remote.GetRootStart())
	base := strings.TrimRight(remote.GetStrmURL(), "/")
	return base + remainder, nil
}

func BuildPath(item jellyfin.MediaItem, localPath string, remote config.RemoteConfig, resolutionSuffix string) (string, error) {
	if !strings.HasPrefix(item.FilePath, remote.GetRootStart()) {
		return "", fmt.Errorf("file path %q does not start with root_start %q", item.FilePath, remote.GetRootStart())
	}

	relative := strings.TrimPrefix(item.FilePath, remote.GetRootStart())
	relative = strings.TrimPrefix(relative, "/")

	parts := strings.Split(relative, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected path structure: %q", relative)
	}

	// Strip the remote library name (first component)
	mediaParts := parts[1:]

	filename := mediaParts[len(mediaParts)-1]
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))

	var dir string
	if len(mediaParts) > 1 {
		dir = filepath.Join(append([]string{localPath}, mediaParts[:len(mediaParts)-1]...)...)
	} else {
		dir = localPath
	}

	if resolutionSuffix != "" {
		stem = stem + " - " + resolutionSuffix
	}

	return filepath.Join(dir, stem+".strm"), nil
}

func Write(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directories for %q: %w", path, err)
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func Delete(path string) error {
	if filepath.Ext(path) != ".strm" {
		return fmt.Errorf("refusing to delete non-.strm file: %s", path)
	}
	return os.Remove(path)
}
