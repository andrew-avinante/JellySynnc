package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "github.com/sj14/jellyfin-go/api"
)

var ErrItemNotFound = errors.New("item not found")

type Library struct {
	ID   string
	Name string
	Type string
}

type MediaItem struct {
	JellyfinID     string
	Name           string
	Type           string
	ProviderIDs    map[string]string
	FilePath       string
	Resolution     string
	Encoding       string
	SeriesName     string
	SeriesID       string
	SeasonNumber   int
	EpisodeNumber  int
}

type Client struct {
	baseURL string
	apiKey  string
	inner   *api.APIClient
}

func NewClient(baseURL, apiKey string) *Client {
	cfg := &api.Configuration{
		Servers:       api.ServerConfigurations{{URL: baseURL}},
		DefaultHeader: map[string]string{"Authorization": fmt.Sprintf(`MediaBrowser Token="%s"`, apiKey)},
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		inner:   api.NewAPIClient(cfg),
	}
}

func (c *Client) GetLibraries(ctx context.Context) ([]Library, error) {
	folders, _, err := c.inner.LibraryStructureAPI.GetVirtualFolders(ctx).Execute()
	if err != nil {
		return nil, fmt.Errorf("GetVirtualFolders: %w", err)
	}

	libs := make([]Library, 0, len(folders))
	for _, f := range folders {
		lib := Library{
			ID:   f.GetItemId(),
			Name: f.GetName(),
		}
		if ct, ok := f.GetCollectionTypeOk(); ok && ct != nil {
			lib.Type = string(*ct)
		}
		libs = append(libs, lib)
	}
	return libs, nil
}

func (c *Client) GetLeafItems(ctx context.Context, libraryID string) ([]MediaItem, error) {
	result, _, err := c.inner.ItemsAPI.GetItems(ctx).
		ParentId(libraryID).
		Fields([]api.ItemFields{
			api.ITEMFIELDS_PROVIDER_IDS,
			api.ITEMFIELDS_PATH,
			api.ITEMFIELDS_MEDIA_SOURCES,
		}).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("GetItems(parentId=%s): %w", libraryID, err)
	}

	var leaves []MediaItem
	for _, item := range result.GetItems() {
		switch item.GetType() {
		case api.BASEITEMKIND_MOVIE, api.BASEITEMKIND_EPISODE:
			mi, err := c.enrichItem(ctx, item)
			if err != nil {
				continue
			}
			leaves = append(leaves, mi)
		default:
			children, err := c.GetLeafItems(ctx, item.GetId())
			if err != nil {
				continue
			}
			leaves = append(leaves, children...)
		}
	}
	return leaves, nil
}

func (c *Client) GetItem(ctx context.Context, itemID string) (MediaItem, error) {
	result, _, err := c.inner.ItemsAPI.GetItems(ctx).
		Ids([]string{itemID}).
		Fields([]api.ItemFields{
			api.ITEMFIELDS_PROVIDER_IDS,
			api.ITEMFIELDS_PATH,
			api.ITEMFIELDS_MEDIA_SOURCES,
		}).
		Execute()
	if err != nil {
		return MediaItem{}, fmt.Errorf("GetItems(id=%s): %w", itemID, err)
	}

	items := result.GetItems()
	if len(items) == 0 {
		return MediaItem{}, ErrItemNotFound
	}

	return c.enrichItem(ctx, items[0])
}

func (c *Client) enrichItem(ctx context.Context, item api.BaseItemDto) (MediaItem, error) {
	mi := MediaItem{
		JellyfinID:    item.GetId(),
		Name:          item.GetName(),
		Type:          string(item.GetType()),
		ProviderIDs:   normalizeProviderIDs(item.GetProviderIds()),
		SeriesName:    item.GetSeriesName(),
		SeriesID:      item.GetSeriesId(),
		SeasonNumber:  int(item.GetParentIndexNumber()),
		EpisodeNumber: int(item.GetIndexNumber()),
	}

	pbInfo, _, err := c.inner.MediaInfoAPI.GetPlaybackInfo(ctx, item.GetId()).Execute()
	if err != nil {
		return MediaItem{}, fmt.Errorf("GetPlaybackInfo(%s): %w", item.GetId(), err)
	}

	sources := pbInfo.GetMediaSources()
	if len(sources) == 0 {
		mi.Resolution = "unknown"
		mi.Encoding = "unknown"
		return mi, nil
	}

	src := sources[0]
	mi.FilePath = src.GetPath()

	for _, stream := range src.GetMediaStreams() {
		if stream.GetType() == api.MEDIASTREAMTYPE_VIDEO {
			mi.Resolution = normalizeResolution(stream.GetHeight())
			mi.Encoding = normalizeEncoding(stream.GetCodec())
			break
		}
	}

	if mi.Resolution == "" {
		mi.Resolution = "unknown"
	}
	if mi.Encoding == "" {
		mi.Encoding = "unknown"
	}

	return mi, nil
}

func normalizeProviderIDs(ids map[string]string) map[string]string {
	out := make(map[string]string, len(ids))
	for k, v := range ids {
		out[strings.ToLower(k)] = v
	}
	return out
}

func normalizeResolution(height int32) string {
	switch {
	case height <= 0:
		return "unknown"
	case height <= 480:
		return "480p"
	case height <= 720:
		return "720p"
	case height <= 1080:
		return "1080p"
	default:
		return "4K"
	}
}

func normalizeEncoding(codec string) string {
	switch strings.ToLower(codec) {
	case "hevc", "h265":
		return "h265"
	case "avc", "h264":
		return "h264"
	case "av1":
		return "av1"
	case "":
		return "unknown"
	default:
		return strings.ToLower(codec)
	}
}
