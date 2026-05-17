package strm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andrewavinante/JellySynnc/internal/config"
	"github.com/andrewavinante/JellySynnc/internal/jellyfin"
)

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		remote   config.RemoteConfig
		want     string
		wantErr  bool
	}{
		{
			name:     "basic",
			filePath: "/media/Movies/Film.mkv",
			remote:   config.RemoteConfig{RootStart: "/media", StrmURL: "http://stream.example.com"},
			want:     "http://stream.example.com/Movies/Film.mkv",
		},
		{
			name:     "strm_url trailing slash trimmed",
			filePath: "/media/Movies/Film.mkv",
			remote:   config.RemoteConfig{RootStart: "/media", StrmURL: "http://stream.example.com/"},
			want:     "http://stream.example.com/Movies/Film.mkv",
		},
		{
			name:     "multiple trailing slashes trimmed",
			filePath: "/media/Movies/Film.mkv",
			remote:   config.RemoteConfig{RootStart: "/media", StrmURL: "http://stream.example.com///"},
			want:     "http://stream.example.com/Movies/Film.mkv",
		},
		{
			name:     "nested path",
			filePath: "/media/Movies/Action/Die Hard/Die Hard.mkv",
			remote:   config.RemoteConfig{RootStart: "/media", StrmURL: "http://stream.example.com"},
			want:     "http://stream.example.com/Movies/Action/Die Hard/Die Hard.mkv",
		},
		{
			name:     "empty root_start matches any path",
			filePath: "/anything/goes/here.mkv",
			remote:   config.RemoteConfig{RootStart: "", StrmURL: "http://stream.example.com"},
			want:     "http://stream.example.com/anything/goes/here.mkv",
		},
		{
			name:     "path does not start with root_start",
			filePath: "/other/Movies/Film.mkv",
			remote:   config.RemoteConfig{RootStart: "/media", StrmURL: "http://stream.example.com"},
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildURL(tc.filePath, tc.remote)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildPath(t *testing.T) {
	tests := []struct {
		name             string
		item             jellyfin.MediaItem
		localPath        string
		remote           config.RemoteConfig
		resolutionSuffix string
		want             string
		wantErr          bool
	}{
		{
			name:      "movie in subdirectory",
			item:      jellyfin.MediaItem{FilePath: "/media/Movies/Die Hard (1988)/Die Hard.mkv"},
			localPath: "/local/movies",
			remote:    config.RemoteConfig{RootStart: "/media"},
			want:      "/local/movies/Die Hard (1988)/Die Hard.strm",
		},
		{
			name:      "movie at library root (flat)",
			item:      jellyfin.MediaItem{FilePath: "/media/Movies/Die Hard.mkv"},
			localPath: "/local/movies",
			remote:    config.RemoteConfig{RootStart: "/media"},
			want:      "/local/movies/Die Hard.strm",
		},
		{
			name:      "deeply nested path",
			item:      jellyfin.MediaItem{FilePath: "/media/TV/Shows/Drama/Series/S01/episode.mkv"},
			localPath: "/local/tv",
			remote:    config.RemoteConfig{RootStart: "/media"},
			want:      "/local/tv/Shows/Drama/Series/S01/episode.strm",
		},
		{
			name:             "with resolution suffix",
			item:             jellyfin.MediaItem{FilePath: "/media/Movies/Film/film.mkv"},
			localPath:        "/local/movies",
			remote:           config.RemoteConfig{RootStart: "/media"},
			resolutionSuffix: "1080p",
			want:             "/local/movies/Film/film - 1080p.strm",
		},
		{
			name:      "no resolution suffix when empty",
			item:      jellyfin.MediaItem{FilePath: "/media/Movies/Film/film.mkv"},
			localPath: "/local/movies",
			remote:    config.RemoteConfig{RootStart: "/media"},
			want:      "/local/movies/Film/film.strm",
		},
		{
			name:      "file with multiple dots in name",
			item:      jellyfin.MediaItem{FilePath: "/media/Movies/Movie.2160p.HDR/movie.2160p.mkv"},
			localPath: "/local/movies",
			remote:    config.RemoteConfig{RootStart: "/media"},
			want:      "/local/movies/Movie.2160p.HDR/movie.2160p.strm",
		},
		{
			name:      "root_start with trailing slash",
			item:      jellyfin.MediaItem{FilePath: "/media/Movies/Film/film.mkv"},
			localPath: "/local/movies",
			remote:    config.RemoteConfig{RootStart: "/media/"},
			want:      "/local/movies/Film/film.strm",
		},
		{
			name:      "path does not start with root_start",
			item:      jellyfin.MediaItem{FilePath: "/other/Movies/Film/film.mkv"},
			localPath: "/local/movies",
			remote:    config.RemoteConfig{RootStart: "/media"},
			wantErr:   true,
		},
		{
			name:      "too few path components after stripping root",
			item:      jellyfin.MediaItem{FilePath: "/media/OnlyLibrary"},
			localPath: "/local",
			remote:    config.RemoteConfig{RootStart: "/media"},
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildPath(tc.item, tc.localPath, tc.remote, tc.resolutionSuffix)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Normalize separators for cross-platform safety.
			got = filepath.ToSlash(got)
			want := filepath.ToSlash(tc.want)
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestWrite(t *testing.T) {
	t.Run("creates file and directories", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "subdir", "movie.strm")
		if err := Write(path, "http://stream.example.com/movie.mkv"); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if string(got) != "http://stream.example.com/movie.mkv" {
			t.Errorf("content = %q, want %q", got, "http://stream.example.com/movie.mkv")
		}
	})

	t.Run("creates deeply nested directories", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "a", "b", "c", "movie.strm")
		if err := Write(path, "content"); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("file not found: %v", err)
		}
	})

	t.Run("overwrites existing file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "movie.strm")
		if err := Write(path, "original"); err != nil {
			t.Fatalf("first Write() error = %v", err)
		}
		if err := Write(path, "updated"); err != nil {
			t.Fatalf("second Write() error = %v", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "updated" {
			t.Errorf("content = %q, want %q", got, "updated")
		}
	})

	t.Run("empty content", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.strm")
		if err := Write(path, ""); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		got, _ := os.ReadFile(path)
		if len(got) != 0 {
			t.Errorf("expected empty file, got %q", got)
		}
	})
}

func TestDelete(t *testing.T) {
	t.Run("deletes existing strm file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "movie.strm")
		if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
			t.Fatalf("setup WriteFile: %v", err)
		}
		if err := Delete(path); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("file should not exist after Delete")
		}
	})

	t.Run("refuses non-strm extension mp4", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "movie.mp4")
		if err := Delete(path); err == nil {
			t.Fatal("expected error for .mp4, got nil")
		}
	})

	t.Run("refuses non-strm extension txt", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "notes.txt")
		if err := Delete(path); err == nil {
			t.Fatal("expected error for .txt, got nil")
		}
	})

	t.Run("refuses no extension", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "noext")
		if err := Delete(path); err == nil {
			t.Fatal("expected error for no extension, got nil")
		}
	})

	t.Run("returns error for missing strm file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "missing.strm")
		if err := Delete(path); err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}
