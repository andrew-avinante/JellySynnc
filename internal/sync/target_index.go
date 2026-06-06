package sync

import (
	"fmt"

	"github.com/andrew-avinante/JellySynnc/internal/jellyfin"
)

const episodeType = "Episode"

// TargetIndex is a unified lookup structure for what already exists on the target server.
// It supports coverage checks by (providerKey, resolution), provider-key-only checks,
// resolution grouping, episode S/E fallback matching, and name-based fallback matching —
// all built from a single pass over target items.
type TargetIndex struct {
	coverage          coverageSet
	providerKeys      stringSet
	typedCoverage     coverageSet // coverage keyed by type-scoped provider key
	typedProviderKeys stringSet   // type-scoped "itemType\x00type:id" provider keys
	resolutions       resolutionIndex
	episodes          stringSet
	names             stringSet // exact item names for non-episode name fallback
	episodeNames      stringSet // "name\x00seriesPK" keys for episode name fallback
}

func NewTargetIndex() *TargetIndex {
	return &TargetIndex{
		coverage:          newCoverageSet(),
		providerKeys:      newStringSet(),
		typedCoverage:     newCoverageSet(),
		typedProviderKeys: newStringSet(),
		resolutions:       newResolutionIndex(),
		episodes:          newStringSet(),
		names:             newStringSet(),
		episodeNames:      newStringSet(),
	}
}

func (t *TargetIndex) Add(item jellyfin.MediaItem) {
	for k, v := range item.ProviderIDs {
		if isCollectionKey(k) {
			continue
		}
		pk := providerKey(k, v)
		t.coverage.add(candidateKey{ProviderKey: pk, Resolution: item.Resolution})
		t.providerKeys.add(pk)
		tpk := typedProviderKey(item.Type, pk)
		t.typedCoverage.add(candidateKey{ProviderKey: tpk, Resolution: item.Resolution})
		t.typedProviderKeys.add(tpk)
		if t.resolutions[pk] == nil {
			t.resolutions[pk] = newStringSet()
		}
		t.resolutions[pk].add(item.Resolution)
	}
	if item.Type == episodeType {
		for k, v := range item.SeriesProviderIDs {
			spk := providerKey(k, v)
			if item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
				t.episodes.add(episodeKey(spk, item.SeasonNumber, item.EpisodeNumber))
			}
			t.episodeNames.add(episodeNameKey(item.Name, spk))
		}
	} else {
		t.names.add(item.Name)
	}
}

func (t *TargetIndex) Has(key candidateKey) bool {
	return t.coverage.has(key)
}

func (t *TargetIndex) HasProviderKey(pk string) bool {
	return t.providerKeys.has(pk)
}

// hasTyped reports an exact (type-scoped providerKey, resolution) match for an item
// of the given type. Scoping by type prevents a provider ID that is unique only
// within an entity type (notably TVDB) from matching a different kind of item.
func (t *TargetIndex) hasTyped(itemType string, key candidateKey) bool {
	return t.typedCoverage.has(candidateKey{
		ProviderKey: typedProviderKey(itemType, key.ProviderKey),
		Resolution:  key.Resolution,
	})
}

// hasTypedProviderKey reports a resolution-agnostic, type-scoped provider-key match.
func (t *TargetIndex) hasTypedProviderKey(itemType, pk string) bool {
	return t.typedProviderKeys.has(typedProviderKey(itemType, pk))
}

// Resolutions returns the providerKey → resolution set map for the target.
// Callers should treat this as read-only.
func (t *TargetIndex) Resolutions() resolutionIndex {
	return t.resolutions
}

func (t *TargetIndex) HasEpisode(seriesPK string, season, episode int) bool {
	return t.episodes.has(episodeKey(seriesPK, season, episode))
}

func (t *TargetIndex) HasName(name string) bool {
	return t.names.has(name)
}

func (t *TargetIndex) HasEpisodeName(name, seriesPK string) bool {
	return t.episodeNames.has(episodeNameKey(name, seriesPK))
}

// covers reports whether item (the winning candidate for key) is already present
// on the target, so the write pass can skip it. It checks, in order:
//   - an exact (providerKey, resolution) match;
//   - a provider-key-only match when the item is not multi-resolution;
//   - the same two checks across the item's OTHER provider IDs (cross-PID), since
//     an item is registered once per provider ID and any of them may match;
//   - an episode-number fallback keyed by (series PID, season, episode) for
//     Episode items whose episode-level PIDs disagree across servers;
//   - a name fallback: exact item name for non-episodes; exact name + at least one
//     matching series provider ID for episodes (guards against same-name episodes
//     across different series).
func (t *TargetIndex) covers(key candidateKey, item jellyfin.MediaItem, multiRes multiResolutionSet) bool {
	covered, _ := t.coverWhy(key, item, multiRes)
	return covered
}

// coverWhy is covers with the matching reason exposed for the debug report. It is
// the single source of truth; covers discards the reason. The reason names which
// check passed (or "no match"), so a debug dump shows exactly why an item was or
// wasn't treated as already present.
func (t *TargetIndex) coverWhy(key candidateKey, item jellyfin.MediaItem, multiRes multiResolutionSet) (bool, string) {
	// Strongest match: same provider ID and same resolution already on target.
	// Provider keys are scoped by item type so an id unique only within an entity
	// type (notably TVDB) can't match a different kind of item.
	if t.hasTyped(item.Type, key) {
		return true, "exact (providerKey, resolution) match"
	}
	// Provider ID exists on target with no resolution variants — treat any resolution as covered.
	if t.hasTypedProviderKey(item.Type, key.ProviderKey) && !multiRes[key.ProviderKey] {
		return true, "providerKey match (single resolution)"
	}
	// Cross-PID: the source item has additional provider IDs (e.g. TMDB and TVDB both present).
	// If any alternate PID already covers this item on the target, it counts.
	for k, v := range item.ProviderIDs {
		if isCollectionKey(k) {
			continue
		}
		pk := providerKey(k, v)

		// Alternate PID matched with same resolution.
		if t.hasTyped(item.Type, candidateKey{ProviderKey: pk, Resolution: key.Resolution}) {
			return true, "cross-PID exact match on " + pk
		}
		// Alternate PID present on target with no resolution variants.
		if t.hasTypedProviderKey(item.Type, pk) && !multiRes[pk] {
			return true, "cross-PID providerKey match on " + pk
		}
	}
	// Episode S/E fallback: episode-level PIDs may differ across servers (re-numbered
	// specials, split episodes), so match by (series PID, season, episode number) instead.
	if item.Type == episodeType && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
		for k, v := range item.SeriesProviderIDs {
			if t.HasEpisode(providerKey(k, v), item.SeasonNumber, item.EpisodeNumber) {
				return true, "episode (series, season, episode) fallback match"
			}
		}
	}
	// Name fallback: last resort when no numeric identifier matches.
	// For episodes, require the name to match within the same series (guards against
	// identically-named episodes in different series). For everything else, exact title match.
	if item.Type == episodeType {
		for k, v := range item.SeriesProviderIDs {
			pk := providerKey(k, v)
			if t.HasEpisodeName(item.Name, pk) {
				return true, "episode name + series PID match on " + pk
			}
		}
	} else if t.HasName(item.Name) {
		return true, "name match"
	}
	return false, "no match"
}

// typedProviderKey scopes a provider key by item type. TVDB (and some others) number
// movies, series and episodes in separate id spaces, so an unrelated episode can share
// a movie's numeric id; prefixing the item type keeps "Movie\x00tvdb:13604" distinct
// from "Episode\x00tvdb:13604". The null byte can't appear in a type or provider key.
func typedProviderKey(itemType, pk string) string {
	return itemType + "\x00" + pk
}

// episodeKey builds the composite "seriesPK:season:episode" key used for the
// episode-number fallback match.
func episodeKey(seriesPK string, season, episode int) string {
	return fmt.Sprintf("%s:%d:%d", seriesPK, season, episode)
}

// episodeNameKey builds the composite key used for the episode name + series PID fallback.
// Null byte separator ensures no collision between name and seriesPK components.
func episodeNameKey(name, seriesPK string) string {
	return name + "\x00" + seriesPK
}
