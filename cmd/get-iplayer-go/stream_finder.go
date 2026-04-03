package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PlaylistResponse represents the JSON response from /programmes/$pid/playlist.json
type PlaylistResponse struct {
	DefaultAvailableVersion struct {
		Pid       string `json:"pid"`
		SmpConfig struct {
			Title           string `json:"title"`
			Summary         string `json:"summary"`
			MasterBrandName string `json:"masterBrandName"`
			Items           []struct {
				Kind string `json:"kind"`
			} `json:"items"`
		} `json:"smpConfig"`
	} `json:"defaultAvailableVersion"`
}

// MediaSelectorResponse represents the XML response from mediaselector
type MediaSelectorResponse struct {
	XMLName xml.Name `xml:"mediaSelection"`
	Media   []struct {
		Kind       string `xml:"kind,attr"`
		Bitrate    string `xml:"bitrate,attr"`
		Width      string `xml:"width,attr"`
		Height     string `xml:"height,attr"`
		Connection []struct {
			Kind           string `xml:"kind,attr"`
			Href           string `xml:"href,attr"`
			TransferFormat string `xml:"transferFormat,attr"`
			Priority       string `xml:"priority,attr"`
		} `xml:"connection"`
	} `xml:"media"`
}

type StreamInfo struct {
	URL        string
	AudioURL   string // separate audio stream if needed
	Resolution string
	Bitrate    int
	Format     string // dash or hls
	Metadata   *ProgrammeMetadata
}

// ProgrammeMetadata contains BBC programme information for tagging
type ProgrammeMetadata struct {
	// Core identification
	PID          string
	EpisodeTitle string
	ShowName     string
	BrandName    string

	// Numbers
	EpisodeNum int
	SeriesNum  int

	// Descriptions
	ShortSynopsis string
	LongSynopsis  string

	// Broadcast info
	FirstBroadcast string // ISO8601
	Channel        string // e.g., "BBC One"

	// Categories
	Genre      string
	Categories string // comma-separated

	// Media
	ThumbnailPID string

	// Flags
	HasGuidance bool // For advisory rating

	// URLs for lyrics field
	PlayerURL string // iPlayer URL
	WebURL    string // Programme page URL
}

// ProgrammeResponse represents the JSON response from /programmes/$pid.json
type ProgrammeResponse struct {
	Programme struct {
		Type           string `json:"type"` // e.g. "brand", "series", "episode"
		Pid            string `json:"pid"`
		Title          string `json:"title"`
		Position       int    `json:"position"`
		ShortSynopsis  string `json:"short_synopsis"`
		MediumSynopsis string `json:"medium_synopsis"`
		LongSynopsis   string `json:"long_synopsis"`
		FirstBroadcast string `json:"first_broadcast_date"`
		Image          struct {
			Pid string `json:"pid"`
		} `json:"image"`
		DisplayTitle struct {
			Title    string `json:"title"`
			Subtitle string `json:"subtitle"`
		} `json:"display_title"`
		Ownership struct {
			Service struct {
				Title string `json:"title"`
			} `json:"service"`
		} `json:"ownership"`
		Categories []struct {
			Title string `json:"title"`
		} `json:"categories"`
		Parent struct {
			Programme struct {
				Title    string `json:"title"`
				Position int    `json:"position"`
				Parent   struct {
					Programme struct {
						Title string `json:"title"`
					} `json:"programme"`
				} `json:"parent"`
			} `json:"programme"`
		} `json:"parent"`
	} `json:"programme"`
}

// CheckPIDType returns the type of the PID (e.g. "episode", "brand")
func CheckPIDType(pid string) (string, error) {
	url := fmt.Sprintf("https://www.bbc.co.uk/programmes/%s.json", pid)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("PID check failed with status: %d", resp.StatusCode)
	}

	var prog ProgrammeResponse
	if err := json.NewDecoder(resp.Body).Decode(&prog); err != nil {
		return "", err
	}

	return prog.Programme.Type, nil
}

// FetchProgrammeMetadata fetches detailed programme information from BBC's API
func FetchProgrammeMetadata(pid string) (*ProgrammeMetadata, error) {
	url := fmt.Sprintf("https://www.bbc.co.uk/programmes/%s.json", pid)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch programme metadata: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("programme metadata request failed with status: %d", resp.StatusCode)
	}

	var prog ProgrammeResponse
	if err := json.NewDecoder(resp.Body).Decode(&prog); err != nil {
		return nil, fmt.Errorf("failed to decode programme JSON: %v", err)
	}

	metadata := &ProgrammeMetadata{
		PID:            prog.Programme.Pid,
		EpisodeTitle:   prog.Programme.Title,
		EpisodeNum:     prog.Programme.Position,
		ShortSynopsis:  prog.Programme.ShortSynopsis,
		LongSynopsis:   prog.Programme.LongSynopsis,
		FirstBroadcast: prog.Programme.FirstBroadcast,
		Channel:        prog.Programme.Ownership.Service.Title,
		ThumbnailPID:   prog.Programme.Image.Pid,
	}

	// Use medium synopsis if long is not available
	if metadata.LongSynopsis == "" && prog.Programme.MediumSynopsis != "" {
		metadata.LongSynopsis = prog.Programme.MediumSynopsis
	}

	// Use display title if available (e.g., "Example Show, Series 1, Episode 1")
	if prog.Programme.DisplayTitle.Title != "" {
		metadata.ShowName = prog.Programme.DisplayTitle.Title
	}

	// Try to extract series and episode numbers from display_title.subtitle
	// Format is typically "Series X, Episode Y" or "Series X: Episode Y"
	if prog.Programme.DisplayTitle.Subtitle != "" {
		subtitle := prog.Programme.DisplayTitle.Subtitle
		// Try to parse "Series X, Episode Y" or "Series X: Episode Y"
		re := regexp.MustCompile(`Series\s+(\d+).*Episode\s+(\d+)`)
		matches := re.FindStringSubmatch(subtitle)
		if len(matches) >= 3 {
			if seriesNum, err := strconv.Atoi(matches[1]); err == nil {
				metadata.SeriesNum = seriesNum
			}
			if episodeNum, err := strconv.Atoi(matches[2]); err == nil {
				metadata.EpisodeNum = episodeNum
			}
		}
	}

	// Get series info from parent (fallback if subtitle parsing didn't work)
	if prog.Programme.Parent.Programme.Title != "" {
		if metadata.SeriesNum == 0 {
			metadata.SeriesNum = prog.Programme.Parent.Programme.Position
		}
		// If we don't have a show name yet, use the series title
		if metadata.ShowName == "" {
			metadata.ShowName = prog.Programme.Parent.Programme.Title
		}
	}

	// Get brand name from parent.parent
	if prog.Programme.Parent.Programme.Parent.Programme.Title != "" {
		metadata.BrandName = prog.Programme.Parent.Programme.Parent.Programme.Title
		// Use brand as show name if we still don't have one
		if metadata.ShowName == "" {
			metadata.ShowName = metadata.BrandName
		}
	}

	// Extract categories
	var categories []string
	for _, cat := range prog.Programme.Categories {
		categories = append(categories, cat.Title)
	}
	if len(categories) > 0 {
		metadata.Genre = categories[0]
		metadata.Categories = strings.Join(categories, ", ")
	}

	// Build URLs for lyrics field
	metadata.PlayerURL = fmt.Sprintf("https://www.bbc.co.uk/iplayer/episode/%s", pid)
	metadata.WebURL = fmt.Sprintf("https://www.bbc.co.uk/programmes/%s", pid)

	return metadata, nil
}

// FindStreams fetches the playlist, gets the VPID, queries mediaselector,
// finds the best stream, and attempts to synthesize a 1080p stream.
func FindStreams(pid string) ([]StreamInfo, error) {
	// 0. Check PID Type
	pidType, err := CheckPIDType(pid)
	if err != nil {
		return nil, fmt.Errorf("failed to check PID type: %v", err)
	}

	if pidType != "episode" && pidType != "clip" {
		return nil, fmt.Errorf("PID %s is a '%s', not an episode. Please provide a specific Episode PID.", pid, pidType)
	}

	// 1. Get Playlist JSON
	playlistURL := fmt.Sprintf("https://www.bbc.co.uk/programmes/%s/playlist.json", pid)
	client := &http.Client{Timeout: 10 * time.Second}

	// Use a desktop User-Agent like Perl's to avoid being blocked or served different content
	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.81 Safari/537.36"

	reqPlaylist, err := http.NewRequest("GET", playlistURL, nil)
	if err != nil {
		return nil, err
	}
	reqPlaylist.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(reqPlaylist)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch playlist: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("playlist request failed with status: %d", resp.StatusCode)
	}

	var playlist PlaylistResponse
	if err := json.NewDecoder(resp.Body).Decode(&playlist); err != nil {
		return nil, fmt.Errorf("failed to decode playlist JSON: %v", err)
	}

	vpid := playlist.DefaultAvailableVersion.Pid
	if vpid == "" {
		return nil, fmt.Errorf("no default version PID found in playlist")
	}

	// 2. Query Media Selector (iptv-all)
	// Using iptv-all as it usually exposes the HD streams needed for synthesis
	mediaselectorURL := fmt.Sprintf("https://open.live.bbc.co.uk/mediaselector/6/select/version/2.0/mediaset/iptv-all/vpid/%s", vpid)

	req, err := http.NewRequest("GET", mediaselectorURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	msResp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch mediaselector: %v", err)
	}
	defer msResp.Body.Close()

	if msResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mediaselector request failed with status: %d", msResp.StatusCode)
	}

	// Read body
	msBody, err := io.ReadAll(msResp.Body)
	if err != nil {
		return nil, err
	}

	// Helper struct for JSON response
	type MediaSelectorResponseJSON struct {
		Media []struct {
			Kind       string      `json:"kind"`
			Bitrate    json.Number `json:"bitrate"` // Use Number to handle string or int
			Width      json.Number `json:"width"`
			Height     json.Number `json:"height"`
			Connection []struct {
				Kind           string      `json:"kind"`
				Href           string      `json:"href"`
				TransferFormat string      `json:"transferFormat"`
				Priority       json.Number `json:"priority"`
			} `json:"connection"`
		} `json:"media"`
	}

	var ms MediaSelectorResponse

	// Detect JSON vs XML
	bodyStr := strings.TrimSpace(string(msBody))
	if strings.HasPrefix(bodyStr, "{") {
		// Parsing as JSON
		var msJSON MediaSelectorResponseJSON
		if err := json.Unmarshal(msBody, &msJSON); err != nil {
			return nil, fmt.Errorf("failed to decode mediaselector JSON: %v", err)
		}

		// Map JSON to XML struct (which we use internally)
		for _, m := range msJSON.Media {
			media := struct {
				Kind       string `xml:"kind,attr"`
				Bitrate    string `xml:"bitrate,attr"`
				Width      string `xml:"width,attr"`
				Height     string `xml:"height,attr"`
				Connection []struct {
					Kind           string `xml:"kind,attr"`
					Href           string `xml:"href,attr"`
					TransferFormat string `xml:"transferFormat,attr"`
					Priority       string `xml:"priority,attr"`
				} `xml:"connection"`
			}{}

			media.Kind = m.Kind
			media.Bitrate = m.Bitrate.String()
			media.Width = m.Width.String()
			media.Height = m.Height.String()

			for _, c := range m.Connection {
				conn := struct {
					Kind           string `xml:"kind,attr"`
					Href           string `xml:"href,attr"`
					TransferFormat string `xml:"transferFormat,attr"`
					Priority       string `xml:"priority,attr"`
				}{}
				conn.Kind = c.Kind
				conn.Href = c.Href
				conn.TransferFormat = c.TransferFormat
				conn.Priority = c.Priority.String()

				media.Connection = append(media.Connection, conn)
			}
			ms.Media = append(ms.Media, media)
		}

	} else {
		// Parsing as XML
		// Sanitize XML: Replace & with &amp; in potential URL parameters to fix "invalid character entity" errors
		xmlStr := string(msBody)
		xmlStr = strings.ReplaceAll(xmlStr, "&Signature=", "&amp;Signature=")
		xmlStr = strings.ReplaceAll(xmlStr, "&Policy=", "&amp;Policy=")
		xmlStr = strings.ReplaceAll(xmlStr, "&Key-Pair-Id=", "&amp;Key-Pair-Id=")

		if err := xml.Unmarshal([]byte(xmlStr), &ms); err != nil {
			return nil, fmt.Errorf("failed to decode mediaselector XML: %v", err)
		}
	}

	var streams []StreamInfo
	var audioStreams []struct {
		URL     string
		Bitrate int
		Format  string
	}

	// 3. Find detailed streams - Two Pass Approach
	// Pass 1: Collect all audio streams
	for _, media := range ms.Media {
		if media.Kind == "audio" {
			for _, conn := range media.Connection {
				isDash := strings.Contains(conn.TransferFormat, "dash")
				isHls := strings.Contains(conn.TransferFormat, "hls")
				if !isDash && !isHls {
					continue
				}
				bitrate, _ := strconv.Atoi(media.Bitrate)
				format := "hls"
				if isDash {
					format = "dash"
				}
				audioStreams = append(audioStreams, struct {
					URL     string
					Bitrate int
					Format  string
				}{conn.Href, bitrate, format})
			}
		}
	}

	// Pass 2: Process video streams and match with best audio
	for _, media := range ms.Media {
		if media.Kind != "video" {
			continue
		}

		for _, conn := range media.Connection {
			// We prefer DASH (or HLS)
			isDash := strings.Contains(conn.TransferFormat, "dash")
			isHls := strings.Contains(conn.TransferFormat, "hls")

			if !isDash && !isHls {
				continue
			}

			bitrate, _ := strconv.Atoi(media.Bitrate)
			width, _ := strconv.Atoi(media.Width)
			height, _ := strconv.Atoi(media.Height)

			// Store the original stream
			format := "hls"
			if isDash {
				format = "dash"
			}

			// Find best audio for this format
			var bestAudioURL string
			var maxAudioBitrate int
			for _, audio := range audioStreams {
				if audio.Format == format && audio.Bitrate > maxAudioBitrate {
					maxAudioBitrate = audio.Bitrate
					bestAudioURL = audio.URL
				}
			}

			streams = append(streams, StreamInfo{
				URL:        conn.Href,
				AudioURL:   bestAudioURL, // Should now be populated correctly
				Resolution: fmt.Sprintf("%dx%d", width, height),
				Bitrate:    bitrate + maxAudioBitrate,
				Format:     format,
			})

		}
	}

	// 4. Synthesize/Fix 1080p DASH streams
	// BBC's mediaselector often reports 1080p but provides MPD manifests that only go up to 720p.
	// We need to modify the MPD to inject the actual 1080p representation.

	// TEMPORARY: Test with unmodified MPD by setting this to true
	testUnmodified720p := os.Getenv("TEST_720P") == "1"

	// Find the best DASH stream (either reported as 1080p or 720p)
	var bestDash *StreamInfo
	for i := range streams {
		s := &streams[i]
		if s.Format == "dash" {
			if bestDash == nil || s.Bitrate > bestDash.Bitrate {
				bestDash = s
			}
		}
	}

	if bestDash != nil {
		if testUnmodified720p {
			fmt.Printf("TEST MODE: Using unmodified 720p stream (no 1080p enhancement)\n")
			// Just use the original stream as-is for testing
		} else {
			fmt.Printf("Using enhanced 1080p stream\n")

			fhdStream, err := synthesizeFHDStream(*bestDash, userAgent)
			if err != nil {
				return nil, fmt.Errorf("failed to synthesize 1080p stream: %v", err)
			}

			// Validate the 1080p segments exist
			re := regexp.MustCompile(`(http[^/]+//[^/]+/[^/]+/[^/]+/[^/]+/[^/]+/(vf_[^/]+)\.ism)`)
			matches := re.FindStringSubmatch(bestDash.URL)
			if len(matches) >= 3 {
				baseURL := matches[1]
				filename := matches[2]
				testSegmentURL := baseURL + "/" + filename + "-video=12000000.dash"

				if !validateStreamURL(testSegmentURL, userAgent) {
					return nil, fmt.Errorf("1080p segments not accessible (validation failed)")
				}
			}
			// If we can't construct validation URL, proceed anyway (accept the risk)
			streams = append(streams, fhdStream)
		}
	} else {
		return nil, fmt.Errorf("no DASH streams found")
	}

	// Sort streams: Prefer DASH over HLS, then highest resolution, then highest bitrate
	sort.Slice(streams, func(i, j int) bool {
		// First priority: Prefer DASH over HLS
		if streams[i].Format != streams[j].Format {
			if streams[i].Format == "dash" {
				return true
			}
			if streams[j].Format == "dash" {
				return false
			}
		}

		// Second priority: Prefer higher resolution (1080p > 720p)
		if streams[i].Resolution != streams[j].Resolution {
			// Simple comparison: 1920x1080 > 1280x720
			return streams[i].Resolution > streams[j].Resolution
		}

		// Third priority: Highest bitrate
		return streams[i].Bitrate > streams[j].Bitrate
	})

	// Fetch programme metadata for tagging
	metadata, err := FetchProgrammeMetadata(pid)
	if err != nil {
		// Log warning but don't fail - tagging is optional
		fmt.Printf("Warning: Could not fetch programme metadata: %v\n", err)
		fmt.Println("(Proceeding with download, but file will not be tagged)")
	}

	// Attach metadata to all streams
	for i := range streams {
		streams[i].Metadata = metadata
	}

	return streams, nil
}

// modifyDASHManifestFor1080p takes an MPD content string, adds a 1080p representation,
// and returns the modified MPD content.
// This is needed because BBC's mediaselector doesn't advertise 1080p in the MPD even though
// the segments exist on the server.
func modifyDASHManifestFor1080p(mpdURL string, userAgent string, mpdContent string) (string, error) {
	mpdStr := mpdContent

	// Extract auth parameters from the MPD URL
	// Pattern: http://host/path/file.ism/manifest.mpd?auth=token
	// We need to append these auth params to each segment URL, not to BaseURL
	re := regexp.MustCompile(`(https?://[^/]+/.+\.ism)(/[^?]+)?(\?.+)?$`)
	baseURLMatches := re.FindStringSubmatch(mpdURL)
	authParams := ""
	if len(baseURLMatches) >= 4 {
		baseISM := baseURLMatches[1]
		authParams = baseURLMatches[3]
		// Set BaseURL to absolute path WITHOUT auth params (they'll go on each segment)
		absoluteBaseURL := baseISM + "/dash/"
		mpdStr = regexp.MustCompile(`<BaseURL>dash/</BaseURL>`).ReplaceAllString(mpdStr, fmt.Sprintf(`<BaseURL>%s</BaseURL>`, absoluteBaseURL))
	}

	// Extract the base filename pattern from existing SegmentTemplate
	// Pattern: initialization="BASENAME-$RepresentationID$.dash"
	initPattern := regexp.MustCompile(`initialization="([^"]+)-\$RepresentationID\$\.dash"`)
	matches := initPattern.FindStringSubmatch(mpdStr)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not find SegmentTemplate pattern in MPD")
	}
	baseName := matches[1]

	// Insert the 1080p representation after the 720p (5070000) representation
	// IMPORTANT: Include auth params in segment URLs
	fhdRepresentation := fmt.Sprintf(`      <Representation
        id="video=12000000"
        bandwidth="12000000"
        width="1920"
        height="1080"
        frameRate="50"
        sar="1:1"
        scanType="progressive">
        <SegmentTemplate
          timescale="50000"
          duration="192000"
          initialization="%s-$RepresentationID$.dash%s"
          media="%s-$RepresentationID$-$Number$.m4s%s">
        </SegmentTemplate>
      </Representation>`, baseName, authParams, baseName, authParams)

	// Find where to insert (after the last video Representation, before </AdaptationSet> for video)
	// Simple approach: find the 5070000 representation and insert after it
	insertPattern := `id="video=5070000"`
	insertIndex := strings.Index(mpdStr, insertPattern)
	if insertIndex == -1 {
		return "", fmt.Errorf("could not find 720p representation in MPD to inject 1080p")
	}

	// Find the end of this Representation element
	endRepIndex := strings.Index(mpdStr[insertIndex:], "</Representation>")
	if endRepIndex == -1 {
		return "", fmt.Errorf("malformed MPD: could not find end of 720p Representation")
	}
	insertPos := insertIndex + endRepIndex + len("</Representation>")

	// Insert the 1080p representation FIRST (before any replacements that could affect position)
	modifiedMPD := mpdStr[:insertPos] + "\n" + fhdRepresentation + mpdStr[insertPos:]

	// Now update ALL existing SegmentTemplate entries to include auth params
	// Pattern: initialization="filename.dash" -> initialization="filename.dash?auth"
	// Pattern: media="filename-$Number$.m4s" -> media="filename-$Number$.m4s?auth"
	if authParams != "" {
		// Add auth to all initialization attributes (except the one we just added)
		modifiedMPD = regexp.MustCompile(`initialization="([^"]+\.dash)"`).ReplaceAllString(
			modifiedMPD, fmt.Sprintf(`initialization="$1%s"`, authParams))

		// Add auth to all media attributes (except the one we just added)
		modifiedMPD = regexp.MustCompile(`media="([^"]+\.m4s)"`).ReplaceAllString(
			modifiedMPD, fmt.Sprintf(`media="$1%s"`, authParams))
	}

	// Now update maxBandwidth and maxWidth/maxHeight in AdaptationSet
	modifiedMPD = strings.Replace(modifiedMPD, `maxBandwidth="5070000"`, `maxBandwidth="12000000"`, 1)
	modifiedMPD = strings.Replace(modifiedMPD, `maxWidth="1280"`, `maxWidth="1920"`, 1)
	modifiedMPD = strings.Replace(modifiedMPD, `maxHeight="720"`, `maxHeight="1080"`, 1)

	return modifiedMPD, nil
}

// synthesizeFHDStream attempts to create a 1080p version from an HD (720p) DASH stream
// by creating TWO separate MPD files:
// 1. Video MPD: Modified with 1080p representation for video download
// 2. Audio MPD: Unmodified original for complete audio download
// Returns a new StreamInfo with video URL pointing to modified MPD and audio URL to original MPD.
func synthesizeFHDStream(hdStream StreamInfo, userAgent string) (StreamInfo, error) {
	// Only works on DASH streams
	if hdStream.Format != "dash" {
		return StreamInfo{}, fmt.Errorf("can only synthesize from DASH streams")
	}

	// Clone the stream
	fhdStream := hdStream

	// Fetch the original MPD (we'll use this for both video and audio)
	originalMPD, err := fetchMPD(hdStream.URL, userAgent)
	if err != nil {
		return StreamInfo{}, fmt.Errorf("failed to fetch MPD: %v", err)
	}

	// Create modified MPD for 1080p VIDEO
	modifiedMPD, err := modifyDASHManifestFor1080p(hdStream.URL, userAgent, originalMPD)
	if err != nil {
		return StreamInfo{}, fmt.Errorf("failed to modify MPD: %v", err)
	}

	// Save VIDEO MPD (1080p modified) to a temporary file
	videoMPDFile, err := os.CreateTemp("", "bbc_video_fhd_*.mpd")
	if err != nil {
		return StreamInfo{}, fmt.Errorf("failed to create video MPD file: %v", err)
	}

	if _, err := videoMPDFile.WriteString(modifiedMPD); err != nil {
		videoMPDFile.Close()
		return StreamInfo{}, fmt.Errorf("failed to write video MPD: %v", err)
	}
	videoMPDFile.Close() // Close but don't delete - ffmpeg needs to read it

	// For AUDIO: Use the SAME modified MPD file (it contains audio streams too!)
	// The Perl script clones the HD stream and modifies it, keeping the same audio.
	// Our modified MPD has the original audio stream with correct auth tokens.
	fhdStream.AudioURL = videoMPDFile.Name() // Same MPD for audio

	// Update video URL to point to the modified MPD file
	fhdStream.URL = videoMPDFile.Name() // 1080p video MPD (local file)

	// Update metadata
	fhdStream.Resolution = "1920x1080"
	// Video bitrate is ~8490 kbps for 1080p, keep the audio bitrate from original
	audioBitrate := hdStream.Bitrate - 5070
	if audioBitrate < 0 {
		audioBitrate = 320 // fallback to reasonable audio bitrate
	}
	fhdStream.Bitrate = 8490 + audioBitrate

	return fhdStream, nil
}

// fetchMPD downloads and returns the MPD content as a string
func fetchMPD(mpdURL string, userAgent string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", mpdURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MPD request failed with status: %d", resp.StatusCode)
	}

	mpdContent, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(mpdContent), nil
}

// fixMPDBaseURL fixes relative BaseURL and adds auth tokens to segment templates
func fixMPDBaseURL(mpdURL string, mpdContent string) (string, error) {
	mpdStr := mpdContent

	// Extract auth parameters from the MPD URL
	re := regexp.MustCompile(`(https?://[^/]+/.+\.ism)(/[^?]+)?(\?.+)?$`)
	baseURLMatches := re.FindStringSubmatch(mpdURL)
	authParams := ""
	if len(baseURLMatches) >= 4 {
		baseISM := baseURLMatches[1]
		authParams = baseURLMatches[3]
		// Set BaseURL to absolute path WITHOUT auth params
		absoluteBaseURL := baseISM + "/dash/"
		mpdStr = regexp.MustCompile(`<BaseURL>dash/</BaseURL>`).ReplaceAllString(mpdStr, fmt.Sprintf(`<BaseURL>%s</BaseURL>`, absoluteBaseURL))
	}

	// Add auth params to all segment templates
	if authParams != "" {
		// Add auth to all initialization attributes
		mpdStr = regexp.MustCompile(`initialization="([^"]+\.dash)"`).ReplaceAllString(
			mpdStr, fmt.Sprintf(`initialization="$1%s"`, authParams))

		// Add auth to all media attributes
		mpdStr = regexp.MustCompile(`media="([^"]+\.m4s)"`).ReplaceAllString(
			mpdStr, fmt.Sprintf(`media="$1%s"`, authParams))
	}

	return mpdStr, nil
}

// validateStreamURL checks if a synthesized URL is accessible without downloading the full content.
// It first tries a HEAD request, and falls back to a GET request with minimal read if HEAD is not supported.
// This validation ensures the 1080p stream actually exists before attempting download.
func validateStreamURL(url string, userAgent string) bool {
	client := &http.Client{Timeout: 10 * time.Second}

	// Use HEAD request first (fastest)
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// If HEAD not supported, try GET with minimal read
	if resp.StatusCode == http.StatusMethodNotAllowed {
		req, err = http.NewRequest("GET", url, nil)
		if err != nil {
			return false
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err = client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		// Read first 1KB to verify it's not an HTML error page
		buf := make([]byte, 1024)
		n, _ := resp.Body.Read(buf)
		content := string(buf[:n])

		// Check it's not an error page
		if strings.Contains(strings.ToLower(content), "<html") {
			return false
		}
	}

	// 2xx or 3xx status codes are good
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}
