package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrewavinante/JellySynnc/internal/config"
	"github.com/andrewavinante/JellySynnc/internal/db"
	"github.com/andrewavinante/JellySynnc/internal/jellyfin"
	"github.com/andrewavinante/JellySynnc/internal/strm"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type candidateKey struct {
	ProviderKey string
	Resolution  string
}

type candidate struct {
	RemoteIdx      int
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
	targetCoverage, targetIdentities, err := s.buildTargetMaps(ctx)
	if err != nil {
		return fmt.Errorf("building target maps: %w", err)
	}

	// Step 2: Load existing synced items from DB
	syncedItems, err := db.GetAllSyncedItems(s.db)
	if err != nil {
		return fmt.Errorf("loading synced items: %w", err)
	}

	syncedMap := make(map[candidateKey]db.SyncedItem)
	for _, item := range syncedItems {
		var pids map[string]string
		if err := json.Unmarshal([]byte(item.ProviderIDs), &pids); err != nil {
			continue
		}
		for k, v := range pids {
			key := candidateKey{ProviderKey: k + ":" + v, Resolution: item.Resolution}
			syncedMap[key] = item
		}
	}

	// Step 3: Build candidate map
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
			resolutionsByProvider[pk][res] = struct{}{}
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

		winner := s.pickWinner(candidates)

		var resolutionSuffix string
		if multiResolution[key.ProviderKey] {
			resolutionSuffix = key.Resolution
		}

		localPath := s.findExistingLocalPath(key.ProviderKey, winner.Item.ProviderIDs, syncedItems, syncedMap)
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
			if err := db.InsertSyncedItem(s.db, db.SyncedItem{
				ID:             uuid.New().String(),
				RemoteID:       winner.Remote.GetID(),
				JellyfinItemID: winner.Item.JellyfinID,
				ProviderIDs:    marshalProviderIDs(winner.Item.ProviderIDs),
				Resolution:     key.Resolution,
				Encoding:       winner.Item.Encoding,
				StrmPath:       strmPath,
			}); err != nil {
				slog.Warn("inserting synced item", "err", err)
			}
			itemsAdded++
		}
	}

	// Step 6: Removal pass
	for _, row := range syncedItems {
		remote, ok := s.remotes[row.RemoteID]
		if !ok {
			slog.Warn("no client for remote, skipping removal check", "remote_id", row.RemoteID)
			continue
		}

		_, err := remote.GetItem(ctx, row.JellyfinItemID)
		if errors.Is(err, jellyfin.ErrItemNotFound) {
			slog.Info("item gone from remote, removing strm", "item_id", row.JellyfinItemID, "remote_id", row.RemoteID, "path", row.StrmPath)
			if err := strm.Delete(row.StrmPath); err != nil {
				slog.Warn("deleting strm", "path", row.StrmPath, "err", err)
			}
			if err := db.DeleteSyncedItem(s.db, row.ID); err != nil {
				slog.Warn("deleting synced item", "id", row.ID, "err", err)
			}
			itemsRemoved++
		} else if err != nil {
			slog.Warn("checking item on remote", "item_id", row.JellyfinItemID, "remote_id", row.RemoteID, "err", err)
		}
	}

	return nil
}

func (s *Syncer) buildTargetMaps(ctx context.Context) (map[candidateKey]struct{}, map[string]map[string]struct{}, error) {
	coverage := make(map[candidateKey]struct{})
	identities := make(map[string]map[string]struct{})

	libs, err := s.target.GetLibraries(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("getting target libraries: %w", err)
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
		}
	}

	return coverage, identities, nil
}

func (s *Syncer) buildCandidateMap(ctx context.Context) (map[candidateKey][]candidate, error) {
	candidateMap := make(map[candidateKey][]candidate)

	for remoteIdx, remote := range s.cfg.GetRemotes() {
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
				for k, v := range item.ProviderIDs {
					pk := k + ":" + v
					key := candidateKey{ProviderKey: pk, Resolution: item.Resolution}
					candidateMap[key] = append(candidateMap[key], candidate{
						RemoteIdx:      remoteIdx,
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

func (s *Syncer) findExistingLocalPath(providerKey string, itemProviderIDs map[string]string, syncedItems []db.SyncedItem, syncedMap map[candidateKey]db.SyncedItem) string {
	// Check syncedMap for any entry with same providerKey (any resolution)
	for key, item := range syncedMap {
		if key.ProviderKey == providerKey {
			return filepath.Dir(item.StrmPath)
		}
	}

	// Scan all synced items for any matching provider ID
	for _, row := range syncedItems {
		var pids map[string]string
		if err := json.Unmarshal([]byte(row.ProviderIDs), &pids); err != nil {
			continue
		}
		for k, v := range itemProviderIDs {
			if pids[k] == v {
				return filepath.Dir(row.StrmPath)
			}
		}
	}

	return ""
}

func marshalProviderIDs(ids map[string]string) string {
	data, _ := json.Marshal(ids)
	return string(data)
}
