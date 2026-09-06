package config

import (
	"fmt"
	"regexp"
	"time"
)

// PlaylistConfig controls metadata fetches, never live-stream deadlines.
type PlaylistConfig struct {
	FetchTimeout    time.Duration
	StartupTimeout  time.Duration
	RefreshInterval time.Duration
	StateDir        string
	StableIDs       bool
	SourceIDs       []string
}

func DefaultPlaylistConfig() PlaylistConfig {
	return PlaylistConfig{FetchTimeout: 30 * time.Second}
}

var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (c PlaylistConfig) Validate(sources []string) error {
	if c.FetchTimeout <= 0 || c.StartupTimeout < 0 || c.RefreshInterval < 0 {
		return fmt.Errorf("playlist fetch timeout must be positive; startup timeout and refresh interval must be nonnegative")
	}
	if (c.StableIDs || len(c.SourceIDs) > 0) && len(c.SourceIDs) != len(sources) {
		return fmt.Errorf("m3u-source-ids must contain one ID per source")
	}
	seen := make(map[string]bool)
	for _, id := range c.SourceIDs {
		if !sourceIDPattern.MatchString(id) || seen[id] {
			return fmt.Errorf("m3u-source-ids must be unique and match [A-Za-z0-9_-]{1,64}")
		}
		seen[id] = true
	}
	return nil
}
