package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

type catalogueSource struct {
	ID          string      `json:"id"`
	TotalCount  int         `json:"total_count"`
	Tracks      []m3u.Track `json:"tracks"`
	Groups      []string    `json:"groups"`
	Accounts    []string    `json:"accounts"`
	LastSuccess time.Time   `json:"last_success"`
}

// Once published, this object and every track/map/byte slice it owns are immutable.
type catalogueSnapshot struct {
	generation uint64
	playlist   m3u.Playlist
	groups     []string
	lookup     map[string]int
	rendered   []byte
	sources    []catalogueSource
	success    time.Time
}

type sourceHealth struct {
	ID            string    `json:"id"`
	Count         int       `json:"count"`
	TotalCount    int       `json:"total_count"`
	LastAttempt   time.Time `json:"last_attempt"`
	LastSuccess   time.Time `json:"last_success"`
	LastErrorKind string    `json:"last_error_kind"`
}

type catalogueStatus struct {
	Ready                 bool           `json:"ready"`
	Generation            uint64         `json:"generation"`
	Count                 int            `json:"count"`
	LastSuccessfulRefresh time.Time      `json:"last_successful_refresh"`
	LastRefreshErrorKind  string         `json:"last_refresh_error_kind"`
	Refreshing            bool           `json:"refreshing"`
	Sources               []sourceHealth `json:"sources"`
}

type catalogueManager struct {
	mu      sync.RWMutex
	current *catalogueSnapshot
	health  catalogueStatus
	refresh chan struct{}
	trigger chan struct{}
	config  config.PlaylistConfig
}

func playlistConfiguration(c *config.ProxyConfig) config.PlaylistConfig {
	if c.Playlist == nil {
		return config.DefaultPlaylistConfig()
	}
	result := *c.Playlist
	result.SourceIDs = append([]string(nil), result.SourceIDs...)
	return result
}

func newCatalogueManager(pc config.PlaylistConfig, sourceCount int) *catalogueManager {
	if len(pc.SourceIDs) == 0 {
		for i := 0; i < sourceCount; i++ {
			pc.SourceIDs = append(pc.SourceIDs, fmt.Sprintf("source%d", i+1))
		}
	}
	m := &catalogueManager{config: pc, refresh: make(chan struct{}, 1), trigger: make(chan struct{}, 1)}
	for _, id := range pc.SourceIDs {
		m.health.Sources = append(m.health.Sources, sourceHealth{ID: id})
	}
	return m
}

func (m *catalogueManager) snapshot() *catalogueSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

func (m *catalogueManager) status() catalogueStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.health
	s.Sources = append([]sourceHealth(nil), s.Sources...)
	return s
}

func (c *Config) fetchCatalogue(ctx context.Context, retry bool) ([]catalogueSource, error) {
	m := c.catalogue
	sources := make([]catalogueSource, len(c.M3USources))
	done := make([]bool, len(sources))
	delay := 5 * time.Second
	for {
		var lastErr error
		for i, source := range c.M3USources {
			if done[i] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			m.mu.Lock()
			m.health.Sources[i].LastAttempt = time.Now().UTC()
			m.mu.Unlock()
			result, err := loadPlaylistSourceDetails(ctx, source, c.IncludeGroups, m.config.FetchTimeout)
			if err == nil && m.config.StableIDs && len(result.Accounts) > 1 {
				err = errors.New("multiple_account_identities")
			}
			m.mu.Lock()
			if err != nil {
				m.health.Sources[i].LastErrorKind = safeErrorKind(err)
				lastErr = err
			} else {
				now := time.Now().UTC()
				result.ID, result.LastSuccess = m.config.SourceIDs[i], now
				sources[i] = result
				m.health.Sources[i].LastSuccess = now
				m.health.Sources[i].LastErrorKind = ""
				done[i] = true
			}
			m.mu.Unlock()
		}
		if lastErr == nil {
			return sources, nil
		}
		if !retry {
			return nil, lastErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func validateSourceTracks(id string, tracks []m3u.Track, stable bool) ([]string, error) {
	accounts := make(map[string]struct{})
	for _, track := range tracks {
		_, _, account, err := stableTrackIdentity(id, track.URI)
		if err != nil {
			return nil, err
		}
		if _, err := trackPathBase(track.URI); err != nil {
			return nil, errors.New("invalid track URI path")
		}
		if account != "" {
			accounts[account] = struct{}{}
		}
	}
	if stable && len(accounts) > 1 {
		return nil, errors.New("multiple_account_identities")
	}
	return sortUniqueKeys(accounts), nil
}

func (c *Config) buildCatalogue(ctx context.Context, sources []catalogueSource, generation uint64, success time.Time) (*catalogueSnapshot, error) {
	s := &catalogueSnapshot{generation: generation, sources: sources, lookup: make(map[string]int), success: success}
	groups := make(map[string]struct{})
	identities := make(map[string]string)
	var rendered bytes.Buffer
	rendered.WriteString("#EXTM3U\n")
	for _, source := range sources {
		if source.TotalCount == 0 {
			return nil, errors.New("empty_source")
		}
		for _, group := range source.Groups {
			groups[group] = struct{}{}
		}
		for _, track := range source.Tracks {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			token := strconv.Itoa(len(s.playlist.Tracks))
			if c.catalogue.config.StableIDs {
				key, identity, _, err := stableTrackIdentity(source.ID, track.URI)
				if err != nil {
					return nil, err
				}
				if previous, exists := identities[key]; exists && previous != identity {
					return nil, errors.New("conflicting_stable_identity")
				}
				identities[key] = identity
				token = key
			}
			if _, exists := s.lookup[token]; !exists {
				s.lookup[token] = len(s.playlist.Tracks)
			}
			uri, err := c.replaceURLToken(track.URI, token, false)
			if err != nil {
				return nil, errors.New("invalid_public_track_url")
			}
			fmt.Fprintf(&rendered, "#EXTINF:%d ", track.Length)
			for i, tag := range track.Tags {
				if i > 0 {
					rendered.WriteByte(' ')
				}
				fmt.Fprintf(&rendered, "%s=%q", tag.Name, tag.Value)
			}
			fmt.Fprintf(&rendered, ", %s\n%s\n", track.Name, uri)
			s.playlist.Tracks = append(s.playlist.Tracks, track)
		}
	}
	s.groups = sortUniqueKeys(groups)
	if len(sources) > 0 && len(s.playlist.Tracks) == 0 {
		return nil, &playlistFailure{kind: "invalid_playlist", cause: noMatchingGroups(c.IncludeGroups, s.groups)}
	}
	s.rendered = rendered.Bytes()
	return s, nil
}

func (c *Config) publishCatalogue(s *catalogueSnapshot) {
	m := c.catalogue
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current = s
	m.health.Ready = len(s.playlist.Tracks) > 0
	m.health.Generation = s.generation
	m.health.Count = len(s.playlist.Tracks)
	m.health.LastSuccessfulRefresh = s.success
	m.health.LastRefreshErrorKind = ""
	for i, source := range s.sources {
		m.health.Sources[i].Count = len(source.Tracks)
		m.health.Sources[i].TotalCount = source.TotalCount
		m.health.Sources[i].LastSuccess = source.LastSuccess
	}
}

func (c *Config) bootstrapCatalogue(ctx context.Context) error {
	m := c.catalogue
	restored, restoreErr := c.restoreCatalogue(ctx)
	if restoreErr != nil {
		log.Printf("[iptv-proxy] catalogue state rejected kind=%s; attempting fresh bootstrap", restoreErr.Error())
	}
	if restored != nil {
		c.publishCatalogue(restored)
		c.RequestCatalogueRefresh()
		return nil
	}
	if m.config.StartupTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.config.StartupTimeout)
		defer cancel()
	}
	sources, err := c.fetchCatalogue(ctx, m.config.StartupTimeout > 0)
	if err != nil {
		return err
	}
	s, err := c.buildCatalogue(ctx, sources, 1, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := c.persistCatalogue(s); err != nil {
		return err
	}
	c.publishCatalogue(s)
	return nil
}

// RefreshCatalogue replaces only complete, validated metadata. It never replaces
// the relay manager or closes sessions whose requests hold older tracks.
func (c *Config) RefreshCatalogue(ctx context.Context) (err error) {
	m := c.catalogue
	if m == nil {
		return errors.New("catalogue_unavailable")
	}
	select {
	case m.refresh <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() {
		m.mu.Lock()
		m.health.Refreshing = false
		if err != nil {
			kind := safeErrorKind(err)
			if err.Error() == "account_identity_changed" || err.Error() == "conflicting_stable_identity" || err.Error() == "state_write" {
				kind = err.Error()
			}
			m.health.LastRefreshErrorKind = kind
		}
		m.mu.Unlock()
		<-m.refresh
	}()
	m.mu.Lock()
	m.health.Refreshing = true
	m.mu.Unlock()
	sources, err := c.fetchCatalogue(ctx, false)
	if err != nil {
		return err
	}
	old := m.snapshot()
	generation := uint64(1)
	if old != nil {
		generation = old.generation + 1
		for i, source := range sources {
			if !reflect.DeepEqual(source.Accounts, old.sources[i].Accounts) {
				m.mu.Lock()
				m.health.Sources[i].LastErrorKind = "account_identity_changed"
				m.mu.Unlock()
				return errors.New("account_identity_changed")
			}
		}
	}
	s, err := c.buildCatalogue(ctx, sources, generation, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.persistCatalogue(s); err != nil {
		return err
	}
	c.publishCatalogue(s)
	return nil
}

// RequestCatalogueRefresh coalesces bursts of timer/manual triggers.
func (c *Config) RequestCatalogueRefresh() {
	if c.catalogue == nil {
		return
	}
	select {
	case c.catalogue.trigger <- struct{}{}:
	default:
	}
}

func (c *Config) runCatalogueRefresh(ctx context.Context) {
	var tick <-chan time.Time
	if interval := c.catalogue.config.RefreshInterval; interval > 0 {
		timer := time.NewTicker(interval)
		defer timer.Stop()
		tick = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-c.catalogue.trigger:
		}
		// A pending timer tick and a manual trigger request the same work.
		select {
		case <-tick:
		default:
		}
		select {
		case <-c.catalogue.trigger:
		default:
		}
		if err := c.RefreshCatalogue(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[iptv-proxy] catalogue refresh failed kind=%s", c.catalogue.status().LastRefreshErrorKind)
		}
	}
}
