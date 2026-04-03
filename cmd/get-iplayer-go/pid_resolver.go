package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// REGEX_PID matches the standard 8+ character alphanumeric PID format used by BBC.
// Excludes vowels 'a', 'e', 'i', 'o', 'u' to avoid accidental words?
// Perl regex: qr/^[b-df-hj-np-tv-z0-9]{8,}$/;
var pidRegex = regexp.MustCompile(`^[b-df-hj-np-tv-z0-9]{8,}$`)

// ResolvePID extracts a PID from a raw string or URL.
func ResolvePID(input string) (string, error) {
	input = strings.TrimSpace(input)

	// If the input itself is a PID, return it.
	if pidRegex.MatchString(input) {
		return input, nil
	}

	// If it looks like a URL, parse it.
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		u, err := url.Parse(input)
		if err != nil {
			return "", err
		}

		// Split path into segments
		segments := strings.Split(u.Path, "/")

		// Find the *last* segment that matches the PID regex
		var foundPID string
		for _, segment := range segments {
			if pidRegex.MatchString(segment) {
				foundPID = segment
			}
		}

		if foundPID != "" {
			return foundPID, nil
		}
	}

	return "", errors.New("no valid PID found in input")
}

// IsSeriesURL checks if a URL is a valid BBC iPlayer episodes/series URL.
func IsSeriesURL(input string) bool {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		u, err := url.Parse(input)
		if err == nil {
			return strings.Contains(u.Path, "/iplayer/episodes/")
		}
	}
	return false
}

// SeriesEpisode represents a single episode in a series.
type SeriesEpisode struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Synopsis string `json:"synopsis"`
	ImageURL string `json:"image_url"`
}

// FetchSeriesEpisodes fetches the HTML of a series page, extracts the Redux state,
// and parses the episodes available.
func FetchSeriesEpisodes(seriesURL string) ([]SeriesEpisode, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.81 Safari/537.36"

	req, err := http.NewRequest("GET", seriesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch series page: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("series page request failed with status: %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}
	html := string(bodyBytes)

	// Extract window.__IPLAYER_REDUX_STATE__ = { ... };
	prefix := "window.__IPLAYER_REDUX_STATE__ = "
	idx := strings.Index(html, prefix)
	if idx == -1 {
		return nil, errors.New("could not find Redux state in page")
	}

	startIdx := idx + len(prefix)
	endIdx := strings.Index(html[startIdx:], ";</script>")
	if endIdx == -1 {
		return nil, errors.New("could not find end of Redux state")
	}

	jsonStr := html[startIdx : startIdx+endIdx]

	var state map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &state); err != nil {
		return nil, fmt.Errorf("failed to parse Redux state JSON: %v", err)
	}

	entitiesNode, ok := state["entities"].(map[string]interface{})
	if !ok {
		return nil, errors.New("no entities data found in Redux state")
	}

	resultsNode, ok := entitiesNode["results"].([]interface{})
	if !ok {
		return nil, errors.New("no results array found in entities node")
	}

	var episodes []SeriesEpisode
	for _, resultItem := range resultsNode {
		resultMap, ok := resultItem.(map[string]interface{})
		if !ok {
			continue
		}

		epMap, ok := resultMap["episode"].(map[string]interface{})
		if !ok {
			continue
		}

		pid, ok := epMap["id"].(string)
		if !ok || pid == "" {
			continue
		}

		ep := SeriesEpisode{ID: pid}

		if titleNode, ok := epMap["title"].(map[string]interface{}); ok {
			if defTitle, ok := titleNode["default"].(string); ok {
				ep.Title = defTitle
			}
		}

		if subtitleNode, ok := epMap["subtitle"].(map[string]interface{}); ok {
			if defSubtitle, ok := subtitleNode["default"].(string); ok {
				ep.Subtitle = defSubtitle
			} else if sliceSubtitle, ok := subtitleNode["slice"].(string); ok {
				ep.Subtitle = sliceSubtitle
			}
		}

		if synopsisNode, ok := epMap["synopsis"].(map[string]interface{}); ok {
			if smallSynopsis, ok := synopsisNode["small"].(string); ok {
				ep.Synopsis = smallSynopsis
			}
		}

		if imageNode, ok := epMap["image"].(map[string]interface{}); ok {
			if defImage, ok := imageNode["default"].(string); ok {
				// Replace {recipe} with a standard size e.g. 640x360
				ep.ImageURL = strings.ReplaceAll(defImage, "{recipe}", "640x360")
			}
		}

		episodes = append(episodes, ep)
	}

	if len(episodes) == 0 {
		return nil, errors.New("no episodes extracted from series page")
	}

	return episodes, nil
}
