package config

import (
	"fmt"
	"time"
)

// RelayConfig controls continuous live TS delivery, not playback caching.
type RelayConfig struct {
	Enabled             bool
	ExistingChannelWins bool
	IdleTimeout         time.Duration
	ReconnectInitial    time.Duration
	ReconnectMax        time.Duration
	ReadTimeout         time.Duration
}

func DefaultRelayConfig() RelayConfig {
	return RelayConfig{
		Enabled:          true,
		IdleTimeout:      30 * time.Second,
		ReconnectInitial: time.Second,
		ReconnectMax:     10 * time.Second,
		ReadTimeout:      15 * time.Second,
	}
}

func (c RelayConfig) Validate() error {
	if c.ExistingChannelWins && !c.Enabled {
		return fmt.Errorf("relay-existing-channel-wins requires relay-enabled")
	}
	if c.IdleTimeout < 0 {
		return fmt.Errorf("relay-idle-timeout must not be negative")
	}
	if c.ReconnectInitial <= 0 || c.ReconnectMax < c.ReconnectInitial {
		return fmt.Errorf("relay reconnect durations must be positive and max >= initial")
	}
	if c.ReadTimeout <= 0 {
		return fmt.Errorf("relay-read-timeout must be positive")
	}
	return nil
}
