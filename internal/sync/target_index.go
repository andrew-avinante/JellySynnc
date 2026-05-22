package sync

import (
	"fmt"

	"github.com/andrew-avinante/JellySynnc/internal/jellyfin"
)

const episodeType = "Episode"

// TargetIndex is a unified lookup structure for what already exists on the target server.
// It supports coverage checks by (providerKey, resolution), provider-key-only checks,
// resolution grouping, and episode S/E fallback matching — all built from a single pass
// over target items.
type TargetIndex struct {
	coverage     coverageSet
	providerKeys stringSet
	resolutions  resolutionIndex
	episodes     stringSet
}

func NewTargetIndex() *TargetIndex {
	return &TargetIndex{
		coverage:     newCoverageSet(),
		providerKeys: newStringSet(),
		resolutions:  newResolutionIndex(),
		episodes:     newStringSet(),
	}
}

func (t *TargetIndex) Add(item jellyfin.MediaItem) {
	for k, v := range item.ProviderIDs {
		pk := providerKey(k, v)
		t.coverage.add(candidateKey{ProviderKey: pk, Resolution: item.Resolution})
		t.providerKeys.add(pk)
		if t.resolutions[pk] == nil {
			t.resolutions[pk] = newStringSet()
		}
		t.resolutions[pk].add(item.Resolution)
	}
	if item.Type == episodeType && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
		for k, v := range item.SeriesProviderIDs {
			t.episodes.add(episodeKey(providerKey(k, v), item.SeasonNumber, item.EpisodeNumber))
		}
	}
}

func (t *TargetIndex) Has(key candidateKey) bool {
	return t.coverage.has(key)
}

func (t *TargetIndex) HasProviderKey(pk string) bool {
	return t.providerKeys.has(pk)
}

// Resolutions returns the providerKey → resolution set map for the target.
// Callers should treat this as read-only.
func (t *TargetIndex) Resolutions() resolutionIndex {
	return t.resolutions
}

func (t *TargetIndex) HasEpisode(seriesPK string, season, episode int) bool {
	return t.episodes.has(episodeKey(seriesPK, season, episode))
}

// covers reports whether item (the winning candidate for key) is already present
// on the target, so the write pass can skip it. It checks, in order:
//   - an exact (providerKey, resolution) match;
//   - a provider-key-only match when the item is not multi-resolution;
//   - the same two checks across the item's OTHER provider IDs (cross-PID), since
//     an item is registered once per provider ID and any of them may match;
//   - an episode-number fallback keyed by (series PID, season, episode) for
//     Episode items whose episode-level PIDs disagree across servers (e.g. TVDB
//     renumbering).
func (t *TargetIndex) covers(key candidateKey, item jellyfin.MediaItem, multiRes multiResolutionSet) bool {
	if t.Has(key) {
		return true
	}
	if t.HasProviderKey(key.ProviderKey) && !multiRes[key.ProviderKey] {
		return true
	}
	for k, v := range item.ProviderIDs {
		pk := providerKey(k, v)
		if pk == key.ProviderKey {
			continue
		}
		if t.Has(candidateKey{ProviderKey: pk, Resolution: key.Resolution}) {
			return true
		}
		if t.HasProviderKey(pk) && !multiRes[key.ProviderKey] {
			return true
		}
	}
	if item.Type == episodeType && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
		for k, v := range item.SeriesProviderIDs {
			if t.HasEpisode(providerKey(k, v), item.SeasonNumber, item.EpisodeNumber) {
				return true
			}
		}
	}
	return false
}

// episodeKey builds the composite "seriesPK:season:episode" key used for the
// episode-number fallback match.
func episodeKey(seriesPK string, season, episode int) string {
	return fmt.Sprintf("%s:%d:%d", seriesPK, season, episode)
}
