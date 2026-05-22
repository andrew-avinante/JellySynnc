package sync

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
