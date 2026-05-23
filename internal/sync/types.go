package sync

import (
	"encoding/json"
	"log/slog"
	"path/filepath"

	"github.com/andrew-avinante/JellySynnc/internal/config"
	"github.com/andrew-avinante/JellySynnc/internal/db"
	"github.com/andrew-avinante/JellySynnc/internal/jellyfin"
)

// candidateKey identifies an item by its provider key ("type:id") and resolution.
// It is the shared key for the candidate, coverage, and synced indexes.
type candidateKey struct {
	ProviderKey string
	Resolution  string
}

// candidate is one remote's copy of a media item, paired with the remote and the
// library mapping it was found through.
type candidate struct {
	Remote         config.RemoteConfig
	Item           jellyfin.MediaItem
	LibraryMapping config.LibraryMapping
}

// stringSet is a presence set of strings (provider keys, resolutions, episode
// keys, Jellyfin IDs) used throughout the sync indexes.
type stringSet map[string]struct{}

func newStringSet() stringSet { return make(stringSet) }

func (set stringSet) add(value string) { set[value] = struct{}{} }

func (set stringSet) has(value string) bool {
	_, ok := set[value]
	return ok
}

// coverageSet is a presence set keyed by the composite candidateKey.
type coverageSet map[candidateKey]struct{}

func newCoverageSet() coverageSet { return make(coverageSet) }

func (set coverageSet) add(key candidateKey) { set[key] = struct{}{} }

func (set coverageSet) has(key candidateKey) bool {
	_, ok := set[key]
	return ok
}

// resolutionIndex maps a provider key to the set of resolutions seen for it.
type resolutionIndex map[string]stringSet

func newResolutionIndex() resolutionIndex { return make(resolutionIndex) }

// syncedIndex is a decoded, indexed view of the synced_items table built once
// per run. It supports the write pass (coverage lookups by key, directory reuse
// for multi-resolution variants) and the removal pass (decoded provider IDs per
// row). Rows with corrupt provider_ids JSON are logged and omitted.
type syncedIndex struct {
	byKey            map[candidateKey]db.SyncedItem // (providerKey, resolution) -> row
	dirByProviderKey map[string]string              // providerKey -> dir of its existing strm
	pidsByItemID     map[string]map[string]string   // synced_item.ID -> decoded provider IDs
}

func newSyncedIndex(items []db.SyncedItem) syncedIndex {
	index := syncedIndex{
		byKey:            make(map[candidateKey]db.SyncedItem),
		dirByProviderKey: make(map[string]string),
		pidsByItemID:     make(map[string]map[string]string),
	}
	for _, item := range items {
		var pids map[string]string
		if err := json.Unmarshal([]byte(item.ProviderIDs), &pids); err != nil {
			slog.Warn("corrupt provider_ids in db, skipping", "id", item.ID, "err", err)
			continue
		}
		index.pidsByItemID[item.ID] = pids
		dir := filepath.Dir(item.StrmPath)
		for k, v := range pids {
			pk := providerKey(k, v)
			index.byKey[candidateKey{ProviderKey: pk, Resolution: item.Resolution}] = item
			index.dirByProviderKey[pk] = dir
		}
	}
	return index
}

// remotePresence records everything currently present on one remote: the set of
// provider keys and the set of Jellyfin item IDs across that remote's libraries.
type remotePresence struct {
	providerKeys stringSet
	jellyfinIDs  stringSet
}

// remoteIndex maps remoteID -> remotePresence. It is built from the candidateMap
// (already fetched) so the removal pass needs no extra API calls and uses stable
// cross-server IDs.
type remoteIndex map[string]*remotePresence

func newRemoteIndex(candidates candidateMap) remoteIndex {
	index := make(remoteIndex)
	for key, entries := range candidates {
		for _, entry := range entries {
			remoteID := entry.Remote.GetID()
			presence := index[remoteID]
			if presence == nil {
				presence = &remotePresence{
					providerKeys: newStringSet(),
					jellyfinIDs:  newStringSet(),
				}
				index[remoteID] = presence
			}
			presence.providerKeys.add(key.ProviderKey)
			presence.jellyfinIDs.add(entry.Item.JellyfinID)
		}
	}
	return index
}

// stillPresent reports whether a previously synced row still exists on its
// remote. remoteKnown is false when the row's remote is absent from the index
// (e.g. a removed/renamed remote), in which case the caller should skip the
// removal check rather than delete. A row is present if any of its provider keys
// matches, or — as a fallback for provider-key churn (synthetic → real IDs after
// a Jellyfin scan) — if its Jellyfin item ID still exists on the remote.
func (index remoteIndex) stillPresent(remoteID string, pids map[string]string, jellyfinID string) (present, remoteKnown bool) {
	presence, ok := index[remoteID]
	if !ok {
		return false, false
	}
	for k, v := range pids {
		if presence.providerKeys.has(providerKey(k, v)) {
			return true, true
		}
	}
	if presence.jellyfinIDs.has(jellyfinID) {
		return true, true
	}
	return false, true
}

const unknownResolution = "unknown"

// multiResolutionSet records, per provider key, whether more than one resolution
// exists across remotes and the target. When true, resolution-suffixed strm
// files are written so each resolution lands in its own file.
type multiResolutionSet map[string]bool

// newMultiResolutionSet unions the resolutions seen in the candidateMap (remotes)
// with those on the target, then flags provider keys with more than one. Target
// "unknown" resolutions are ignored: .strm files can't report a real resolution,
// and counting "unknown" would cause false multi-resolution detection.
func newMultiResolutionSet(candidates candidateMap, target *TargetIndex) multiResolutionSet {
	byProvider := make(resolutionIndex)
	add := func(pk, resolution string) {
		if byProvider[pk] == nil {
			byProvider[pk] = newStringSet()
		}
		byProvider[pk].add(resolution)
	}
	for key := range candidates {
		add(key.ProviderKey, key.Resolution)
	}
	for pk, resolutions := range target.Resolutions() {
		for resolution := range resolutions {
			if resolution != unknownResolution {
				add(pk, resolution)
			}
		}
	}
	multi := make(multiResolutionSet)
	for pk, resolutions := range byProvider {
		multi[pk] = len(resolutions) > 1
	}
	return multi
}

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
		if isCollectionKey(k) {
			continue
		}
		key := candidateKey{ProviderKey: providerKey(k, v), Resolution: item.Resolution}
		candidates[key] = append(candidates[key], candidate{Remote: remote, Item: item, LibraryMapping: mapping})
	}
}
