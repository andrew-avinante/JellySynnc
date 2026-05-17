package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrew-avinante/JellySynnc/internal/config"
	dbpkg "github.com/andrew-avinante/JellySynnc/internal/db"
	"github.com/andrew-avinante/JellySynnc/internal/jellyfin"
	"github.com/andrew-avinante/JellySynnc/internal/migrations"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// ---------------------------------------------------------------------------
// Pure-function unit tests
// ---------------------------------------------------------------------------

func TestFindExistingLocalPath(t *testing.T) {
	tests := []struct {
		name             string
		providerKey      string
		itemProviderIDs  map[string]string
		providerKeyToDir map[string]string
		want             string
	}{
		{
			name:             "direct provider key match",
			providerKey:      "tmdb:12345",
			itemProviderIDs:  map[string]string{"tmdb": "12345"},
			providerKeyToDir: map[string]string{"tmdb:12345": "/local/movies"},
			want:             "/local/movies",
		},
		{
			name:             "match via item provider IDs cross-lookup",
			providerKey:      "tmdb:12345",
			itemProviderIDs:  map[string]string{"tmdb": "12345", "imdb": "tt0000001"},
			providerKeyToDir: map[string]string{"imdb:tt0000001": "/local/movies"},
			want:             "/local/movies",
		},
		{
			name:             "not found returns empty string",
			providerKey:      "tmdb:99999",
			itemProviderIDs:  map[string]string{"tmdb": "99999"},
			providerKeyToDir: map[string]string{"tmdb:11111": "/local/movies"},
			want:             "",
		},
		{
			name:             "empty provider key to dir map",
			providerKey:      "tmdb:12345",
			itemProviderIDs:  map[string]string{"tmdb": "12345"},
			providerKeyToDir: map[string]string{},
			want:             "",
		},
		{
			name:             "direct match takes precedence over cross-lookup",
			providerKey:      "tmdb:12345",
			itemProviderIDs:  map[string]string{"tmdb": "12345", "imdb": "tt0000001"},
			providerKeyToDir: map[string]string{"tmdb:12345": "/direct", "imdb:tt0000001": "/cross"},
			want:             "/direct",
		},
		{
			name:             "nil item provider IDs",
			providerKey:      "tmdb:12345",
			itemProviderIDs:  nil,
			providerKeyToDir: map[string]string{"imdb:tt0000001": "/local"},
			want:             "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findExistingLocalPath(tc.providerKey, tc.itemProviderIDs, tc.providerKeyToDir)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMarshalProviderIDs(t *testing.T) {
	tests := []struct {
		name    string
		ids     map[string]string
		wantErr bool
	}{
		{
			name: "single entry",
			ids:  map[string]string{"tmdb": "12345"},
		},
		{
			name: "multiple entries",
			ids:  map[string]string{"tmdb": "12345", "imdb": "tt0000001"},
		},
		{
			name: "empty map",
			ids:  map[string]string{},
		},
		{
			name: "nil map",
			ids:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := marshalProviderIDs(tc.ids)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Round-trip: unmarshal and compare.
			var decoded map[string]string
			if err := json.Unmarshal([]byte(got), &decoded); err != nil {
				t.Fatalf("invalid JSON output %q: %v", got, err)
			}
			for k, v := range tc.ids {
				if decoded[k] != v {
					t.Errorf("decoded[%q] = %q, want %q", k, decoded[k], v)
				}
			}
		})
	}
}

func TestPickWinner(t *testing.T) {
	makeCandidate := func(remoteID, encoding, resolution string) candidate {
		return candidate{
			Remote: config.RemoteConfig{ID: remoteID},
			Item:   jellyfin.MediaItem{Encoding: encoding, Resolution: resolution},
		}
	}

	tests := []struct {
		name             string
		tieBreakerFields []config.TieBreaker
		candidates       []candidate
		wantRemoteID     string
	}{
		{
			name:         "single candidate always wins",
			candidates:   []candidate{makeCandidate("remote1", "h265", "1080p")},
			wantRemoteID: "remote1",
		},
		{
			name:         "no tie-breaker returns first candidate",
			candidates:   []candidate{makeCandidate("remote1", "h264", "720p"), makeCandidate("remote2", "h265", "1080p")},
			wantRemoteID: "remote1",
		},
		{
			name:             "tie-breaker on encoding selects matching candidate",
			tieBreakerFields: []config.TieBreaker{{Field: "encoding", Value: "h265"}},
			candidates:       []candidate{makeCandidate("remote1", "h264", "1080p"), makeCandidate("remote2", "h265", "1080p")},
			wantRemoteID:     "remote2",
		},
		{
			name:             "tie-breaker on resolution selects matching candidate",
			tieBreakerFields: []config.TieBreaker{{Field: "resolution", Value: "4K"}},
			candidates:       []candidate{makeCandidate("remote1", "h265", "1080p"), makeCandidate("remote2", "h265", "4K")},
			wantRemoteID:     "remote2",
		},
		{
			name:             "tie-breaker is case-insensitive",
			tieBreakerFields: []config.TieBreaker{{Field: "Encoding", Value: "H265"}},
			candidates:       []candidate{makeCandidate("remote1", "h264", "1080p"), makeCandidate("remote2", "h265", "1080p")},
			wantRemoteID:     "remote2",
		},
		{
			name:             "first tie-breaker match wins, second ignored",
			tieBreakerFields: []config.TieBreaker{{Field: "encoding", Value: "h265"}, {Field: "resolution", Value: "4K"}},
			candidates:       []candidate{makeCandidate("remote1", "h265", "1080p"), makeCandidate("remote2", "h264", "4K")},
			wantRemoteID:     "remote1",
		},
		{
			name:             "no tie-breaker match falls back to first candidate",
			tieBreakerFields: []config.TieBreaker{{Field: "encoding", Value: "av1"}},
			candidates:       []candidate{makeCandidate("remote1", "h264", "1080p"), makeCandidate("remote2", "h265", "1080p")},
			wantRemoteID:     "remote1",
		},
		{
			name:             "tie-breaker matches first candidate",
			tieBreakerFields: []config.TieBreaker{{Field: "encoding", Value: "h264"}},
			candidates:       []candidate{makeCandidate("remote1", "h264", "1080p"), makeCandidate("remote2", "h265", "1080p")},
			wantRemoteID:     "remote1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Syncer{
				cfg: &config.Config{TieBreakerFields: tc.tieBreakerFields},
			}
			got := s.pickWinner(tc.candidates)
			if got.Remote.GetID() != tc.wantRemoteID {
				t.Errorf("winner remote ID = %q, want %q", got.Remote.GetID(), tc.wantRemoteID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration tests for Syncer.Run
// ---------------------------------------------------------------------------

// jellyfinMockServer types for constructing JSON responses that match the
// Jellyfin REST API format that the jellyfin-go SDK parses.

type jfVirtualFolder struct {
	ItemId         string `json:"ItemId"`
	Name           string `json:"Name"`
	CollectionType string `json:"CollectionType"`
}

type jfMediaStream struct {
	Type   string `json:"Type"`
	Height int    `json:"Height"`
	Codec  string `json:"Codec"`
}

type jfMediaSource struct {
	Path         string          `json:"Path"`
	MediaStreams []jfMediaStream `json:"MediaStreams"`
}

type jfItem struct {
	Id           string            `json:"Id"`
	Name         string            `json:"Name"`
	Type         string            `json:"Type"`
	ProviderIds  map[string]string `json:"ProviderIds"`
	MediaSources []jfMediaSource   `json:"MediaSources"`
}

// newJellyfinServer creates an httptest.Server that speaks the Jellyfin REST
// API subset used by the syncer (VirtualFolders + Items endpoints).
func newJellyfinServer(t *testing.T, libs []jfVirtualFolder, itemsByLib map[string][]jfItem) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/Library/VirtualFolders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(libs); err != nil {
			t.Logf("encode VirtualFolders: %v", err)
		}
	})

	mux.HandleFunc("/Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		parentID := q.Get("parentId")
		if parentID == "" {
			parentID = q.Get("ParentId")
		}
		items := itemsByLib[parentID]
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"Items":            items,
			"TotalRecordCount": len(items),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("encode Items: %v", err)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// setupTestDB creates an in-memory SQLite database with migrations applied.
func setupTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	database, err := dbpkg.Connect(":memory:")
	if err != nil {
		t.Fatalf("db.Connect: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := dbpkg.Migrate(database, migrations.FS); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return database
}

func TestRun_AddsNewItem(t *testing.T) {
	tmpDir := t.TempDir()

	targetSrv := newJellyfinServer(t, nil, nil)

	remoteSrv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "lib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"lib1": {{
				Id:          "item1",
				Name:        "Test Movie",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "12345"},
				MediaSources: []jfMediaSource{{
					Path:         "/media/Movies/Test Movie (2020)/movie.mkv",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 1080, Codec: "hevc"}},
				}},
			}},
		},
	)

	database := setupTestDB(t)

	cfg := &config.Config{
		Target: config.TargetConfig{URL: targetSrv.URL, APIKey: "key"},
		Remotes: []config.RemoteConfig{{
			ID:        "remote1",
			APIURL:    remoteSrv.URL,
			StrmURL:   "http://stream.example.com",
			APIKey:    "key",
			RootStart: "/media",
			LibraryMappings: []config.LibraryMapping{{
				RemoteName: "Movies",
				LocalPath:  tmpDir,
			}},
		}},
	}

	s := New(cfg, database)
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Strm file should be created.
	expectedStrm := filepath.Join(tmpDir, "Test Movie (2020)", "movie.strm")
	content, err := os.ReadFile(expectedStrm)
	if err != nil {
		t.Fatalf("strm file not created at %q: %v", expectedStrm, err)
	}
	wantContent := "http://stream.example.com/Movies/Test Movie (2020)/movie.mkv"
	if string(content) != wantContent {
		t.Errorf("strm content = %q, want %q", string(content), wantContent)
	}

	// DB record should be inserted.
	items, err := dbpkg.GetAllSyncedItems(database)
	if err != nil {
		t.Fatalf("GetAllSyncedItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 synced item, got %d", len(items))
	}
	if items[0].RemoteID != "remote1" {
		t.Errorf("RemoteID = %q, want %q", items[0].RemoteID, "remote1")
	}
	if items[0].StrmPath != expectedStrm {
		t.Errorf("StrmPath = %q, want %q", items[0].StrmPath, expectedStrm)
	}
}

func TestRun_SkipsItemAlreadyOnTarget(t *testing.T) {
	tmpDir := t.TempDir()

	// Target reports the same item (via provider ID).
	targetSrv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "tlib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"tlib1": {{
				Id:          "target-item1",
				Name:        "Existing Movie",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "12345"},
				MediaSources: []jfMediaSource{{
					Path:         "/target/Movies/Existing Movie/movie.strm",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 0, Codec: ""}},
				}},
			}},
		},
	)

	remoteSrv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "rlib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"rlib1": {{
				Id:          "remote-item1",
				Name:        "Existing Movie",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "12345"},
				MediaSources: []jfMediaSource{{
					Path:         "/media/Movies/Existing Movie/movie.mkv",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 1080, Codec: "hevc"}},
				}},
			}},
		},
	)

	database := setupTestDB(t)

	cfg := &config.Config{
		Target: config.TargetConfig{URL: targetSrv.URL, APIKey: "key"},
		Remotes: []config.RemoteConfig{{
			ID:        "remote1",
			APIURL:    remoteSrv.URL,
			StrmURL:   "http://stream.example.com",
			APIKey:    "key",
			RootStart: "/media",
			LibraryMappings: []config.LibraryMapping{{
				RemoteName: "Movies",
				LocalPath:  tmpDir,
			}},
		}},
	}

	s := New(cfg, database)
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// No strm files should have been written.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no files in tmpDir, found %d", len(entries))
	}

	// No DB records.
	items, _ := dbpkg.GetAllSyncedItems(database)
	if len(items) != 0 {
		t.Errorf("expected 0 synced items, got %d", len(items))
	}
}

func TestRun_RemovesStaleItem(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create the strm file that should be deleted.
	strmPath := filepath.Join(tmpDir, "gone.strm")
	if err := os.WriteFile(strmPath, []byte("old-content"), 0644); err != nil {
		t.Fatalf("setup WriteFile: %v", err)
	}

	// Remote returns a different item (not the one in DB).
	remoteSrv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "lib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"lib1": {{
				Id:          "item-still-here",
				Name:        "Still Present Movie",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "11111"},
				MediaSources: []jfMediaSource{{
					Path:         "/media/Movies/Still Present Movie/movie.mkv",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 1080, Codec: "h264"}},
				}},
			}},
		},
	)

	targetSrv := newJellyfinServer(t, nil, nil)
	database := setupTestDB(t)

	// Pre-insert a DB record for the item that will be "gone".
	provIDs, _ := marshalProviderIDs(map[string]string{"tmdb": "99999"})
	if err := dbpkg.InsertSyncedItem(database, dbpkg.SyncedItem{
		ID:             uuid.New().String(),
		RemoteID:       "remote1",
		JellyfinItemID: "old-jellyfin-id",
		ProviderIDs:    provIDs,
		Resolution:     "1080p",
		Encoding:       "h265",
		StrmPath:       strmPath,
	}); err != nil {
		t.Fatalf("InsertSyncedItem: %v", err)
	}

	cfg := &config.Config{
		Target: config.TargetConfig{URL: targetSrv.URL, APIKey: "key"},
		Remotes: []config.RemoteConfig{{
			ID:        "remote1",
			APIURL:    remoteSrv.URL,
			StrmURL:   "http://stream.example.com",
			APIKey:    "key",
			RootStart: "/media",
			LibraryMappings: []config.LibraryMapping{{
				RemoteName: "Movies",
				LocalPath:  tmpDir,
			}},
		}},
	}

	s := New(cfg, database)
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Stale strm file should be deleted.
	if _, err := os.Stat(strmPath); !os.IsNotExist(err) {
		t.Errorf("stale strm file %q should have been deleted", strmPath)
	}

	// DB record for the stale item should be removed.
	items, _ := dbpkg.GetAllSyncedItems(database)
	for _, item := range items {
		if item.StrmPath == strmPath {
			t.Errorf("stale DB record for %q should have been deleted", strmPath)
		}
	}
}

func TestRun_TieBreakerSelectsWinner(t *testing.T) {
	tmpDir := t.TempDir()
	targetSrv := newJellyfinServer(t, nil, nil)

	// Two remotes both have the same movie; tie-breaker prefers h265.
	remote1Srv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "r1lib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"r1lib1": {{
				Id:          "r1item1",
				Name:        "Shared Movie",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "55555"},
				MediaSources: []jfMediaSource{{
					Path:         "/media1/Movies/Shared Movie/movie.mkv",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 1080, Codec: "h264"}},
				}},
			}},
		},
	)
	remote2Srv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "r2lib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"r2lib1": {{
				Id:          "r2item1",
				Name:        "Shared Movie",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "55555"},
				MediaSources: []jfMediaSource{{
					Path:         "/media2/Movies/Shared Movie/movie.mkv",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 1080, Codec: "hevc"}},
				}},
			}},
		},
	)

	database := setupTestDB(t)

	cfg := &config.Config{
		Target: config.TargetConfig{URL: targetSrv.URL, APIKey: "key"},
		TieBreakerFields: []config.TieBreaker{
			{Field: "encoding", Value: "h265"},
		},
		Remotes: []config.RemoteConfig{
			{
				ID:              "remote1",
				APIURL:          remote1Srv.URL,
				StrmURL:         "http://stream1.example.com",
				APIKey:          "key",
				RootStart:       "/media1",
				LibraryMappings: []config.LibraryMapping{{RemoteName: "Movies", LocalPath: tmpDir}},
			},
			{
				ID:              "remote2",
				APIURL:          remote2Srv.URL,
				StrmURL:         "http://stream2.example.com",
				APIKey:          "key",
				RootStart:       "/media2",
				LibraryMappings: []config.LibraryMapping{{RemoteName: "Movies", LocalPath: tmpDir}},
			},
		},
	}

	s := New(cfg, database)
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Winner should be remote2 (h265/hevc). Strm content should point to stream2.
	expectedStrm := filepath.Join(tmpDir, "Shared Movie", "movie.strm")
	content, err := os.ReadFile(expectedStrm)
	if err != nil {
		t.Fatalf("strm file not found: %v", err)
	}
	wantPrefix := "http://stream2.example.com"
	if len(content) == 0 || string(content[:len(wantPrefix)]) != wantPrefix {
		t.Errorf("strm content = %q, want prefix %q (should come from remote2/h265)", string(content), wantPrefix)
	}

	items, _ := dbpkg.GetAllSyncedItems(database)
	if len(items) != 1 {
		t.Fatalf("expected 1 synced item, got %d", len(items))
	}
	if items[0].RemoteID != "remote2" {
		t.Errorf("winner RemoteID = %q, want %q", items[0].RemoteID, "remote2")
	}
}

func TestRun_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	targetSrv := newJellyfinServer(t, nil, nil)
	remoteSrv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "lib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"lib1": {{
				Id:          "item1",
				Name:        "Stable Movie",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "77777"},
				MediaSources: []jfMediaSource{{
					Path:         "/media/Movies/Stable Movie/movie.mkv",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 720, Codec: "h264"}},
				}},
			}},
		},
	)

	database := setupTestDB(t)
	cfg := &config.Config{
		Target: config.TargetConfig{URL: targetSrv.URL, APIKey: "key"},
		Remotes: []config.RemoteConfig{{
			ID:              "remote1",
			APIURL:          remoteSrv.URL,
			StrmURL:         "http://stream.example.com",
			APIKey:          "key",
			RootStart:       "/media",
			LibraryMappings: []config.LibraryMapping{{RemoteName: "Movies", LocalPath: tmpDir}},
		}},
	}

	s := New(cfg, database)

	// Run twice; state should be identical after both runs.
	for i := 0; i < 2; i++ {
		if err := s.Run(context.Background()); err != nil {
			t.Fatalf("Run() iteration %d error = %v", i+1, err)
		}
	}

	items, _ := dbpkg.GetAllSyncedItems(database)
	if len(items) != 1 {
		t.Errorf("expected exactly 1 synced item after 2 runs, got %d", len(items))
	}

	strmPath := filepath.Join(tmpDir, "Stable Movie", "movie.strm")
	if _, err := os.Stat(strmPath); err != nil {
		t.Errorf("strm file missing after 2 runs: %v", err)
	}
}
