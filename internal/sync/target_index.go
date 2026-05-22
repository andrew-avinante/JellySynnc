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
	providerKeys providerKeySet
	resolutions  resolutionIndex
	episodes     episodeSet
}

func NewTargetIndex() *TargetIndex {
	return &TargetIndex{
		coverage:     newCoverageSet(),
		providerKeys: newProviderKeySet(),
		resolutions:  newResolutionIndex(),
		episodes:     newEpisodeSet(),
	}
}

func (t *TargetIndex) Add(item jellyfin.MediaItem) {
	for k, v := range item.ProviderIDs {
		pk := providerKey(k, v)
		t.coverage[candidateKey{ProviderKey: pk, Resolution: item.Resolution}] = emptyStruct()
		t.providerKeys[pk] = emptyStruct()
		if t.resolutions[pk] == nil {
			t.resolutions[pk] = newResolutionSet()
		}
		t.resolutions[pk][item.Resolution] = emptyStruct()
	}
	if item.Type == episodeType && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
		for k, v := range item.SeriesProviderIDs {
			t.episodes[fmt.Sprintf("%s:%d:%d", providerKey(k, v), item.SeasonNumber, item.EpisodeNumber)] = emptyStruct()
		}
	}
}

func (t *TargetIndex) Has(key candidateKey) bool {
	_, ok := t.coverage[key]
	return ok
}

func (t *TargetIndex) HasProviderKey(pk string) bool {
	_, ok := t.providerKeys[pk]
	return ok
}

// Resolutions returns the providerKey → resolution set map for the target.
// Callers should treat this as read-only.
func (t *TargetIndex) Resolutions() resolutionIndex {
	return t.resolutions
}

func (t *TargetIndex) HasEpisode(seriesPK string, season, episode int) bool {
	_, ok := t.episodes[fmt.Sprintf("%s:%d:%d", seriesPK, season, episode)]
	return ok
}

func emptyStruct() struct{} {
	return struct{}{}
}
