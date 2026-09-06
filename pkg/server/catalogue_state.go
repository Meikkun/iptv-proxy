package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const catalogueStateVersion = 1
const maxCatalogueStateBytes = 128 << 20

type catalogueState struct {
	Version     int               `json:"version"`
	Fingerprint string            `json:"fingerprint"`
	Generation  uint64            `json:"generation"`
	Success     time.Time         `json:"last_success"`
	Sources     []catalogueSource `json:"sources"`
}

func (c *Config) catalogueFingerprint() string {
	data, _ := json.Marshal(struct {
		Sources []string
		IDs     []string
		Groups  []string
		Stable  bool
	}{c.M3USources, c.catalogue.config.SourceIDs, normalizeGroups(c.IncludeGroups), c.catalogue.config.StableIDs})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (c *Config) restoreCatalogue(ctx context.Context) (*catalogueSnapshot, error) {
	dir := c.catalogue.config.StateDir
	if dir == "" {
		return nil, nil
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("state_permissions")
	}
	filename := filepath.Join(dir, "catalogue.json")
	info, err = os.Lstat(filename)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("state_permissions")
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, errors.New("state_read")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(&playlistContextReader{ctx: ctx, reader: f}, maxCatalogueStateBytes+1))
	if err != nil || len(raw) > maxCatalogueStateBytes {
		return nil, errors.New("state_read")
	}
	var state catalogueState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, errors.New("state_malformed")
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return nil, errors.New("state_malformed")
	}
	if state.Version != catalogueStateVersion || state.Fingerprint != c.catalogueFingerprint() {
		return nil, errors.New("state_incompatible")
	}
	if state.Generation == 0 || state.Success.IsZero() || len(state.Sources) != len(c.M3USources) {
		return nil, errors.New("state_invalid")
	}
	for i, source := range state.Sources {
		if source.ID != c.catalogue.config.SourceIDs[i] || source.LastSuccess.IsZero() || source.TotalCount <= 0 || source.TotalCount < len(source.Tracks) {
			return nil, errors.New("state_invalid")
		}
		accounts, err := validateSourceTracks(source.ID, source.Tracks, c.catalogue.config.StableIDs)
		if err != nil || (c.catalogue.config.StableIDs && len(source.Accounts) > 1) {
			return nil, errors.New("state_invalid")
		}
		accountSet := make(map[string]bool)
		for _, account := range source.Accounts {
			if !stableTokenPattern.MatchString("s"+account) || accountSet[account] {
				return nil, errors.New("state_invalid")
			}
			accountSet[account] = true
		}
		for _, account := range accounts {
			if !accountSet[account] {
				return nil, errors.New("state_invalid")
			}
		}
		for _, track := range source.Tracks {
			if strings.ContainsAny(track.Name, "\r\n") {
				return nil, errors.New("state_invalid")
			}
			for _, tag := range track.Tags {
				if strings.ContainsAny(tag.Name+tag.Value, "\r\n") {
					return nil, errors.New("state_invalid")
				}
			}
			if patterns := normalizeGroups(c.IncludeGroups); len(patterns) > 0 && !groupMatchesAnyPattern(trackGroup(track), patterns) {
				return nil, errors.New("state_invalid")
			}
		}
	}
	return c.buildCatalogue(ctx, state.Sources, state.Generation, state.Success)
}

func (c *Config) persistCatalogue(s *catalogueSnapshot) error {
	dir := c.catalogue.config.StateDir
	if dir == "" {
		return nil
	}
	state := catalogueState{Version: catalogueStateVersion, Fingerprint: c.catalogueFingerprint(), Generation: s.generation, Success: s.success, Sources: s.sources}
	if err := writeCatalogueState(dir, state); err != nil {
		return errors.New("state_write")
	}
	return nil
}

// The previous inode remains linked until the replacement and directory sync
// succeed, so a failed rename/sync can restore the previous disk generation.
func writeCatalogueState(dir string, state catalogueState) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("state directory must not be a symlink")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	filename := filepath.Join(dir, "catalogue.json")
	info, err = os.Lstat(filename)
	existed := err == nil
	if existed && !info.Mode().IsRegular() {
		return errors.New("state must be a regular file")
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next, err := os.CreateTemp(dir, ".catalogue-next-*")
	if err != nil {
		return err
	}
	defer os.Remove(next.Name())
	defer next.Close()
	if err := next.Chmod(0600); err != nil {
		return err
	}
	if err := json.NewEncoder(next).Encode(state); err != nil {
		return err
	}
	info, err = next.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxCatalogueStateBytes {
		return errors.New("state exceeds size limit")
	}
	if err := next.Sync(); err != nil {
		return err
	}
	if err := next.Close(); err != nil {
		return err
	}
	backup := ""
	removeBackup := true
	if existed {
		f, err := os.CreateTemp(dir, ".catalogue-previous-*")
		if err != nil {
			return err
		}
		backup = f.Name()
		if err := f.Close(); err != nil {
			os.Remove(backup)
			return err
		}
		if err := os.Remove(backup); err != nil {
			return err
		}
		if err := os.Link(filename, backup); err != nil {
			return err
		}
		defer func() {
			if removeBackup {
				os.Remove(backup)
			}
		}()
	}
	if err := os.Rename(next.Name(), filename); err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		if existed {
			if rollbackErr := os.Rename(backup, filename); rollbackErr != nil {
				removeBackup = false
				return errors.New("state sync and rollback failed")
			}
		} else if rollbackErr := os.Remove(filename); rollbackErr != nil {
			return errors.New("state sync and rollback failed")
		}
		directory.Sync()
		return err
	}
	if backup != "" {
		os.Remove(backup)
		directory.Sync()
	}
	return nil
}
