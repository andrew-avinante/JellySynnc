package sync

// CoverageMap tracks (providerKey, resolution) pairs already present on the target server.
type CoverageMap map[candidateKey]struct{}

func NewCoverageMap() CoverageMap { return make(CoverageMap) }

// IdentityMap tracks providerKey → set of resolutions seen on the target server.
// Used to detect when multiple resolutions of the same item exist.
type IdentityMap map[string]map[string]struct{}

func NewIdentityMap() IdentityMap { return make(IdentityMap) }

// EpisodeFallbackMap tracks (seriesProviderKey, season, episode) tuples for episodes.
// Enables S/E-based matching when episode-level provider IDs disagree between servers.
type EpisodeFallbackMap map[episodeFallbackKey]struct{}

func NewEpisodeFallbackMap() EpisodeFallbackMap { return make(EpisodeFallbackMap) }
