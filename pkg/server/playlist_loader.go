package server

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jamesnetherton/m3u"
)

const groupTitleTagName = "group-title"

var playlistTagsRegexp = regexp.MustCompile(`([a-zA-Z0-9-]+?)="([^"]*)"`)

func loadPlaylistSources(sources []string) (m3u.Playlist, error) {
	merged := m3u.Playlist{
		Tracks: make([]m3u.Track, 0),
	}

	for _, source := range sources {
		if strings.TrimSpace(source) == "" {
			continue
		}

		playlist, err := loadPlaylistSource(source)
		if err != nil {
			return m3u.Playlist{}, err
		}

		merged.Tracks = append(merged.Tracks, playlist.Tracks...)
	}

	return merged, nil
}

func loadPlaylistSource(source string) (m3u.Playlist, error) {
	reader, err := openPlaylistSource(source)
	if err != nil {
		return m3u.Playlist{}, err
	}
	defer reader.Close()

	playlist, err := parsePlaylist(reader)
	if err != nil {
		return m3u.Playlist{}, fmt.Errorf("unable to parse playlist source %q: %w", source, err)
	}

	if err := normalizePlaylistTrackURIs(source, &playlist); err != nil {
		return m3u.Playlist{}, err
	}

	return playlist, nil
}

func openPlaylistSource(source string) (io.ReadCloser, error) {
	if isRemotePlaylistSource(source) {
		resp, err := newUpstreamHTTPClient().Get(source)
		if err != nil {
			return nil, fmt.Errorf("unable to open playlist URL %q: %w", source, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("unable to open playlist URL %q: unexpected status %s", source, resp.Status)
		}

		return resp.Body, nil
	}

	file, err := os.Open(source)
	if err != nil {
		return nil, fmt.Errorf("unable to open playlist file %q: %w", source, err)
	}

	return file, nil
}

func parsePlaylist(reader io.Reader) (m3u.Playlist, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	onFirstLine := true
	playlist := m3u.Playlist{}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if onFirstLine {
			line = strings.TrimSpace(strings.TrimPrefix(line, "\uFEFF"))
		}

		if onFirstLine && !strings.HasPrefix(strings.TrimSpace(line), "#EXTM3U") {
			return m3u.Playlist{}, fmt.Errorf("invalid m3u file format. Expected #EXTM3U file header")
		}

		onFirstLine = false

		switch {
		case strings.HasPrefix(line, "#EXTINF"):
			track, err := parseTrackMetadata(line)
			if err != nil {
				return m3u.Playlist{}, err
			}
			playlist.Tracks = append(playlist.Tracks, track)
		case strings.HasPrefix(line, "#") || line == "":
			continue
		case len(playlist.Tracks) == 0:
			return m3u.Playlist{}, fmt.Errorf("URI provided for playlist with no tracks")
		default:
			playlist.Tracks[len(playlist.Tracks)-1].URI = strings.TrimSpace(line)
		}
	}

	if err := scanner.Err(); err != nil {
		return m3u.Playlist{}, err
	}

	for i, track := range playlist.Tracks {
		if strings.TrimSpace(track.URI) == "" {
			return m3u.Playlist{}, fmt.Errorf("missing uri for track %d (%q)", i, track.Name)
		}
	}

	return playlist, nil
}

func parseTrackMetadata(line string) (m3u.Track, error) {
	trimmedLine := strings.TrimPrefix(line, "#EXTINF:")
	trackInfo := strings.Split(trimmedLine, ",")
	if len(trackInfo) < 2 {
		return m3u.Track{}, fmt.Errorf("invalid m3u file format. Expected EXTINF metadata to contain track length and name data")
	}

	lengthMetadata := strings.TrimSpace(trackInfo[0])
	lengthFields := strings.Fields(lengthMetadata)
	if len(lengthFields) == 0 {
		return m3u.Track{}, fmt.Errorf("invalid m3u file format. Expected EXTINF length")
	}

	length, err := strconv.Atoi(lengthFields[0])
	if err != nil {
		return m3u.Track{}, fmt.Errorf("unable to parse length")
	}

	track := m3u.Track{
		Name:   strings.TrimSpace(strings.Join(trackInfo[1:], ",")),
		Length: length,
		Tags:   make([]m3u.Tag, 0),
	}

	for _, tagString := range playlistTagsRegexp.FindAllString(trimmedLine, -1) {
		tagInfo := strings.SplitN(tagString, "=", 2)
		if len(tagInfo) != 2 {
			continue
		}

		track.Tags = append(track.Tags, m3u.Tag{
			Name:  tagInfo[0],
			Value: strings.Trim(tagInfo[1], `"`),
		})
	}

	return track, nil
}

func normalizePlaylistTrackURIs(source string, playlist *m3u.Playlist) error {
	if !isRemotePlaylistSource(source) {
		return nil
	}

	baseURL, err := url.Parse(source)
	if err != nil {
		return fmt.Errorf("unable to parse playlist source url %q: %w", source, err)
	}

	for i := range playlist.Tracks {
		resolvedURI, err := resolveTrackURI(baseURL, playlist.Tracks[i].URI)
		if err != nil {
			return fmt.Errorf("unable to resolve uri for track %q from %q: %w", playlist.Tracks[i].Name, source, err)
		}

		playlist.Tracks[i].URI = resolvedURI
	}

	return nil
}

func resolveTrackURI(baseURL *url.URL, rawURI string) (string, error) {
	trimmedURI := strings.TrimSpace(rawURI)
	trackURL, err := url.Parse(trimmedURI)
	if err != nil {
		return "", err
	}

	if trackURL.IsAbs() {
		return trackURL.String(), nil
	}

	return baseURL.ResolveReference(trackURL).String(), nil
}

func filterPlaylistByGroups(playlist m3u.Playlist, includeGroups []string) (m3u.Playlist, error) {
	requestedGroups := normalizeGroups(includeGroups)
	if len(requestedGroups) == 0 {
		return playlist, nil
	}

	filtered := m3u.Playlist{
		Tracks: make([]m3u.Track, 0, len(playlist.Tracks)),
	}

	for _, track := range playlist.Tracks {
		if groupMatchesAnyPattern(trackGroup(track), requestedGroups) {
			filtered.Tracks = append(filtered.Tracks, track)
		}
	}

	if len(playlist.Tracks) > 0 && len(filtered.Tracks) == 0 {
		return m3u.Playlist{}, fmt.Errorf(
			"no tracks matched the requested groups %q (available groups: %s)",
			strings.Join(requestedGroups, ", "),
			strings.Join(playlistGroups(playlist), ", "),
		)
	}

	return filtered, nil
}

func playlistGroups(playlist m3u.Playlist) []string {
	groups := make(map[string]struct{})
	for _, track := range playlist.Tracks {
		groupName := trackGroup(track)
		if groupName == "" {
			continue
		}

		groups[groupName] = struct{}{}
	}

	return sortUniqueKeys(groups)
}

func trackGroup(track m3u.Track) string {
	for _, tag := range track.Tags {
		if tag.Name == groupTitleTagName {
			return strings.TrimSpace(tag.Value)
		}
	}

	return ""
}

func normalizeGroups(groups []string) []string {
	normalized := make([]string, 0, len(groups))
	for _, group := range groups {
		trimmedGroup := strings.TrimSpace(group)
		if trimmedGroup == "" {
			continue
		}

		normalized = append(normalized, trimmedGroup)
	}

	return normalized
}

func groupMatchesAnyPattern(group string, patterns []string) bool {
	for _, pattern := range patterns {
		if groupMatchesPattern(group, pattern) {
			return true
		}
	}

	return false
}

func groupMatchesPattern(group, pattern string) bool {
	if !strings.ContainsAny(pattern, "*?") {
		return group == pattern
	}

	return wildcardMatch(group, pattern)
}

func wildcardMatch(value, pattern string) bool {
	valueRunes := []rune(value)
	patternRunes := []rune(pattern)

	valueIndex := 0
	patternIndex := 0
	starIndex := -1
	matchIndex := 0

	for valueIndex < len(valueRunes) {
		switch {
		case patternIndex < len(patternRunes) && (patternRunes[patternIndex] == valueRunes[valueIndex] || patternRunes[patternIndex] == '?'):
			valueIndex++
			patternIndex++
		case patternIndex < len(patternRunes) && patternRunes[patternIndex] == '*':
			starIndex = patternIndex
			matchIndex = valueIndex
			patternIndex++
		case starIndex != -1:
			patternIndex = starIndex + 1
			matchIndex++
			valueIndex = matchIndex
		default:
			return false
		}
	}

	for patternIndex < len(patternRunes) && patternRunes[patternIndex] == '*' {
		patternIndex++
	}

	return patternIndex == len(patternRunes)
}

func sortUniqueKeys(values map[string]struct{}) []string {
	sortedValues := make([]string, 0, len(values))
	for value := range values {
		sortedValues = append(sortedValues, value)
	}

	sort.Strings(sortedValues)

	return sortedValues
}

func isRemotePlaylistSource(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")
}
