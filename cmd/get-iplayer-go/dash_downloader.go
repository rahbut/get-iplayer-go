package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MPD represents a DASH Media Presentation Description
type MPD struct {
	XMLName                   xml.Name `xml:"MPD"`
	MediaPresentationDuration string   `xml:"mediaPresentationDuration,attr"`
	Periods                   []Period `xml:"Period"`
}

type Period struct {
	BaseURL        string          `xml:"BaseURL"`
	AdaptationSets []AdaptationSet `xml:"AdaptationSet"`
}

type AdaptationSet struct {
	ContentType     string           `xml:"contentType,attr"`
	MimeType        string           `xml:"mimeType,attr"`
	SegmentTemplate SegmentTemplate  `xml:"SegmentTemplate"`
	Representations []Representation `xml:"Representation"`
}

type SegmentTemplate struct {
	Timescale      string `xml:"timescale,attr"`
	Duration       string `xml:"duration,attr"`
	Initialization string `xml:"initialization,attr"`
	Media          string `xml:"media,attr"`
}

type Representation struct {
	ID              string          `xml:"id,attr"`
	Bandwidth       string          `xml:"bandwidth,attr"`
	Width           string          `xml:"width,attr"`
	Height          string          `xml:"height,attr"`
	SegmentTemplate SegmentTemplate `xml:"SegmentTemplate"`
}

// DASHSegment represents a single segment to download
type DASHSegment struct {
	URL           string
	SegmentNumber int
	IsInit        bool
	Duration      float64
}

// DownloadConfig contains settings for segment downloads
type DownloadConfig struct {
	ConcurrentSegments int            // Number of parallel downloads (1 = sequential, 4 = default, 10 = max)
	MaxRetries         int            // Per-segment retry limit
	UserAgent          string         // HTTP User-Agent header
	ExtraWorkersChan   <-chan int     // Optional channel to receive signal to scale up workers dynamically
}

// segmentTask represents a download task for a worker
type segmentTask struct {
	segment DASHSegment
	index   int // Position in sequence for ordered writing
}

// segmentResult represents a completed download
type segmentResult struct {
	index int
	data  []byte
	err   error
}

// createHTTPClient creates an HTTP client with connection pooling (like Perl's LWP::ConnCache)
func createHTTPClient(userAgent string) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second, // NOTE: May need increase for very slow networks (e.g., 300s)
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Enable connection reuse to avoid repeated DNS lookups
		DisableKeepAlives: false,
	}

	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
}

// rateLimiter implements a global rate limiter with adaptive backoff
type rateLimiter struct {
	mu              sync.Mutex
	tokens          int
	maxTokens       int
	lastError       time.Time
	consecutiveErrs int
}

func newRateLimiter(maxConcurrent int) *rateLimiter {
	return &rateLimiter{
		tokens:    maxConcurrent,
		maxTokens: maxConcurrent,
	}
}

func (rl *rateLimiter) AddTokens(n int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.maxTokens += n
	rl.tokens += n
}

func (rl *rateLimiter) acquire() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Wait if no tokens available
	for rl.tokens <= 0 {
		rl.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		rl.mu.Lock()
	}

	rl.tokens--
}

func (rl *rateLimiter) release() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.tokens < rl.maxTokens {
		rl.tokens++
	}
}

func (rl *rateLimiter) reportError() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// If error within 1 second of last error, increment counter
	if now.Sub(rl.lastError) < time.Second {
		rl.consecutiveErrs++

		// If multiple errors in quick succession, apply global backoff
		if rl.consecutiveErrs > 3 {
			rl.mu.Unlock()
			time.Sleep(time.Duration(rl.consecutiveErrs) * time.Second)
			rl.mu.Lock()
		}
	} else {
		rl.consecutiveErrs = 1
	}

	rl.lastError = now
}

func (rl *rateLimiter) reportSuccess() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.consecutiveErrs = 0
}

// DASHMedia represents parsed media info from MPD
type DASHMedia struct {
	BaseURL          string
	RepresentationID string
	InitTemplate     string
	MediaTemplate    string
	Timescale        int
	Duration         int
	SegmentDuration  float64
	TotalDuration    float64
	Segments         []DASHSegment
}

// ParseMPD parses an MPD manifest from URL and extracts segment information
func ParseMPD(mpdURL string, userAgent string) (*DASHMedia, *DASHMedia, error) {
	// Download MPD
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", mpdURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch MPD: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("MPD request failed with status: %d", resp.StatusCode)
	}

	// Read content
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read MPD: %v", err)
	}

	// Extract auth params from MPD URL
	authParams := ""
	if idx := strings.Index(mpdURL, "?"); idx != -1 {
		authParams = mpdURL[idx:]
	}

	// Extract base URL from MPD URL (everything up to .ism/)
	// e.g., http://host/path/file.ism/manifest.mpd -> http://host/path/file.ism/
	baseURLPrefix := ""
	if idx := strings.Index(mpdURL, ".ism/"); idx != -1 {
		baseURLPrefix = mpdURL[:idx+5] // Include ".ism/"
	}

	return parseMPDContent(string(content), authParams, baseURLPrefix)
}

// ParseMPDContent parses MPD from content string
func ParseMPDContent(content string) (*DASHMedia, *DASHMedia, error) {
	// Extract auth params from content if present
	authParams := ""
	baseURLPrefix := ""

	if strings.Contains(content, "?__gda__=") {
		re := regexp.MustCompile(`\?__gda__=[^"]+`)
		if match := re.FindString(content); match != "" {
			authParams = match
		}
	}

	// Extract base URL from BaseURL element
	reBase := regexp.MustCompile(`<BaseURL>([^<]+)</BaseURL>`)
	if match := reBase.FindStringSubmatch(content); len(match) > 1 {
		if strings.HasPrefix(match[1], "http://") || strings.HasPrefix(match[1], "https://") {
			baseURLPrefix = match[1]
		}
	}

	return parseMPDContent(content, authParams, baseURLPrefix)
}

// parseMPDContent does the actual parsing work
func parseMPDContent(content string, authParams string, baseURLPrefix string) (*DASHMedia, *DASHMedia, error) {
	// Parse XML
	var mpd MPD
	if err := xml.Unmarshal([]byte(content), &mpd); err != nil {
		return nil, nil, fmt.Errorf("failed to parse MPD XML: %v", err)
	}

	var videoMedia, audioMedia *DASHMedia

	// Process each period
	for _, period := range mpd.Periods {
		baseURL := period.BaseURL

		// If baseURL is relative, prepend the baseURLPrefix
		if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
			if baseURLPrefix != "" {
				baseURL = baseURLPrefix + baseURL
			}
		}

		// Process adaptation sets
		for _, adaptSet := range period.AdaptationSets {
			isVideo := adaptSet.ContentType == "video" || strings.Contains(adaptSet.MimeType, "video")
			isAudio := adaptSet.ContentType == "audio" || strings.Contains(adaptSet.MimeType, "audio")

			// Get the best representation
			var bestRep *Representation
			var maxBandwidth int

			for i := range adaptSet.Representations {
				rep := &adaptSet.Representations[i]
				bandwidth, _ := strconv.Atoi(rep.Bandwidth)
				if bandwidth > maxBandwidth {
					maxBandwidth = bandwidth
					bestRep = rep
				}
			}

			if bestRep == nil {
				continue
			}

			// Use representation's SegmentTemplate if available, otherwise use adaptation set's
			segTemplate := bestRep.SegmentTemplate
			if segTemplate.Initialization == "" {
				segTemplate = adaptSet.SegmentTemplate
			}

			timescale, _ := strconv.Atoi(segTemplate.Timescale)
			duration, _ := strconv.Atoi(segTemplate.Duration)

			if timescale == 0 || duration == 0 {
				continue
			}

			segmentDuration := float64(duration) / float64(timescale)

			// Parse total duration
			totalDuration := parseDuration(mpd.MediaPresentationDuration)
			numSegments := int(totalDuration / segmentDuration)
			// Add one more segment if the calculated segments don't fully cover the duration
			// This matches the Perl implementation's logic
			if segmentDuration*float64(numSegments) < totalDuration {
				numSegments++
			}

			media := &DASHMedia{
				BaseURL:          baseURL,
				RepresentationID: bestRep.ID,
				InitTemplate:     segTemplate.Initialization,
				MediaTemplate:    segTemplate.Media,
				Timescale:        timescale,
				Duration:         duration,
				SegmentDuration:  segmentDuration,
				TotalDuration:    totalDuration,
			}

			// Generate segment URLs
			media.Segments = generateSegments(media, numSegments, authParams)

			if isVideo && videoMedia == nil {
				videoMedia = media
			} else if isAudio && audioMedia == nil {
				audioMedia = media
			}
		}
	}

	return videoMedia, audioMedia, nil
}

// generateSegments creates segment URLs from the template
func generateSegments(media *DASHMedia, numSegments int, authParams string) []DASHSegment {
	var segments []DASHSegment

	// Add initialization segment
	initURL := strings.ReplaceAll(media.InitTemplate, "$RepresentationID$", media.RepresentationID)
	segments = append(segments, DASHSegment{
		URL:           media.BaseURL + initURL + authParams,
		SegmentNumber: 0,
		IsInit:        true,
		Duration:      0,
	})

	// Add media segments
	for i := 1; i <= numSegments; i++ {
		mediaURL := strings.ReplaceAll(media.MediaTemplate, "$RepresentationID$", media.RepresentationID)
		mediaURL = strings.ReplaceAll(mediaURL, "$Number$", strconv.Itoa(i))

		segments = append(segments, DASHSegment{
			URL:           media.BaseURL + mediaURL + authParams,
			SegmentNumber: i,
			IsInit:        false,
			Duration:      media.SegmentDuration,
		})
	}

	return segments
}

// parseDuration parses ISO 8601 duration (e.g., "PT59M7.600S")
func parseDuration(duration string) float64 {
	re := regexp.MustCompile(`PT(?:(\d+)H)?(?:(\d+)M)?(?:([\d.]+)S)?`)
	matches := re.FindStringSubmatch(duration)
	if len(matches) == 0 {
		return 0
	}

	var totalSeconds float64
	if matches[1] != "" {
		hours, _ := strconv.ParseFloat(matches[1], 64)
		totalSeconds += hours * 3600
	}
	if matches[2] != "" {
		minutes, _ := strconv.ParseFloat(matches[2], 64)
		totalSeconds += minutes * 60
	}
	if matches[3] != "" {
		seconds, _ := strconv.ParseFloat(matches[3], 64)
		totalSeconds += seconds
	}

	return totalSeconds
}

// DownloadSegments downloads all segments to a file with progress tracking
// Uses concurrent downloads with configurable parallelism
func DownloadSegments(segments []DASHSegment, outputFile string, mediaType string, config DownloadConfig, onProgress func(float64, int, int)) error {
	return DownloadSegmentsWithContext(context.Background(), segments, outputFile, mediaType, config, onProgress)
}

// DownloadSegmentsWithContext downloads all segments with context support for cancellation
func DownloadSegmentsWithContext(ctx context.Context, segments []DASHSegment, outputFile string, mediaType string, config DownloadConfig, onProgress func(float64, int, int)) error {
	// Open output file
	f, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %v", err)
	}
	defer f.Close()

	var totalDuration float64
	for _, seg := range segments {
		totalDuration += seg.Duration
	}

	// Use sequential mode if concurrency is 1
	if config.ConcurrentSegments <= 1 {
		return downloadSegmentsSequentialWithContext(ctx, segments, f, totalDuration, config.UserAgent, onProgress)
	}

	// Concurrent mode
	return downloadSegmentsConcurrentWithContext(ctx, segments, f, totalDuration, config, onProgress)
}

// downloadSegmentsSequential is the original sequential implementation
func downloadSegmentsSequential(segments []DASHSegment, f *os.File, totalDuration float64, userAgent string, onProgress func(float64, int, int)) error {
	return downloadSegmentsSequentialWithContext(context.Background(), segments, f, totalDuration, userAgent, onProgress)
}

// downloadSegmentsSequentialWithContext downloads segments sequentially with context support
func downloadSegmentsSequentialWithContext(ctx context.Context, segments []DASHSegment, f *os.File, totalDuration float64, userAgent string, onProgress func(float64, int, int)) error {
	totalSegments := len(segments)
	var downloadedDuration float64

	// Create shared HTTP client with custom DNS resolver
	client := createHTTPClient(userAgent)

	// Download each segment
	for i, segment := range segments {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := downloadSegmentToFile(segment, f, client, 10); err != nil {
			return fmt.Errorf("failed to download segment %d: %v", segment.SegmentNumber, err)
		}

		downloadedDuration += segment.Duration

		// Report progress
		if onProgress != nil {
			percent := 0.0
			if totalDuration > 0 {
				percent = (downloadedDuration / totalDuration) * 100
			} else {
				percent = (float64(i+1) / float64(totalSegments)) * 100
			}
			onProgress(percent, i+1, totalSegments)
		}
	}

	return nil
}

// downloadSegmentsConcurrent implements concurrent segment downloads with ordered writing
func downloadSegmentsConcurrent(segments []DASHSegment, f *os.File, totalDuration float64, config DownloadConfig, onProgress func(float64, int, int)) error {
	return downloadSegmentsConcurrentWithContext(context.Background(), segments, f, totalDuration, config, onProgress)
}

// downloadSegmentsConcurrentWithContext implements concurrent segment downloads with context support
func downloadSegmentsConcurrentWithContext(ctx context.Context, segments []DASHSegment, f *os.File, totalDuration float64, config DownloadConfig, onProgress func(float64, int, int)) error {
	totalSegments := len(segments)
	numWorkers := config.ConcurrentSegments

	// Create channels (bump maximum buffer size to account for dynamic scaling)
	tasks := make(chan segmentTask, (numWorkers+10)*2)
	results := make(chan segmentResult, (numWorkers+10)*2)

	// Create child context for internal cancellation
	internalCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Create rate limiter
	limiter := newRateLimiter(numWorkers)

	// Create shared HTTP client with custom DNS resolver
	httpClient := createHTTPClient(config.UserAgent)

	// Start worker pool
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			downloadWorker(internalCtx, tasks, results, limiter, httpClient, config.MaxRetries)
		}()
	}

	// Start a background listener for dynamic scaling
	if config.ExtraWorkersChan != nil {
		go func() {
			select {
			case <-internalCtx.Done():
				return
			case extra := <-config.ExtraWorkersChan:
				limiter.AddTokens(extra)
				for i := 0; i < extra; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						downloadWorker(internalCtx, tasks, results, limiter, httpClient, config.MaxRetries)
					}()
				}
			}
		}()
	}

	// Start result writer goroutine
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- orderedWriter(results, f, totalSegments, totalDuration, onProgress)
	}()

	// Send tasks to workers
	go func() {
		for i, segment := range segments {
			select {
			case <-internalCtx.Done():
				return
			case tasks <- segmentTask{segment: segment, index: i}:
			}
		}
		close(tasks)
	}()

	// Wait for workers to finish
	wg.Wait()
	close(results)

	// Wait for writer to finish and return its error (if any)
	return <-writerDone
}

// downloadWorker is a worker goroutine that downloads segments
func downloadWorker(ctx context.Context, tasks <-chan segmentTask, results chan<- segmentResult, limiter *rateLimiter, client *http.Client, maxRetries int) {
	for task := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Acquire rate limit token
		limiter.acquire()

		// Download segment to memory
		data, err := downloadSegmentToMemory(task.segment, client, maxRetries, limiter)

		// Release token
		limiter.release()

		// Send result
		results <- segmentResult{
			index: task.index,
			data:  data,
			err:   err,
		}
	}
}

// orderedWriter writes segments to file in order, buffering out-of-order segments
func orderedWriter(results <-chan segmentResult, f *os.File, totalSegments int, totalDuration float64, onProgress func(float64, int, int)) error {
	nextIndex := 0
	buffer := make(map[int][]byte)
	segmentCount := 0
	downloadedCount := 0

	for result := range results {
		if result.err != nil {
			return fmt.Errorf("failed to download segment %d: %v", result.index, result.err)
		}

		downloadedCount++

		// Store in buffer
		buffer[result.index] = result.data

		// Write all consecutive segments we have
		for {
			data, ok := buffer[nextIndex]
			if !ok {
				break
			}

			// Write to file
			if _, err := f.Write(data); err != nil {
				return fmt.Errorf("failed to write segment %d: %v", nextIndex, err)
			}

			delete(buffer, nextIndex)
			segmentCount++
			nextIndex++
		}

		// Report progress based on total downloaded segments instead of written segments
		// This prevents the progress bar from stalling if an early segment takes longer to download
		if onProgress != nil {
			percent := (float64(downloadedCount) / float64(totalSegments)) * 100
			onProgress(percent, downloadedCount, totalSegments)
		}
	}

	return nil
}

// downloadSegmentToFile downloads a single segment directly to a file with retries
func downloadSegmentToFile(segment DASHSegment, output *os.File, client *http.Client, maxRetries int) error {
	var lastErr error

	for retry := 0; retry < maxRetries; retry++ {
		req, err := http.NewRequest("GET", segment.URL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			// Exponential backoff with max 5 seconds
			backoff := time.Duration(1<<uint(retry)) * time.Second
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			time.Sleep(backoff)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			backoff := time.Duration(1<<uint(retry)) * time.Second
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			time.Sleep(backoff)
			continue
		}

		// Download segment data
		_, err = io.Copy(output, resp.Body)
		resp.Body.Close()

		if err != nil {
			lastErr = err
			backoff := time.Duration(1<<uint(retry)) * time.Second
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			time.Sleep(backoff)
			continue
		}

		// Success!
		return nil
	}

	return fmt.Errorf("failed after %d retries: %v", maxRetries, lastErr)
}

// downloadSegmentToMemory downloads a single segment to memory with retries
func downloadSegmentToMemory(segment DASHSegment, client *http.Client, maxRetries int, limiter *rateLimiter) ([]byte, error) {
	var lastErr error

	for retry := 0; retry < maxRetries; retry++ {
		req, err := http.NewRequest("GET", segment.URL, nil)
		if err != nil {
			lastErr = err
			limiter.reportError()
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			limiter.reportError()
			// Exponential backoff with max 5 seconds
			backoff := time.Duration(1<<uint(retry)) * time.Second
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			time.Sleep(backoff)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			limiter.reportError()
			backoff := time.Duration(1<<uint(retry)) * time.Second
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			time.Sleep(backoff)
			continue
		}

		// Read segment data to memory
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			lastErr = err
			limiter.reportError()
			backoff := time.Duration(1<<uint(retry)) * time.Second
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			time.Sleep(backoff)
			continue
		}

		// Success!
		limiter.reportSuccess()
		return data, nil
	}

	return nil, fmt.Errorf("failed after %d retries: %v", maxRetries, lastErr)
}
