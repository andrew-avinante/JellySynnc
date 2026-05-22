package sync

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/andrew-avinante/JellySynnc/internal/jellyfin"
)

// debugCollector gathers raw MediaItems whose name matches one of the configured
// debug titles, from both the target and every remote. When the run finishes it
// writes a JSON report pairing each side's fields with the computed match keys and
// the target coverage verdict, so an item that "already exists but keeps syncing"
// can be diagnosed field-by-field. It is nil when no debug titles are configured;
// all methods are nil-safe so callers need no guards.
type debugCollector struct {
	titles      []string // lowercased substrings
	targetItems []debugSourceItem
	remoteItems []debugSourceItem
}

// debugSourceItem is a captured MediaItem tagged with where it came from
// ("target" or a remote ID).
type debugSourceItem struct {
	source string
	item   jellyfin.MediaItem
}

// debugReport is the JSON document written per run. Target items list their raw
// fields and match keys; remote items additionally carry the per-key coverage
// verdict, which is what the write pass uses to decide whether to skip them.
type debugReport struct {
	GeneratedAt time.Time   `json:"generated_at"`
	Titles      []string    `json:"titles"`
	TargetItems []debugItem `json:"target_items"`
	RemoteItems []debugItem `json:"remote_items"`
}

// debugItem is one media item's fields plus the keys derived from it. Coverage is
// populated for remote items only.
type debugItem struct {
	Source            string            `json:"source"`
	Name              string            `json:"name"`
	Type              string            `json:"type"`
	JellyfinID        string            `json:"jellyfin_id"`
	Resolution        string            `json:"resolution"`
	Encoding          string            `json:"encoding"`
	ProviderIDs       map[string]string `json:"provider_ids"`
	SeriesProviderIDs map[string]string `json:"series_provider_ids,omitempty"`
	SeasonNumber      int               `json:"season_number,omitempty"`
	EpisodeNumber     int               `json:"episode_number,omitempty"`
	FilePath          string            `json:"file_path"`
	ProviderKeys      []string          `json:"provider_keys"`
	Coverage          []debugCoverage   `json:"coverage,omitempty"`
}

// debugCoverage is the target coverage verdict for one of a remote item's
// candidate keys.
type debugCoverage struct {
	Key     candidateKey `json:"key"`
	Covered bool         `json:"covered"`
	Reason  string       `json:"reason"`
}

// newDebugCollector returns a collector for the given titles, or nil when none are
// configured (which disables all collection via the nil-safe methods).
func newDebugCollector(titles []string) *debugCollector {
	if len(titles) == 0 {
		return nil
	}
	lowered := make([]string, 0, len(titles))
	for _, title := range titles {
		lowered = append(lowered, strings.ToLower(title))
	}
	return &debugCollector{titles: lowered}
}

// matches reports whether name contains any configured title (case-insensitive).
func (collector *debugCollector) matches(name string) bool {
	if collector == nil {
		return false
	}
	lower := strings.ToLower(name)
	for _, title := range collector.titles {
		if strings.Contains(lower, title) {
			return true
		}
	}
	return false
}

// addTarget records a matching item from the target server.
func (collector *debugCollector) addTarget(item jellyfin.MediaItem) {
	if !collector.matches(item.Name) {
		return
	}
	collector.targetItems = append(collector.targetItems, debugSourceItem{source: "target", item: item})
}

// addRemote records a matching item from the given remote.
func (collector *debugCollector) addRemote(remoteID string, item jellyfin.MediaItem) {
	if !collector.matches(item.Name) {
		return
	}
	collector.remoteItems = append(collector.remoteItems, debugSourceItem{source: remoteID, item: item})
}

// write builds the report and writes it to path as indented JSON. It is a no-op
// when the collector is nil (debug disabled).
func (collector *debugCollector) write(path string, target *TargetIndex, multiRes multiResolutionSet) error {
	if collector == nil {
		return nil
	}
	report := collector.report(target, multiRes)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling debug report: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing debug report to %s: %w", path, err)
	}
	slog.Info("wrote debug report", "path", path,
		"target_items", len(report.TargetItems), "remote_items", len(report.RemoteItems))
	return nil
}

// report assembles the report. Remote items get a coverage verdict per candidate
// key against the target index.
func (collector *debugCollector) report(target *TargetIndex, multiRes multiResolutionSet) debugReport {
	report := debugReport{
		GeneratedAt: time.Now(),
		Titles:      collector.titles,
	}
	for _, captured := range collector.targetItems {
		report.TargetItems = append(report.TargetItems, newDebugItem(captured))
	}
	for _, captured := range collector.remoteItems {
		entry := newDebugItem(captured)
		entry.Coverage = evaluateCoverage(captured.item, target, multiRes)
		report.RemoteItems = append(report.RemoteItems, entry)
	}
	return report
}

// newDebugItem copies the common MediaItem fields and computed provider keys into
// a debugItem. Coverage is left empty; callers add it for remote items.
func newDebugItem(captured debugSourceItem) debugItem {
	item := captured.item
	return debugItem{
		Source:            captured.source,
		Name:              item.Name,
		Type:              item.Type,
		JellyfinID:        item.JellyfinID,
		Resolution:        item.Resolution,
		Encoding:          item.Encoding,
		ProviderIDs:       item.ProviderIDs,
		SeriesProviderIDs: item.SeriesProviderIDs,
		SeasonNumber:      item.SeasonNumber,
		EpisodeNumber:     item.EpisodeNumber,
		FilePath:          item.FilePath,
		ProviderKeys:      providerKeysOf(item.ProviderIDs),
	}
}

// evaluateCoverage runs the target coverage check for each of the item's candidate
// keys, returning one verdict per (providerKey, resolution) pair.
func evaluateCoverage(item jellyfin.MediaItem, target *TargetIndex, multiRes multiResolutionSet) []debugCoverage {
	var out []debugCoverage
	for k, v := range item.ProviderIDs {
		key := candidateKey{ProviderKey: providerKey(k, v), Resolution: item.Resolution}
		covered, reason := target.coverWhy(key, item, multiRes)
		out = append(out, debugCoverage{Key: key, Covered: covered, Reason: reason})
	}
	return out
}

// providerKeysOf returns the canonical "type:id" keys for a provider-ID map.
func providerKeysOf(ids map[string]string) []string {
	keys := make([]string, 0, len(ids))
	for k, v := range ids {
		keys = append(keys, providerKey(k, v))
	}
	return keys
}
