package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrew-avinante/JellySynnc/internal/config"
	"github.com/andrew-avinante/JellySynnc/internal/db"
	"github.com/andrew-avinante/JellySynnc/internal/jellyfin"
	"github.com/andrew-avinante/JellySynnc/internal/strm"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type candidateKey struct {
	ProviderKey string
	Resolution  string
}

// episodeFallbackKey identifies an episode by (series ProviderID, season, episode).
// Used to match episodes across servers when episode-level ProviderIDs disagree
// (e.g. TVDB renumbered episodes between scrapes) but series IDs + S/E align.
type episodeFallbackKey struct {
	SeriesProviderKey string
	Season            int
	Episode           int
}

type candidate struct {
	Remote         config.RemoteConfig
	Item           jellyfin.MediaItem
	LibraryMapping config.LibraryMapping
}

type Syncer struct {
	cfg     *config.Config
	db      *sqlx.DB
	target  *jellyfin.Client
	remotes map[string]*jellyfin.Client
}

// New constructs a Syncer from the given config and database handle, initialising
// one Jellyfin client per configured remote and one for the target server.
func New(cfg *config.Config, db *sqlx.DB) *Syncer {
	remotes := make(map[string]*jellyfin.Client, len(cfg.GetRemotes()))
	for _, r := range cfg.GetRemotes() {
		remotes[r.GetID()] = jellyfin.NewClient(r.GetAPIURL(), r.GetAPIKey())
	}
	return &Syncer{
		cfg:     cfg,
		db:      db,
		target:  jellyfin.NewClient(cfg.GetTarget().GetURL(), cfg.GetTarget().GetAPIKey()),
		remotes: remotes,
	}
}

// Run executes a full sync cycle: it builds coverage maps for the target server,
// fetches all items from every configured remote, writes missing .strm files, and
// removes .strm files whose source items have disappeared from their remote.
// A sync-run record is inserted at the start and finalised (with status and counts)
// on return, even when an error occurs.
func (s *Syncer) Run(ctx context.Context) (retErr error) {
	slog.Info("sync started")
	runID := uuid.New().String()
	if err := db.InsertSyncRun(s.db, db.SyncRun{
		ID:        runID,
		StartedAt: time.Now(),
		Status:    "running",
	}); err != nil {
		return fmt.Errorf("inserting sync run: %w", err)
	}

	var itemsAdded, itemsRemoved int
	defer func() {
		status := "ok"
		var errMsg *string
		if retErr != nil {
			status = "error"
			msg := retErr.Error()
			errMsg = &msg
		}
		slog.Info("sync complete", "status", status, "added", itemsAdded, "removed", itemsRemoved)
		if err := db.FinalizeSyncRun(s.db, runID, status, itemsAdded, itemsRemoved, errMsg); err != nil {
			slog.Warn("finalizing sync run", "err", err)
		}
	}()

	// Step 1: Build target coverage maps
	targetCoverage, targetIdentities, targetEpisodeFallback, err := s.buildTargetMaps(ctx)
	if err != nil {
		return fmt.Errorf("building target maps: %w", err)
	}

	// Provider-key-only set: lets us detect items already on target even when
	// Jellyfin can't probe .strm files and reports resolution as "unknown".
	targetProviderKeys := make(map[string]struct{}, len(targetCoverage))
	for key := range targetCoverage {
		targetProviderKeys[key.ProviderKey] = struct{}{}
	}

	// Step 2: Load and decode existing synced items from DB once
	syncedItems, err := db.GetAllSyncedItems(s.db)
	if err != nil {
		return fmt.Errorf("loading synced items: %w", err)
	}

	syncedMap := make(map[candidateKey]db.SyncedItem)
	providerKeyToDir := make(map[string]string)       // "type:id" -> dir of existing strm
	decodedPIDs := make(map[string]map[string]string) // item.ID -> decoded provider IDs

	for _, item := range syncedItems {
		var pids map[string]string
		if err := json.Unmarshal([]byte(item.ProviderIDs), &pids); err != nil {
			slog.Warn("corrupt provider_ids in db, skipping", "id", item.ID, "err", err)
			continue
		}
		decodedPIDs[item.ID] = pids
		dir := filepath.Dir(item.StrmPath)
		for k, v := range pids {
			pk := k + ":" + v
			syncedMap[candidateKey{ProviderKey: pk, Resolution: item.Resolution}] = item
			providerKeyToDir[pk] = dir
		}
	}

	// Step 3: Build candidate map (fetches all items from all remotes)
	candidateMap, err := s.buildCandidateMap(ctx)
	if err != nil {
		return fmt.Errorf("building candidate map: %w", err)
	}

	// Step 4: Compute multi-resolution set
	resolutionsByProvider := make(map[string]map[string]struct{})
	for key := range candidateMap {
		if resolutionsByProvider[key.ProviderKey] == nil {
			resolutionsByProvider[key.ProviderKey] = make(map[string]struct{})
		}
		resolutionsByProvider[key.ProviderKey][key.Resolution] = struct{}{}
	}
	for pk, resSet := range targetIdentities {
		if resolutionsByProvider[pk] == nil {
			resolutionsByProvider[pk] = make(map[string]struct{})
		}
		for res := range resSet {
			// Skip "unknown" — .strm files on target can't report resolution;
			// counting it as a real resolution causes false multi-resolution detection.
			if res != "unknown" {
				resolutionsByProvider[pk][res] = struct{}{}
			}
		}
	}

	multiResolution := make(map[string]bool)
	for pk, resolutions := range resolutionsByProvider {
		multiResolution[pk] = len(resolutions) > 1
	}

	// Step 5: Write pass
	for key, candidates := range candidateMap {
		if _, exists := targetCoverage[key]; exists {
			continue
		}
		// Item exists on target but .strm reported unknown resolution — skip unless
		// there are genuinely multiple resolutions from remotes that need separate files.
		if _, exists := targetProviderKeys[key.ProviderKey]; exists && !multiResolution[key.ProviderKey] {
			continue
		}

		winner := s.pickWinner(candidates)

		// Cross-PID coverage check: an item is registered in candidateMap once per
		// ProviderID. If ANY of the winner's other PIDs already match the target,
		// the item is on the target — skip even though this specific iteration's PID missed.
		covered := false
		for k, v := range winner.Item.ProviderIDs {
			pk := k + ":" + v
			if pk == key.ProviderKey {
				continue
			}
			if _, exists := targetCoverage[candidateKey{ProviderKey: pk, Resolution: key.Resolution}]; exists {
				covered = true
				break
			}
			if _, exists := targetProviderKeys[pk]; exists && !multiResolution[key.ProviderKey] {
				covered = true
				break
			}
		}

		// Episode-number fallback: match by (series PID, season, episode) when episode-level
		// PIDs disagree between servers (e.g. TVDB renumbering). Only for Episode items.
		if !covered && winner.Item.Type == "Episode" && winner.Item.SeasonNumber > 0 && winner.Item.EpisodeNumber > 0 {
			for k, v := range winner.Item.SeriesProviderIDs {
				spk := k + ":" + v
				fk := episodeFallbackKey{
					SeriesProviderKey: spk,
					Season:            winner.Item.SeasonNumber,
					Episode:           winner.Item.EpisodeNumber,
				}
				if _, exists := targetEpisodeFallback[fk]; exists {
					covered = true
					break
				}
			}
		}

		if covered {
			continue
		}

		var resolutionSuffix string
		if multiResolution[key.ProviderKey] {
			resolutionSuffix = key.Resolution
		}

		localPath := findExistingLocalPath(key.ProviderKey, winner.Item.ProviderIDs, providerKeyToDir)
		if localPath == "" {
			localPath = winner.LibraryMapping.GetLocalPath()
		}

		strmPath, err := strm.BuildPath(winner.Item, localPath, winner.Remote, resolutionSuffix)
		if err != nil {
			slog.Warn("building strm path", "provider", key.ProviderKey, "err", err)
			continue
		}
		strmContent, err := strm.BuildURL(winner.Item.FilePath, winner.Remote)
		if err != nil {
			slog.Warn("building strm URL", "provider", key.ProviderKey, "err", err)
			continue
		}

		existing, inDB := syncedMap[key]
		switch {
		case inDB && existing.RemoteID == winner.Remote.GetID():
			// no-op
		case inDB && existing.RemoteID != winner.Remote.GetID():
			if err := strm.Write(strmPath, strmContent); err != nil {
				slog.Warn("writing strm", "path", strmPath, "err", err)
				continue
			}
			updated := existing
			updated.RemoteID = winner.Remote.GetID()
			updated.StrmPath = strmPath
			updated.Encoding = winner.Item.Encoding
			if err := db.UpdateSyncedItem(s.db, updated); err != nil {
				slog.Warn("updating synced item", "err", err)
			}
			itemsAdded++
		default:
			if err := strm.Write(strmPath, strmContent); err != nil {
				slog.Warn("writing strm", "path", strmPath, "err", err)
				continue
			}
			provIDs, err := marshalProviderIDs(winner.Item.ProviderIDs)
			if err != nil {
				slog.Warn("marshaling provider IDs", "err", err)
				continue
			}
			if err := db.InsertSyncedItem(s.db, db.SyncedItem{
				ID:             uuid.New().String(),
				RemoteID:       winner.Remote.GetID(),
				JellyfinItemID: winner.Item.JellyfinID,
				ProviderIDs:    provIDs,
				Resolution:     key.Resolution,
				Encoding:       winner.Item.Encoding,
				StrmPath:       strmPath,
			}); err != nil {
				slog.Warn("inserting synced item", "err", err)
			}
			itemsAdded++
		}
	}

	// Step 6: Removal pass — use provider IDs from the already-fetched candidateMap
	// so we avoid N extra API calls and use stable cross-server IDs.
	remoteCurrentKeys := make(map[string]map[string]struct{})
	remoteCurrentJellyfinIDs := make(map[string]map[string]struct{})
	for key, candidates := range candidateMap {
		for _, c := range candidates {
			rid := c.Remote.GetID()
			if remoteCurrentKeys[rid] == nil {
				remoteCurrentKeys[rid] = make(map[string]struct{})
			}
			remoteCurrentKeys[rid][key.ProviderKey] = struct{}{}
			if remoteCurrentJellyfinIDs[rid] == nil {
				remoteCurrentJellyfinIDs[rid] = make(map[string]struct{})
			}
			remoteCurrentJellyfinIDs[rid][c.Item.JellyfinID] = struct{}{}
		}
	}

	for _, row := range syncedItems {
		pids, ok := decodedPIDs[row.ID]
		if !ok {
			continue // skipped above due to corrupt JSON
		}
		currentKeys, ok := remoteCurrentKeys[row.RemoteID]
		if !ok {
			slog.Warn("no remote found for synced item, skipping removal check", "remote_id", row.RemoteID)
			continue
		}
		stillExists := false
		for k, v := range pids {
			if _, found := currentKeys[k+":"+v]; found {
				stillExists = true
				break
			}
		}
		// Fallback: provider keys may have changed (e.g. synthetic → real IDs after
		// Jellyfin scans). If the Jellyfin item ID still exists on the remote the
		// item is still present — don't remove it.
		if !stillExists {
			if jellyfinIDs, ok := remoteCurrentJellyfinIDs[row.RemoteID]; ok {
				if _, found := jellyfinIDs[row.JellyfinItemID]; found {
					stillExists = true
				}
			}
		}
		if !stillExists {
			slog.Info("item gone from remote, removing strm",
				"remote_id", row.RemoteID, "path", row.StrmPath)
			if err := strm.Delete(row.StrmPath); err != nil {
				slog.Warn("deleting strm", "path", row.StrmPath, "err", err)
			}
			if err := db.DeleteSyncedItem(s.db, row.ID); err != nil {
				slog.Warn("deleting synced item", "id", row.ID, "err", err)
			}
			itemsRemoved++
		}
	}

	return nil
}

// buildTargetMaps fetches every leaf item from every library on the target server
// and returns a CoverageMap, IdentityMap, and EpisodeFallbackMap.
func (s *Syncer) buildTargetMaps(ctx context.Context) (
	CoverageMap,
	IdentityMap,
	EpisodeFallbackMap,
	error,
) {
	coverage := NewCoverageMap()
	identities := NewIdentityMap()
	episodeFallback := NewEpisodeFallbackMap()

	libs, err := s.target.GetLibraries(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getting target libraries: %w", err)
	}

	for _, lib := range libs {
		items, err := s.target.GetLeafItems(ctx, lib.ID)
		if err != nil {
			slog.Warn("getting target items", "library", lib.Name, "err", err)
			continue
		}
		for _, item := range items {
			for k, v := range item.ProviderIDs {
				pk := k + ":" + v
				key := candidateKey{ProviderKey: pk, Resolution: item.Resolution}
				coverage[key] = struct{}{}
				if identities[pk] == nil {
					identities[pk] = make(map[string]struct{})
				}
				identities[pk][item.Resolution] = struct{}{}
			}
			if item.Type == "Episode" && item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
				for k, v := range item.SeriesProviderIDs {
					spk := k + ":" + v
					episodeFallback[episodeFallbackKey{
						SeriesProviderKey: spk,
						Season:            item.SeasonNumber,
						Episode:           item.EpisodeNumber,
					}] = struct{}{}
				}
			}
		}
	}

	return coverage, identities, episodeFallback, nil
}

// buildCandidateMap fetches all leaf items from every configured remote and indexes
// them by (providerKey, resolution). Items without provider IDs are either skipped
// or assigned a synthetic "jellyfin_<remoteID>" ID when sync_unknown_provider_ids
// is enabled for the library mapping. Each map entry holds all remote candidates
// that share the same key so pickWinner can choose between them.
func (s *Syncer) buildCandidateMap(ctx context.Context) (map[candidateKey][]candidate, error) {
	candidateMap := make(map[candidateKey][]candidate)

	for _, remote := range s.cfg.GetRemotes() {
		client := s.remotes[remote.GetID()]
		libs, err := client.GetLibraries(ctx)
		if err != nil {
			slog.Warn("getting libraries for remote", "remote_id", remote.GetID(), "err", err)
			continue
		}

		libByName := make(map[string]string)
		for _, lib := range libs {
			libByName[strings.ToLower(lib.Name)] = lib.ID
		}

		for _, mapping := range remote.GetLibraryMappings() {
			libID, ok := libByName[strings.ToLower(mapping.GetRemoteName())]
			if !ok {
				slog.Warn("library not found on remote", "library", mapping.GetRemoteName(), "remote_id", remote.GetID())
				continue
			}

			slog.Info("fetching items", "library", mapping.GetRemoteName(), "remote_id", remote.GetID())
			items, err := client.GetLeafItems(ctx, libID)
			if err != nil {
				slog.Warn("getting items for library", "library", mapping.GetRemoteName(), "remote_id", remote.GetID(), "err", err)
				continue
			}

			for _, item := range items {
				if len(item.ProviderIDs) == 0 {
					if !mapping.GetSyncUnknownProviderIDs() {
						slog.Warn("skipping item with no provider IDs",
							"name", item.Name,
							"library", mapping.GetRemoteName(),
							"remote_id", remote.GetID(),
							"hint", "set sync_unknown_provider_ids: true in library mapping to sync anyway",
						)
						continue
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
					pk := k + ":" + v
					key := candidateKey{ProviderKey: pk, Resolution: item.Resolution}
					candidateMap[key] = append(candidateMap[key], candidate{
						Remote:         remote,
						Item:           item,
						LibraryMapping: mapping,
					})
				}
			}
		}
	}

	return candidateMap, nil
}

// pickWinner selects the preferred candidate from a slice that share the same
// provider key and resolution. Tie-breaker fields from the config are evaluated
// in order; the first candidate whose field value matches is returned. If no
// tie-breaker matches, the first candidate in the slice is used.
func (s *Syncer) pickWinner(candidates []candidate) candidate {
	for _, tb := range s.cfg.GetTieBreakerFields() {
		for _, c := range candidates {
			switch strings.ToLower(tb.GetField()) {
			case "encoding":
				if strings.EqualFold(c.Item.Encoding, tb.GetValue()) {
					return c
				}
			case "resolution":
				if strings.EqualFold(c.Item.Resolution, tb.GetValue()) {
					return c
				}
			}
		}
	}
	return candidates[0]
}

// findExistingLocalPath returns the directory of a previously synced strm for
// this item so multi-resolution variants land alongside existing files.
func findExistingLocalPath(providerKey string, itemProviderIDs map[string]string, providerKeyToDir map[string]string) string {
	if dir, ok := providerKeyToDir[providerKey]; ok {
		return dir
	}
	for k, v := range itemProviderIDs {
		if dir, ok := providerKeyToDir[k+":"+v]; ok {
			return dir
		}
	}
	return ""
}

// marshalProviderIDs serialises a provider-ID map to its JSON string representation
// for storage in the database.
func marshalProviderIDs(ids map[string]string) (string, error) {
	data, err := json.Marshal(ids)
	return string(data), err
}
