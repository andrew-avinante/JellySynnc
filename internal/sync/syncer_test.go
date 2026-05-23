package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// TestCollectionKeyExcludedFromIndexes verifies collection IDs never enter the
// target coverage index or the candidate map. They identify a franchise, not a
// single item, so keying on them would let one synced movie cover its siblings.
func TestCollectionKeyExcludedFromIndexes(t *testing.T) {
	target := NewTargetIndex()
	target.Add(jellyfin.MediaItem{
		Name: "Iron Man 3", Type: "Movie", Resolution: "1080p",
		ProviderIDs: map[string]string{
			"imdb": "tt1300854", "tmdb": "68721", "tmdbcollection": "131292",
		},
	})
	if target.HasProviderKey("tmdbcollection:131292") {
		t.Error("collection key must not be indexed in target coverage")
	}
	if !target.HasProviderKey("imdb:tt1300854") {
		t.Error("real provider key should be indexed")
	}

	candidates := newCandidateMap()
	candidates.add(jellyfin.MediaItem{
		Name: "Iron Man", Type: "Movie", Resolution: "1080p",
		ProviderIDs: map[string]string{
			"imdb": "tt0371746", "tmdb": "1726", "tmdbcollection": "131292",
		},
	}, config.RemoteConfig{ID: "remote1"}, config.LibraryMapping{})
	for key := range candidates {
		if isCollectionKey(strings.SplitN(key.ProviderKey, ":", 2)[0]) {
			t.Errorf("collection key %q must not be a candidate key", key.ProviderKey)
		}
	}
}

// TestCoverWhy_CollectionSiblingNotFalselyCovered is the core regression for the
// over-coverage bug: a genuinely-absent movie that shares only a collection ID
// with a different movie already on the target must NOT be treated as covered.
// Iron Man (2008) shares collection 131292 with Iron Man 3 (on target) but has
// distinct real IDs and a distinct name, so it must be flagged for sync.
func TestCoverWhy_CollectionSiblingNotFalselyCovered(t *testing.T) {
	target := NewTargetIndex()
	target.Add(jellyfin.MediaItem{
		Name: "Iron Man 3", Type: "Movie", Resolution: "1080p",
		ProviderIDs: map[string]string{
			"imdb": "tt1300854", "tmdb": "68721", "tmdbcollection": "131292",
		},
	})

	ironMan := jellyfin.MediaItem{
		Name: "Iron Man", Type: "Movie", Resolution: "1080p",
		ProviderIDs: map[string]string{
			"imdb": "tt0371746", "tmdb": "1726", "tmdbcollection": "131292",
		},
	}
	candidates := newCandidateMap()
	candidates.add(ironMan, config.RemoteConfig{ID: "remote1"}, config.LibraryMapping{})
	multiRes := newMultiResolutionSet(candidates, target)

	// Every non-collection candidate key for Iron Man must be uncovered.
	for key := range candidates {
		covered, reason := target.coverWhy(key, ironMan, multiRes)
		if covered {
			t.Errorf("key %q wrongly covered (reason %q); Iron Man is absent from target", key.ProviderKey, reason)
		}
	}
}

// TestCoverWhy_SharedRealIDsCoverViaCrossPID confirms exclusion of collection keys
// does not break legitimate coverage: an item sharing a real ID with a target item
// is still covered via cross-PID, independent of any collection ID.
func TestCoverWhy_SharedRealIDsCoverViaCrossPID(t *testing.T) {
	target := NewTargetIndex()
	target.Add(jellyfin.MediaItem{
		Name: "Avatar: The Way of Water", Type: "Movie", Resolution: "unknown",
		ProviderIDs: map[string]string{
			"imdb": "tt1630029", "tmdb": "76600", "tmdbcollection": "87096",
			"tvdb": "5483", "tvdbslug": "avatar-the-way-of-water",
		},
	})

	// The "Stunts" extra carries the real movie's imdb/tmdb IDs (plus its own tvdb).
	stunts := jellyfin.MediaItem{
		Name: "Avatar The way of water: Stunts", Type: "Movie", Resolution: "4K",
		ProviderIDs: map[string]string{
			"imdb": "tt1630029", "tmdb": "76600", "tmdbcollection": "87096",
			"tvdb": "353309", "tvdbslug": "avatar-the-way-of-water-stunts",
		},
	}
	candidates := newCandidateMap()
	candidates.add(stunts, config.RemoteConfig{ID: "remote1"}, config.LibraryMapping{})
	multiRes := newMultiResolutionSet(candidates, target)

	// tvdb:353309 is absent from target but imdb:tt1630029 is present → cross-PID covers it.
	covered, reason := target.coverWhy(
		candidateKey{ProviderKey: "tvdb:353309", Resolution: "4K"}, stunts, multiRes)
	if !covered {
		t.Errorf("expected cross-PID coverage via real ID, got not covered (reason %q)", reason)
	}
}

// TestCoverWhy_CrossPIDMultiResAlternateNotCovered verifies the flip side: when an
// item's alternate provider ID is itself multi-resolution on the target, a
// different-resolution candidate must NOT be treated as covered by a provider-key-only
// cross-PID match. This guards the multiRes[pk] gate from over-matching.
func TestCoverWhy_CrossPIDMultiResAlternateNotCovered(t *testing.T) {
	target := NewTargetIndex()
	target.Add(jellyfin.MediaItem{
		Name: "Movie HD", Type: "Movie", Resolution: "1080p",
		ProviderIDs: map[string]string{"imdb": "tt9999999"},
	})
	target.Add(jellyfin.MediaItem{
		Name: "Movie UHD", Type: "Movie", Resolution: "4K",
		ProviderIDs: map[string]string{"imdb": "tt9999999"},
	})

	candidate := jellyfin.MediaItem{
		Name: "Movie SD", Type: "Movie", Resolution: "720p",
		ProviderIDs: map[string]string{"imdb": "tt9999999", "tvdb": "424242"},
	}
	candidates := newCandidateMap()
	candidates.add(candidate, config.RemoteConfig{ID: "remote1"}, config.LibraryMapping{})

	multiRes := newMultiResolutionSet(candidates, target)
	if !multiRes["imdb:tt9999999"] {
		t.Fatal("expected imdb:tt9999999 to be multi-resolution")
	}

	// tvdb:424242 @720p is absent from target; its only target-present alt PID
	// (imdb) is multi-resolution and has no 720p variant, so it must not match.
	key := candidateKey{ProviderKey: "tvdb:424242", Resolution: "720p"}
	covered, reason := target.coverWhy(key, candidate, multiRes)
	if covered {
		t.Errorf("covered = true (reason %q), want false: multi-res alternate PID must not over-match", reason)
	}
}

// TestCoverWhy_AllBranches exercises every match branch of coverWhy with a focused
// target setup per case, including the two "not covered" scenarios surfaced during
// debugging: an unidentified target item (empty provider IDs + mangled name) and a
// genuinely-absent item with no shared identity.
func TestCoverWhy_AllBranches(t *testing.T) {
	tests := []struct {
		name           string
		targetItems    []jellyfin.MediaItem
		item           jellyfin.MediaItem
		key            candidateKey
		wantCovered    bool
		wantReason     string // exact match when set
		wantReasonPref string // prefix match when set
	}{
		{
			name: "exact providerKey+resolution match",
			targetItems: []jellyfin.MediaItem{
				{Name: "Foo", Type: "Movie", Resolution: "1080p", ProviderIDs: map[string]string{"imdb": "tt1"}},
			},
			item:        jellyfin.MediaItem{Name: "Foo", Type: "Movie", Resolution: "1080p", ProviderIDs: map[string]string{"imdb": "tt1"}},
			key:         candidateKey{ProviderKey: "imdb:tt1", Resolution: "1080p"},
			wantCovered: true,
			wantReason:  "exact (providerKey, resolution) match",
		},
		{
			name: "providerKey match single resolution covers any resolution",
			targetItems: []jellyfin.MediaItem{
				{Name: "Foo", Type: "Movie", Resolution: "1080p", ProviderIDs: map[string]string{"imdb": "tt1"}},
			},
			item:        jellyfin.MediaItem{Name: "Foo", Type: "Movie", Resolution: "4K", ProviderIDs: map[string]string{"imdb": "tt1"}},
			key:         candidateKey{ProviderKey: "imdb:tt1", Resolution: "4K"},
			wantCovered: true,
			wantReason:  "providerKey match (single resolution)",
		},
		{
			name: "cross-PID exact match on alternate ID",
			targetItems: []jellyfin.MediaItem{
				{Name: "Foo", Type: "Movie", Resolution: "4K", ProviderIDs: map[string]string{"imdb": "tt1"}},
			},
			item:        jellyfin.MediaItem{Name: "Foo", Type: "Movie", Resolution: "4K", ProviderIDs: map[string]string{"imdb": "tt1", "tvdb": "99"}},
			key:         candidateKey{ProviderKey: "tvdb:99", Resolution: "4K"},
			wantCovered: true,
			wantReason:  "cross-PID exact match on imdb:tt1",
		},
		{
			name: "episode (series, season, episode) fallback when episode PIDs differ",
			targetItems: []jellyfin.MediaItem{
				{Name: "Ep A", Type: "Episode", Resolution: "1080p",
					ProviderIDs:       map[string]string{"imdb": "epOLD"},
					SeriesProviderIDs: map[string]string{"tvdb": "series1"},
					SeasonNumber:      1, EpisodeNumber: 2},
			},
			item: jellyfin.MediaItem{Name: "Ep A renamed", Type: "Episode", Resolution: "1080p",
				ProviderIDs:       map[string]string{"imdb": "epNEW"},
				SeriesProviderIDs: map[string]string{"tvdb": "series1"},
				SeasonNumber:      1, EpisodeNumber: 2},
			key:         candidateKey{ProviderKey: "imdb:epNEW", Resolution: "1080p"},
			wantCovered: true,
			wantReason:  "episode (series, season, episode) fallback match",
		},
		{
			name: "episode name + series PID fallback when numbers absent",
			targetItems: []jellyfin.MediaItem{
				{Name: "Special", Type: "Episode", Resolution: "1080p",
					ProviderIDs:       map[string]string{"imdb": "epOLD"},
					SeriesProviderIDs: map[string]string{"tvdb": "series1"}},
			},
			item: jellyfin.MediaItem{Name: "Special", Type: "Episode", Resolution: "1080p",
				ProviderIDs:       map[string]string{"imdb": "epNEW"},
				SeriesProviderIDs: map[string]string{"tvdb": "series1"}},
			key:            candidateKey{ProviderKey: "imdb:epNEW", Resolution: "1080p"},
			wantCovered:    true,
			wantReasonPref: "episode name + series PID match on ",
		},
		{
			name: "name match covers item unidentified on target (empty IDs, same name)",
			targetItems: []jellyfin.MediaItem{
				{Name: "Foo", Type: "Movie", Resolution: "unknown", ProviderIDs: map[string]string{}},
			},
			item:        jellyfin.MediaItem{Name: "Foo", Type: "Movie", Resolution: "1080p", ProviderIDs: map[string]string{"imdb": "tt1"}},
			key:         candidateKey{ProviderKey: "imdb:tt1", Resolution: "1080p"},
			wantCovered: true,
			wantReason:  "name match",
		},
		{
			name: "unidentified target item with mangled name is NOT covered (perpetual re-sync scenario)",
			targetItems: []jellyfin.MediaItem{
				{Name: "Foo Waifu2x", Type: "Movie", Resolution: "unknown", ProviderIDs: map[string]string{}},
			},
			item:        jellyfin.MediaItem{Name: "Foo", Type: "Movie", Resolution: "1080p", ProviderIDs: map[string]string{"imdb": "tt1"}},
			key:         candidateKey{ProviderKey: "imdb:tt1", Resolution: "1080p"},
			wantCovered: false,
			wantReason:  "no match",
		},
		{
			name: "genuinely absent item is NOT covered",
			targetItems: []jellyfin.MediaItem{
				{Name: "Other", Type: "Movie", Resolution: "1080p", ProviderIDs: map[string]string{"imdb": "ttOTHER"}},
			},
			item:        jellyfin.MediaItem{Name: "Foo", Type: "Movie", Resolution: "1080p", ProviderIDs: map[string]string{"imdb": "tt1"}},
			key:         candidateKey{ProviderKey: "imdb:tt1", Resolution: "1080p"},
			wantCovered: false,
			wantReason:  "no match",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := NewTargetIndex()
			for _, ti := range tc.targetItems {
				target.Add(ti)
			}
			multiRes := newMultiResolutionSet(newCandidateMap(), target)
			covered, reason := target.coverWhy(tc.key, tc.item, multiRes)
			if covered != tc.wantCovered {
				t.Errorf("covered = %v, want %v (reason %q)", covered, tc.wantCovered, reason)
			}
			if tc.wantReason != "" && reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
			if tc.wantReasonPref != "" && !strings.HasPrefix(reason, tc.wantReasonPref) {
				t.Errorf("reason = %q, want prefix %q", reason, tc.wantReasonPref)
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

// TestRun_WritesCollectionSibling is the end-to-end regression for the collection-ID
// over-coverage bug. The target has Iron Man 3; the remote has a different movie
// (Iron Man) that shares only the franchise collection ID. The sibling must still be
// written, because a shared collection ID is not proof the item itself is present.
func TestRun_WritesCollectionSibling(t *testing.T) {
	tmpDir := t.TempDir()

	targetSrv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "tlib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"tlib1": {{
				Id:          "target-im3",
				Name:        "Iron Man 3",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "68721", "TmdbCollection": "131292"},
				MediaSources: []jfMediaSource{{
					Path:         "/target/Movies/Iron Man 3/movie.strm",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 1080, Codec: "hevc"}},
				}},
			}},
		},
	)

	remoteSrv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "rlib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"rlib1": {{
				Id:          "remote-im1",
				Name:        "Iron Man",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "1726", "TmdbCollection": "131292"},
				MediaSources: []jfMediaSource{{
					Path:         "/media/Movies/Iron Man (2008)/movie.mkv",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 1080, Codec: "hevc"}},
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
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	expectedStrm := filepath.Join(tmpDir, "Iron Man (2008)", "movie.strm")
	if _, err := os.Stat(expectedStrm); err != nil {
		t.Errorf("collection sibling not written at %q: %v", expectedStrm, err)
	}
}

// TestRun_WritesUnidentifiedTargetItem covers the "perpetual re-sync" scenario found
// during debugging: the target already has the file as a .strm but Jellyfin failed to
// match it (empty provider IDs) and named it after the mangled filename. With no shared
// provider ID and a non-matching name, the remote item is correctly treated as missing
// and written.
func TestRun_WritesUnidentifiedTargetItem(t *testing.T) {
	tmpDir := t.TempDir()

	targetSrv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "tlib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"tlib1": {{
				Id:          "target-unmatched",
				Name:        "Iron Man Waifu2x",
				Type:        "Movie",
				ProviderIds: map[string]string{}, // Jellyfin could not identify it
				MediaSources: []jfMediaSource{{
					Path:         "/target/Movies/Iron Man (2008)/Iron Man Waifu2x.strm",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 0, Codec: ""}},
				}},
			}},
		},
	)

	remoteSrv := newJellyfinServer(t,
		[]jfVirtualFolder{{ItemId: "rlib1", Name: "Movies", CollectionType: "movies"}},
		map[string][]jfItem{
			"rlib1": {{
				Id:          "remote-im1",
				Name:        "Iron Man",
				Type:        "Movie",
				ProviderIds: map[string]string{"Tmdb": "1726", "Imdb": "tt0371746"},
				MediaSources: []jfMediaSource{{
					Path:         "/media/Movies/Iron Man (2008)/movie.mkv",
					MediaStreams: []jfMediaStream{{Type: "Video", Height: 1080, Codec: "hevc"}},
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
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	expectedStrm := filepath.Join(tmpDir, "Iron Man (2008)", "movie.strm")
	if _, err := os.Stat(expectedStrm); err != nil {
		t.Errorf("unidentified item not written at %q: %v", expectedStrm, err)
	}
}
