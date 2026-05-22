package sync

import (
	"log/slog"

	"github.com/andrew-avinante/JellySynnc/internal/config"
	"github.com/andrew-avinante/JellySynnc/internal/jellyfin"
)

type coverageSet map[candidateKey]struct{}

func newCoverageSet() coverageSet { return make(coverageSet) }

type providerKeySet map[string]struct{}

func newProviderKeySet() providerKeySet { return make(providerKeySet) }

type resolutionSet map[string]struct{}

func newResolutionSet() resolutionSet { return make(resolutionSet) }

type resolutionIndex map[string]resolutionSet

func newResolutionIndex() resolutionIndex { return make(resolutionIndex) }

type episodeSet map[string]struct{}

func newEpisodeSet() episodeSet { return make(episodeSet) }

type candidateMap map[candidateKey][]candidate

func newCandidateMap() candidateMap { return make(candidateMap) }

// add resolves provider IDs for item and inserts one candidate entry per
// (providerKey, resolution) pair. Items with no provider IDs are skipped or
// given a synthetic ID depending on the mapping's sync_unknown_provider_ids flag.
func (candidates candidateMap) add(item jellyfin.MediaItem, remote config.RemoteConfig, mapping config.LibraryMapping) {
	if len(item.ProviderIDs) == 0 {
		if !mapping.GetSyncUnknownProviderIDs() {
			slog.Warn("skipping item with no provider IDs",
				"name", item.Name,
				"library", mapping.GetRemoteName(),
				"remote_id", remote.GetID(),
				"hint", "set sync_unknown_provider_ids: true in library mapping to sync anyway",
			)
			return
		}
		// Inject synthetic provider ID so the item is tracked in the DB
		// and resolves consistently across runs. Uses "jellyfin_<remoteID>"
		// as key (no colons; remote IDs are UUIDs) so pk reconstruction
		// via k+":"+v produces a stable, unique value.
		// If Jellyfin later finds real provider IDs, the JellyfinItemID
		// fallback in the removal pass prevents false deletion.
		item.ProviderIDs = map[string]string{
			"jellyfin_" + remote.GetID(): item.JellyfinID,
		}
	}
	for k, v := range item.ProviderIDs {
		key := candidateKey{ProviderKey: providerKey(k, v), Resolution: item.Resolution}
		candidates[key] = append(candidates[key], candidate{Remote: remote, Item: item, LibraryMapping: mapping})
	}
}
