package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/andrew-avinante/JellySynnc/internal/config"
	"github.com/andrew-avinante/JellySynnc/internal/db"
	"github.com/andrew-avinante/JellySynnc/internal/jellyfin"
	"github.com/andrew-avinante/JellySynnc/internal/strm"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type Syncer struct {
	cfg     *config.Config
	db      *sqlx.DB
	target  *jellyfin.Client
	remotes map[string]*jellyfin.Client
	debug   *debugCollector
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
		debug:   newDebugCollector(cfg.GetDebugTitles()),
	}
}

// Run executes a full sync cycle: it builds coverage maps for the target server,
// fetches all items from every configured remote, writes missing .strm files, and
// removes .strm files whose source items have disappeared from their remote.
// A sync-run record is inserted at the start and finalised (with status and counts)
// on return, even when an error occurs.
func (s *Syncer) Run(ctx context.Context) (retErr error) {
	runID, err := s.startRun()
	if err != nil {
		return err
	}
	var itemsAdded, itemsRemoved int
	defer func() { s.finishRun(runID, itemsAdded, itemsRemoved, retErr) }()

	// Coverage of what already exists on the target server.
	targetIndex, err := s.buildTargetIndex(ctx)
	if err != nil {
		return fmt.Errorf("building target index: %w", err)
	}

	// Previously synced items, decoded once for the write and removal passes.
	syncedItems, err := db.GetAllSyncedItems(s.db)
	if err != nil {
		return fmt.Errorf("loading synced items: %w", err)
	}
	synced := newSyncedIndex(syncedItems)

	// Everything currently available across all remotes.
	candidateMap, err := s.buildCandidateMap(ctx)
	if err != nil {
		return fmt.Errorf("building candidate map: %w", err)
	}

	multiResolution := newMultiResolutionSet(candidateMap, targetIndex)

	itemsAdded = s.writePass(candidateMap, targetIndex, multiResolution, synced)
	itemsRemoved = s.removalPass(syncedItems, synced, candidateMap)

	if err := s.debug.write(s.cfg.GetDebugOutputPath(), targetIndex, multiResolution); err != nil {
		slog.Warn("writing debug report", "err", err)
	}
	return nil
}

// Verify reconciles the synced_items table against reality on the target. A synced
// row is stale when its .strm file is missing on disk OR the target server no
// longer indexes the item by provider key. Stale rows are always logged; when apply
// is true they are deleted from the database. The .strm files themselves are left
// untouched: a missing one is already gone, and a present-but-unindexed one is left
// for the next sync to re-evaluate.
func (s *Syncer) Verify(ctx context.Context, apply bool) error {
	targetIndex, err := s.buildTargetIndex(ctx)
	if err != nil {
		return fmt.Errorf("building target index: %w", err)
	}

	syncedItems, err := db.GetAllSyncedItems(s.db)
	if err != nil {
		return fmt.Errorf("loading synced items: %w", err)
	}

	stale := findStale(syncedItems, targetIndex)
	logStale(stale, apply)

	removed := 0
	if apply {
		removed = s.deleteStale(stale)
	}
	slog.Info("verify complete",
		"checked", len(syncedItems), "stale", len(stale), "removed", removed, "apply", apply)
	return nil
}

// startRun logs the start of a sync cycle and inserts a "running" sync-run record,
// returning its ID for later finalisation.
func (s *Syncer) startRun() (string, error) {
	slog.Info("sync started")
	runID := uuid.New().String()
	if err := db.InsertSyncRun(s.db, db.SyncRun{
		ID:        runID,
		StartedAt: time.Now(),
		Status:    "running",
	}); err != nil {
		return "", fmt.Errorf("inserting sync run: %w", err)
	}
	return runID, nil
}

// finishRun finalises the sync-run record with status and counts. A non-nil
// retErr marks the run as errored and stores its message.
func (s *Syncer) finishRun(runID string, itemsAdded, itemsRemoved int, retErr error) {
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
}

// writePass writes strm files for every candidate the target doesn't already
// cover, returning how many items were added or re-pointed to a new remote.
func (s *Syncer) writePass(candidates candidateMap, target *TargetIndex, multiRes multiResolutionSet, synced syncedIndex) int {
	itemsAdded := 0
	// A single item carries several provider IDs, so the same item appears under
	// multiple candidateKeys here. written tracks strm paths already handled this
	// run so each physical file yields exactly one DB row (newSyncedIndex fans that
	// one row back out across every provider key, so coverage is unaffected).
	written := newStringSet()
	for key, group := range candidates {
		winner := s.pickWinner(group)
		if target.covers(key, winner.Item, multiRes) {
			continue
		}
		if s.writeWinner(key, winner, multiRes, synced, written) {
			itemsAdded++
		}
	}
	return itemsAdded
}

// removalPass deletes strm files (and their DB rows) whose source item has
// disappeared from its remote, returning how many were removed. It uses the
// already-fetched candidateMap (via remoteIndex) to avoid extra API calls.
func (s *Syncer) removalPass(syncedItems []db.SyncedItem, synced syncedIndex, candidates candidateMap) int {
	remotes := newRemoteIndex(candidates)
	itemsRemoved := 0
	for _, row := range syncedItems {
		pids, ok := synced.pidsByItemID[row.ID]
		if !ok {
			continue // skipped during decode due to corrupt JSON
		}
		present, remoteKnown := remotes.stillPresent(row.RemoteID, pids, row.JellyfinItemID)
		if !remoteKnown {
			slog.Warn("no remote found for synced item, skipping removal check", "remote_id", row.RemoteID)
			continue
		}
		if present {
			continue
		}
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
	return itemsRemoved
}

// deleteStale removes the database row for each stale item, returning how many were
// deleted. The .strm files are intentionally not touched. Failures are logged and
// skipped so one bad row doesn't abort the rest.
func (s *Syncer) deleteStale(stale []staleItem) int {
	removed := 0
	for _, item := range stale {
		if err := db.DeleteSyncedItem(s.db, item.Row.ID); err != nil {
			slog.Warn("deleting synced item", "id", item.Row.ID, "err", err)
			continue
		}
		removed++
	}
	return removed
}

// writeWinner writes the strm file for the chosen candidate and records it in the
// DB, returning true when an item was added or re-pointed to a new remote. It is
// only called for items the target doesn't already cover.
func (s *Syncer) writeWinner(key candidateKey, winner candidate, multiRes multiResolutionSet, synced syncedIndex, written stringSet) bool {
	strmPath, strmContent, ok := s.resolveStrm(key, winner, multiRes, synced)
	if !ok {
		return false
	}
	if written.has(strmPath) {
		return false // another provider key of this item already wrote this file
	}
	written.add(strmPath)
	return s.persistWinner(key, winner, synced, strmPath, strmContent)
}

// resolveStrm computes the destination path and URL content for a winning
// candidate's strm file. ok is false (with the reason logged) when either can't
// be built, in which case the candidate is skipped. Multi-resolution provider
// keys get a resolution suffix; the directory of an existing variant is reused
// when present so variants land together.
func (s *Syncer) resolveStrm(key candidateKey, winner candidate, multiRes multiResolutionSet, synced syncedIndex) (strmPath, strmContent string, ok bool) {
	var resolutionSuffix string
	if multiRes[key.ProviderKey] {
		resolutionSuffix = key.Resolution
	}

	localPath := findExistingLocalPath(key.ProviderKey, winner.Item.ProviderIDs, synced.dirByProviderKey)
	if localPath == "" {
		localPath = winner.LibraryMapping.GetLocalPath()
	}

	strmPath, err := strm.BuildPath(winner.Item, localPath, winner.Remote, resolutionSuffix)
	if err != nil {
		slog.Warn("building strm path", "provider", key.ProviderKey, "err", err)
		return "", "", false
	}
	strmContent, err = strm.BuildURL(winner.Item.FilePath, winner.Remote)
	if err != nil {
		slog.Warn("building strm URL", "provider", key.ProviderKey, "err", err)
		return "", "", false
	}
	return strmPath, strmContent, true
}

// persistWinner writes the strm file and records it in the DB. It is a no-op
// (false) when the item is already synced from the same remote. A failed strm
// write aborts without a DB change (false); DB errors are logged but the item
// still counts as added (true), matching the original write-pass behavior.
func (s *Syncer) persistWinner(key candidateKey, winner candidate, synced syncedIndex, strmPath, strmContent string) bool {
	existing, inDB := synced.byKey[key]
	if inDB && existing.RemoteID == winner.Remote.GetID() {
		return false // already synced from this remote
	}

	if err := strm.Write(strmPath, strmContent); err != nil {
		slog.Warn("writing strm", "path", strmPath, "err", err)
		return false
	}

	if inDB {
		// Item moved to a different remote — re-point the existing row.
		updated := existing
		updated.RemoteID = winner.Remote.GetID()
		updated.StrmPath = strmPath
		updated.Encoding = winner.Item.Encoding
		if err := db.UpdateSyncedItem(s.db, updated); err != nil {
			slog.Warn("updating synced item", "err", err)
		}
		return true
	}

	provIDs, err := marshalProviderIDs(winner.Item.ProviderIDs)
	if err != nil {
		slog.Warn("marshaling provider IDs", "err", err)
		return false
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
	return true
}

// buildTargetIndex fetches every leaf item from every library on the target server
// and returns a TargetIndex for coverage lookups during the sync write and removal passes.
func (s *Syncer) buildTargetIndex(ctx context.Context) (*TargetIndex, error) {
	index := NewTargetIndex()

	libs, err := s.target.GetLibraries(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting target libraries: %w", err)
	}

	var mediaItems []jellyfin.MediaItem
	for _, lib := range libs {
		items, err := s.target.GetLeafItems(ctx, lib.ID)
		if err != nil {
			slog.Warn("getting target items", "library", lib.Name, "err", err)
			continue
		}
		mediaItems = append(mediaItems, items...)
	}

	for _, item := range mediaItems {
		index.Add(item)
		s.debug.addTarget(item)
	}

	return index, nil
}

// buildCandidateMap fetches all leaf items from every configured remote and indexes
// them by (providerKey, resolution). Items without provider IDs are either skipped
// or assigned a synthetic "jellyfin_<remoteID>" ID when sync_unknown_provider_ids
// is enabled for the library mapping. Each map entry holds all remote candidates
// that share the same key so pickWinner can choose between them.
func (s *Syncer) buildCandidateMap(ctx context.Context) (candidateMap, error) {
	out := newCandidateMap()

	for _, remote := range s.cfg.GetRemotes() {
		client := s.remotes[remote.GetID()]
		libs, err := client.GetLibraries(ctx)
		if err != nil {
			slog.Warn("getting libraries for remote", "remote_id", remote.GetID(), "err", err)
			continue
		}

		libIndex := buildLibraryIndex(libs)

		for _, mapping := range remote.GetLibraryMappings() {
			libID, ok := libIndex[strings.ToLower(mapping.GetRemoteName())]
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
				s.debug.addRemote(remote.GetID(), item)
				out.add(item, remote, mapping)
			}
		}
	}

	return out, nil
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
func findExistingLocalPath(pk string, itemProviderIDs map[string]string, providerKeyToDir map[string]string) string {
	if dir, ok := providerKeyToDir[pk]; ok {
		return dir
	}
	for k, v := range itemProviderIDs {
		if dir, ok := providerKeyToDir[providerKey(k, v)]; ok {
			return dir
		}
	}
	return ""
}

// providerKey builds the canonical "type:id" string used to key items by provider ID.
func providerKey(k, v string) string {
	return k + ":" + v
}

// isCollectionKey reports whether a provider-ID key identifies a collection
// (e.g. "tmdbcollection", "tvdbcollection") rather than a single item. Collection
// IDs are shared by every movie in a franchise, so keying coverage or candidates
// on them makes one synced movie falsely cover its still-missing siblings.
func isCollectionKey(k string) bool {
	return strings.HasSuffix(strings.ToLower(k), "collection")
}

// marshalProviderIDs serialises a provider-ID map to its JSON string representation
// for storage in the database.
func marshalProviderIDs(ids map[string]string) (string, error) {
	data, err := json.Marshal(ids)
	return string(data), err
}

// buildLibraryIndex returns a case-insensitive name-to-ID map for a set of libraries.
func buildLibraryIndex(libs []jellyfin.Library) map[string]string {
	index := make(map[string]string, len(libs))
	for _, lib := range libs {
		index[strings.ToLower(lib.Name)] = lib.ID
	}
	return index
}

// findStale returns the synced rows that are no longer backed by reality on the
// target, each paired with the reason it was flagged.
func findStale(rows []db.SyncedItem, target *TargetIndex) []staleItem {
	var stale []staleItem
	for _, row := range rows {
		if reason, ok := staleReason(row, target); ok {
			stale = append(stale, staleItem{Row: row, Reason: reason})
		}
	}
	return stale
}

// staleReason reports whether a synced row is stale and why. A row is stale when its
// .strm file is missing OR a conclusive target check finds no matching provider key.
// The target check is inconclusive (and so never flags "not on target") when the row
// has no real provider IDs to match — only synthetic "jellyfin_*" or collection keys,
// neither of which exist on the target. A row whose provider_ids JSON is corrupt
// can't be checked against the target at all: it's flagged stale only if its file is
// also missing, otherwise skipped (logged) to avoid deleting a row we can't verify.
func staleReason(row db.SyncedItem, target *TargetIndex) (string, bool) {
	fileMissing := strmFileMissing(row.StrmPath)

	pids, ok := decodeProviderIDs(row.ProviderIDs)
	if !ok {
		if fileMissing {
			return "strm file missing (provider_ids corrupt)", true
		}
		slog.Warn("corrupt provider_ids, skipping target check", "id", row.ID)
		return "", false
	}

	onTarget, checkable := targetCovers(pids, row.Resolution, target)
	notOnTarget := checkable && !onTarget
	switch {
	case fileMissing && notOnTarget:
		return "strm file missing and not on target", true
	case fileMissing:
		return "strm file missing", true
	case notOnTarget:
		return "not on target", true
	default:
		return "", false
	}
}

// logStale logs each stale row with its reason. dry_run is true when no deletion
// will follow, so the output makes clear whether rows are being removed or only
// reported.
func logStale(stale []staleItem, apply bool) {
	for _, item := range stale {
		slog.Info("stale synced item",
			"reason", item.Reason,
			"remote_id", item.Row.RemoteID,
			"resolution", item.Row.Resolution,
			"strm_path", item.Row.StrmPath,
			"id", item.Row.ID,
			"dry_run", !apply)
	}
}

// targetCovers reports whether any of the row's provider keys (at its resolution, or
// at any resolution) is present on the target. checkable is false when the row has no
// real provider IDs to test — only synthetic or collection keys are skipped — so the
// caller can distinguish "confirmed absent" from "couldn't check".
func targetCovers(pids map[string]string, resolution string, target *TargetIndex) (covered, checkable bool) {
	for k, v := range pids {
		if isCollectionKey(k) || isSyntheticKey(k) {
			continue
		}
		checkable = true
		pk := providerKey(k, v)
		if target.Has(candidateKey{ProviderKey: pk, Resolution: resolution}) || target.HasProviderKey(pk) {
			return true, true
		}
	}
	return false, checkable
}

// isSyntheticKey reports whether a provider-ID key is a synthetic "jellyfin_<remoteID>"
// identifier injected for items with no real provider IDs (sync_unknown_provider_ids).
// Such keys exist only in this DB, never on the target, so they can't confirm presence.
func isSyntheticKey(k string) bool {
	return strings.HasPrefix(k, "jellyfin_")
}

// strmFileMissing reports whether the file at path does not exist. A stat error other
// than "not found" (e.g. a permission error) returns false so an inconclusive check
// never flags the row for deletion.
func strmFileMissing(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return false
}

// decodeProviderIDs unmarshals the JSON provider-ID map stored on a synced row.
// ok is false when the JSON is corrupt.
func decodeProviderIDs(raw string) (map[string]string, bool) {
	var pids map[string]string
	if err := json.Unmarshal([]byte(raw), &pids); err != nil {
		return nil, false
	}
	return pids, true
}
