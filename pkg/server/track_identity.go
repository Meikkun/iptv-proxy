package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var stableTokenPattern = regexp.MustCompile(`^s[0-9a-f]{64}$`)
var providerIDPattern = regexp.MustCompile(`^[0-9]+$`)

func canonicalTrackURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid track URI")
	}
	u.Fragment, u.RawFragment = "", ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

func stableTrackIdentity(sourceID, raw string) (key, identity, account string, err error) {
	identity, err = canonicalTrackURI(raw)
	if err != nil {
		return "", "", "", err
	}
	u, _ := url.Parse(identity)
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	kind, id := "", ""
	account = relayAccountKey(identity)
	if account != "" {
		switch {
		case len(parts) == 4 && (parts[0] == "live" || parts[0] == "movie" || parts[0] == "series"):
			kind, id = parts[0], parts[3]
		case len(parts) == 3:
			kind, id = "live", parts[2]
		}
		id = strings.TrimSuffix(id, path.Ext(id))
	}
	tuple := []string{"uri", sourceID, identity}
	if kind != "" && providerIDPattern.MatchString(id) {
		tuple = []string{"xtream", sourceID, kind, id}
	}
	data, _ := json.Marshal(tuple)
	return fmt.Sprintf("s%x", sha256.Sum256(data)), identity, account, nil
}
