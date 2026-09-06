package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

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
	p, _, err := loadPlaylistSourceContext(context.Background(), source, nil, defaultUpstreamRequestTimeout)
	return p, err
}

func loadPlaylistSourceContext(ctx context.Context, source string, groups []string, timeout time.Duration) (m3u.Playlist, []string, error) {
	result, err := loadPlaylistSourceDetails(ctx, source, groups, timeout)
	if err == nil && len(result.Tracks) == 0 {
		err = noMatchingGroups(groups, result.Groups)
	}
	return m3u.Playlist{Tracks: result.Tracks}, result.Groups, err
}

func loadPlaylistSourceDetails(ctx context.Context, source string, groups []string, timeout time.Duration) (catalogueSource, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reader, err := openPlaylistSourceContext(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			return catalogueSource{}, ctx.Err()
		}
		return catalogueSource{}, err
	}
	defer reader.Close()
	var base *url.URL
	if isRemotePlaylistSource(source) {
		base, err = url.Parse(source)
		if err != nil {
			return catalogueSource{}, fmt.Errorf("invalid playlist source URL")
		}
	}
	accounts := make(map[string]struct{})
	total := 0
	p, discovered, err := parsePlaylistRecords(&playlistContextReader{ctx: ctx, reader: reader}, groups, base, func(track m3u.Track) {
		total++
		if account := relayAccountKey(track.URI); account != "" {
			accounts[account] = struct{}{}
		}
	})
	if ctx.Err() != nil {
		return catalogueSource{}, ctx.Err()
	}
	if err != nil {
		err = &playlistFailure{kind: "invalid_playlist", cause: err}
	}
	return catalogueSource{Tracks: p.Tracks, Groups: discovered, Accounts: sortUniqueKeys(accounts), TotalCount: total}, err
}

type playlistContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *playlistContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func openPlaylistSourceContext(ctx context.Context, source string) (io.ReadCloser, error) {
	if isRemotePlaylistSource(source) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, fmt.Errorf("invalid playlist source URL")
		}
		resp, err := playlistHTTPClient.Do(req)
		if err != nil {
			return nil, &playlistFailure{kind: safeErrorKind(err), cause: fmt.Errorf("playlist fetch failed")}
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, &playlistFailure{kind: "http_status", cause: fmt.Errorf("playlist fetch returned HTTP %d", resp.StatusCode)}
		}

		return resp.Body, nil
	}

	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("playlist source must be a regular file")
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, fmt.Errorf("unable to open playlist file")
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("playlist source must be a regular file")
	}

	return file, nil
}

func parsePlaylist(reader io.Reader) (m3u.Playlist, error) {
	p, _, err := parsePlaylistFiltered(reader, nil, nil)
	return p, err
}

func parsePlaylistFiltered(reader io.Reader, includeGroups []string, base *url.URL) (m3u.Playlist, []string, error) {
	p, groups, err := parsePlaylistRecords(reader, includeGroups, base, nil)
	if err == nil && len(p.Tracks) == 0 {
		return m3u.Playlist{}, nil, noMatchingGroups(includeGroups, groups)
	}
	return p, groups, err
}

func noMatchingGroups(patterns, groups []string) error {
	return fmt.Errorf("no tracks matched the requested groups %q (available groups: %s)", strings.Join(normalizeGroups(patterns), ", "), strings.Join(groups, ", "))
}

func parsePlaylistRecords(reader io.Reader, includeGroups []string, base *url.URL, inspect func(m3u.Track)) (m3u.Playlist, []string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	onFirstLine := true
	playlist := m3u.Playlist{}
	patterns := normalizeGroups(includeGroups)
	groups := make(map[string]struct{})
	var pending *m3u.Track
	records := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if onFirstLine {
			line = strings.TrimSpace(strings.TrimPrefix(line, "\uFEFF"))
		}

		if onFirstLine && line != "#EXTM3U" && !strings.HasPrefix(line, "#EXTM3U ") && !strings.HasPrefix(line, "#EXTM3U\t") {
			return m3u.Playlist{}, nil, fmt.Errorf("invalid m3u file format. Expected #EXTM3U file header")
		}

		onFirstLine = false

		switch {
		case strings.HasPrefix(line, "#EXTINF"):
			if pending != nil {
				return m3u.Playlist{}, nil, fmt.Errorf("missing uri for track")
			}
			track, err := parseTrackMetadata(line)
			if err != nil {
				return m3u.Playlist{}, nil, err
			}
			pending = &track
		case strings.HasPrefix(line, "#") || line == "":
			continue
		case pending == nil:
			return m3u.Playlist{}, nil, fmt.Errorf("URI provided for playlist with no pending track")
		default:
			uri, err := url.Parse(line)
			if err != nil {
				return m3u.Playlist{}, nil, fmt.Errorf("invalid track URI")
			}
			if base != nil {
				uri = base.ResolveReference(uri)
			}
			pending.URI = uri.String()
			if _, err := trackPathBase(pending.URI); err != nil {
				return m3u.Playlist{}, nil, fmt.Errorf("invalid track URI path")
			}
			if inspect != nil {
				inspect(*pending)
			}
			group := trackGroup(*pending)
			if group != "" {
				groups[group] = struct{}{}
			}
			if len(patterns) == 0 || groupMatchesAnyPattern(group, patterns) {
				playlist.Tracks = append(playlist.Tracks, *pending)
			}
			records++
			pending = nil
		}
	}

	if err := scanner.Err(); err != nil {
		return m3u.Playlist{}, nil, fmt.Errorf("playlist read failed (%s)", safeErrorKind(err))
	}
	if pending != nil {
		return m3u.Playlist{}, nil, fmt.Errorf("missing uri for track")
	}
	if onFirstLine || records == 0 {
		return m3u.Playlist{}, nil, fmt.Errorf("empty or truncated playlist")
	}
	discovered := sortUniqueKeys(groups)
	return playlist, discovered, nil
}

func parseTrackMetadata(line string) (m3u.Track, error) {
	trimmedLine := strings.TrimPrefix(line, "#EXTINF:")
	// Commas inside quoted attributes are not the metadata/name delimiter.
	quoted, comma := false, -1
	for i, r := range trimmedLine {
		if r == '"' {
			quoted = !quoted
		}
		if r == ',' && !quoted {
			comma = i
			break
		}
	}
	var trackInfo []string
	if comma >= 0 {
		trackInfo = []string{trimmedLine[:comma], trimmedLine[comma+1:]}
	}
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
