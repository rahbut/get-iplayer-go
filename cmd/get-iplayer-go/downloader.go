package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// progressTracker manages concurrent progress display for audio and video
type progressTracker struct {
	mu            sync.Mutex
	audioProgress     string
	videoProgress     string
	started           bool
	audioExtraWorkers <-chan int
	videoExtraWorkers <-chan int
}

func newProgressTracker() *progressTracker {
	return &progressTracker{}
}

func (pt *progressTracker) updateAudio(percent float64, current, total int) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	bar := createProgressBar(percent, 40)
	pt.audioProgress = fmt.Sprintf("  [Audio] %s %5.1f%% (%d/%d segments)", bar, percent, current, total)
	pt.display()
}

func (pt *progressTracker) updateVideo(percent float64, current, total int) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	bar := createProgressBar(percent, 40)
	pt.videoProgress = fmt.Sprintf("  [Video] %s %5.1f%% (%d/%d segments)", bar, percent, current, total)
	pt.display()
}

// createProgressBar creates a visual progress bar
func createProgressBar(percent float64, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	filled := int((percent / 100.0) * float64(width))
	if filled > width {
		filled = width
	}

	bar := "["
	for i := 0; i < width; i++ {
		if i < filled {
			bar += "="
		} else if i == filled && filled < width {
			bar += ">"
		} else {
			bar += " "
		}
	}
	bar += "]"

	return bar
}

func (pt *progressTracker) display() {
	if !pt.started {
		pt.started = true
		// Reserve two lines for progress
		fmt.Println()
		fmt.Println()
	}

	// Move cursor up 2 lines and redraw both progress bars
	fmt.Print("\033[2A") // Move up 2 lines
	fmt.Print("\033[K")  // Clear line
	if pt.audioProgress != "" {
		fmt.Println(pt.audioProgress)
	} else {
		fmt.Println("  [Audio] Starting...")
	}
	fmt.Print("\033[K") // Clear line
	if pt.videoProgress != "" {
		fmt.Println(pt.videoProgress)
	} else {
		fmt.Println("  [Video] Starting...")
	}
}

func (pt *progressTracker) finish() {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	// Just print a newline to move past the progress lines
	// The completion messages will be printed after this
}

// downloadVideo downloads only the video stream to a .ts file
func downloadVideo(videoURL, tsFilename string, concurrentSegments int) error {
	// We only support MPD streams now - always use segment downloader
	if !strings.HasSuffix(videoURL, ".mpd") {
		return fmt.Errorf("unsupported video URL format (expected .mpd): %s", videoURL)
	}

	fmt.Printf("  Using segment downloader for DASH video (concurrency: %d)\n", concurrentSegments)
	return downloadVideoSegments(videoURL, tsFilename, concurrentSegments)
}

// downloadVideoWithProgress downloads video with concurrent progress tracking
func downloadVideoWithProgress(videoURL, tsFilename string, concurrentSegments int, progress *progressTracker) error {
	if !strings.HasSuffix(videoURL, ".mpd") {
		return fmt.Errorf("unsupported video URL format (expected .mpd): %s", videoURL)
	}
	return downloadVideoSegmentsWithProgress(videoURL, tsFilename, concurrentSegments, progress)
}

// downloadVideoSegments uses our custom segment downloader
func downloadVideoSegments(mpdPath, outputFile string, concurrentSegments int) error {
	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"

	var videoMedia *DASHMedia
	var err error

	// Check if it's a local file or HTTP URL
	if strings.HasPrefix(mpdPath, "http://") || strings.HasPrefix(mpdPath, "https://") {
		// HTTP URL - fetch and parse
		videoMedia, _, err = ParseMPD(mpdPath, userAgent)
	} else {
		// Local file - read and parse
		mpdContent, readErr := os.ReadFile(mpdPath)
		if readErr != nil {
			return fmt.Errorf("failed to read MPD: %v", readErr)
		}
		videoMedia, _, err = ParseMPDContent(string(mpdContent))
	}

	if err != nil {
		return fmt.Errorf("failed to parse MPD: %v", err)
	}

	if videoMedia == nil {
		return fmt.Errorf("no video media found in MPD")
	}

	// Create download config
	config := DownloadConfig{
		ConcurrentSegments: concurrentSegments,
		MaxRetries:         10,
		UserAgent:          userAgent,
	}

	// Download segments with progress
	return DownloadSegments(videoMedia.Segments, outputFile, "video", config, func(percent float64, current, total int) {
		fmt.Printf("\r  [Video] %.1f%% (%d/%d segments)   ", percent, current, total)
	})
}

// downloadVideoSegmentsWithProgress is like downloadVideoSegments but uses progress tracker
func downloadVideoSegmentsWithProgress(mpdPath, outputFile string, concurrentSegments int, progress *progressTracker) error {
	return downloadVideoSegmentsWithProgressAndContext(context.Background(), mpdPath, outputFile, concurrentSegments, progress)
}

// parseMPDForVideo parses an MPD file (local or HTTP) and returns video segments
func parseMPDForVideo(mpdPath string) ([]DASHSegment, error) {
	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"

	var videoMedia *DASHMedia
	var err error

	if strings.HasPrefix(mpdPath, "http://") || strings.HasPrefix(mpdPath, "https://") {
		videoMedia, _, err = ParseMPD(mpdPath, userAgent)
	} else {
		mpdContent, readErr := os.ReadFile(mpdPath)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read MPD: %v", readErr)
		}
		videoMedia, _, err = ParseMPDContent(string(mpdContent))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to parse MPD: %v", err)
	}

	if videoMedia == nil {
		return nil, fmt.Errorf("no video media found in MPD")
	}

	return videoMedia.Segments, nil
}

// downloadVideoSegmentsWithProgressAndContext downloads video with context support for cancellation
func downloadVideoSegmentsWithProgressAndContext(ctx context.Context, mpdPath, outputFile string, concurrentSegments int, progress *progressTracker) error {
	segments, err := parseMPDForVideo(mpdPath)
	if err != nil {
		return err
	}

	config := DownloadConfig{
		ConcurrentSegments: concurrentSegments,
		MaxRetries:         10,
		UserAgent:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		ExtraWorkersChan:   progress.videoExtraWorkers,
	}

	return DownloadSegmentsWithContext(ctx, segments, outputFile, "video", config, func(percent float64, current, total int) {
		progress.updateVideo(percent, current, total)
	})
}

// downloadAudio downloads only the audio stream to a .m4a file
func downloadAudio(audioURL, videoURL, audioFilename string, concurrentSegments int) error {
	// If no separate audio URL, we try to extract audio from the video URL
	targetAudioURL := audioURL
	if targetAudioURL == "" {
		targetAudioURL = videoURL
	}

	// We only support MPD streams now - always use segment downloader
	if !strings.HasSuffix(targetAudioURL, ".mpd") {
		return fmt.Errorf("unsupported audio URL format (expected .mpd): %s", targetAudioURL)
	}

	fmt.Printf("  Using segment downloader for DASH audio (concurrency: %d)\n", concurrentSegments)
	return downloadAudioSegments(targetAudioURL, audioFilename, concurrentSegments)
}

// downloadAudioWithProgress downloads audio with concurrent progress tracking
func downloadAudioWithProgress(audioURL, videoURL, audioFilename string, concurrentSegments int, progress *progressTracker) error {
	targetAudioURL := audioURL
	if targetAudioURL == "" {
		targetAudioURL = videoURL
	}

	if !strings.HasSuffix(targetAudioURL, ".mpd") {
		return fmt.Errorf("unsupported audio URL format (expected .mpd): %s", targetAudioURL)
	}

	return downloadAudioSegmentsWithProgress(targetAudioURL, audioFilename, concurrentSegments, progress)
}

// downloadAudioSegments uses our custom segment downloader
func downloadAudioSegments(mpdPath, outputFile string, concurrentSegments int) error {
	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"

	var audioMedia *DASHMedia
	var err error

	// Check if it's a local file or HTTP URL
	if strings.HasPrefix(mpdPath, "http://") || strings.HasPrefix(mpdPath, "https://") {
		// HTTP URL - fetch and parse
		_, audioMedia, err = ParseMPD(mpdPath, userAgent)
	} else {
		// Local file - read and parse
		mpdContent, readErr := os.ReadFile(mpdPath)
		if readErr != nil {
			return fmt.Errorf("failed to read MPD: %v", readErr)
		}
		_, audioMedia, err = ParseMPDContent(string(mpdContent))
	}

	if err != nil {
		return fmt.Errorf("failed to parse MPD: %v", err)
	}

	if audioMedia == nil {
		return fmt.Errorf("no audio media found in MPD")
	}

	// Create download config
	config := DownloadConfig{
		ConcurrentSegments: concurrentSegments,
		MaxRetries:         10,
		UserAgent:          userAgent,
	}

	// Download segments with progress
	return DownloadSegments(audioMedia.Segments, outputFile, "audio", config, func(percent float64, current, total int) {
		fmt.Printf("\r  [Audio] %.1f%% (%d/%d segments)   ", percent, current, total)
	})
}

// downloadAudioSegmentsWithProgress is like downloadAudioSegments but uses progress tracker
func downloadAudioSegmentsWithProgress(mpdPath, outputFile string, concurrentSegments int, progress *progressTracker) error {
	return downloadAudioSegmentsWithProgressAndContext(context.Background(), mpdPath, outputFile, concurrentSegments, progress)
}

// parseMPDForAudio parses an MPD file (local or HTTP) and returns audio segments
func parseMPDForAudio(mpdPath string) ([]DASHSegment, error) {
	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"

	var audioMedia *DASHMedia
	var err error

	if strings.HasPrefix(mpdPath, "http://") || strings.HasPrefix(mpdPath, "https://") {
		_, audioMedia, err = ParseMPD(mpdPath, userAgent)
	} else {
		mpdContent, readErr := os.ReadFile(mpdPath)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read MPD: %v", readErr)
		}
		_, audioMedia, err = ParseMPDContent(string(mpdContent))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to parse MPD: %v", err)
	}

	if audioMedia == nil {
		return nil, fmt.Errorf("no audio media found in MPD")
	}

	return audioMedia.Segments, nil
}

// downloadAudioSegmentsWithProgressAndContext downloads audio with context support for cancellation
func downloadAudioSegmentsWithProgressAndContext(ctx context.Context, mpdPath, outputFile string, concurrentSegments int, progress *progressTracker) error {
	segments, err := parseMPDForAudio(mpdPath)
	if err != nil {
		return err
	}

	config := DownloadConfig{
		ConcurrentSegments: concurrentSegments,
		MaxRetries:         10,
		UserAgent:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		ExtraWorkersChan:   progress.audioExtraWorkers,
	}

	return DownloadSegmentsWithContext(ctx, segments, outputFile, "audio", config, func(percent float64, current, total int) {
		progress.updateAudio(percent, current, total)
	})
}

// validateDownloadedFiles performs basic file size validation
func validateDownloadedFiles(videoFile, audioFile string, expectedDuration time.Duration) error {
	// Check video file exists and has reasonable size
	videoInfo, err := os.Stat(videoFile)
	if err != nil {
		return fmt.Errorf("video file not found: %v", err)
	}

	audioInfo, err := os.Stat(audioFile)
	if err != nil {
		return fmt.Errorf("audio file not found: %v", err)
	}

	// Calculate minimum expected sizes based on duration (rough estimates)
	// For 59 minutes: video ~400MB (1080p), audio ~55MB (128kbps AAC)
	durationMinutes := expectedDuration.Minutes()
	minVideoSize := int64(durationMinutes * 7 * 1024 * 1024)   // ~7MB per minute for 1080p
	minAudioSize := int64(durationMinutes * 0.9 * 1024 * 1024) // ~0.9MB per minute for 128kbps audio

	if videoInfo.Size() < minVideoSize {
		return fmt.Errorf("video file too small: %.1f MB (expected >%.1f MB for %.0f min video)",
			float64(videoInfo.Size())/(1024*1024),
			float64(minVideoSize)/(1024*1024),
			durationMinutes)
	}

	if audioInfo.Size() < minAudioSize {
		return fmt.Errorf("audio file too small: %.1f MB (expected >%.1f MB for %.0f min audio)",
			float64(audioInfo.Size())/(1024*1024),
			float64(minAudioSize)/(1024*1024),
			durationMinutes)
	}

	return nil
}

// validateMediaDuration uses ffprobe to verify media duration
func validateMediaDuration(file string, expectedDuration time.Duration, mediaType string) error {
	// Use ffprobe to get actual duration
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		file)

	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ffprobe failed on %s: %v", mediaType, err)
	}

	durationStr := strings.TrimSpace(string(output))
	if durationStr == "" || durationStr == "N/A" {
		return fmt.Errorf("%s file has no duration information", mediaType)
	}

	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		return fmt.Errorf("invalid duration in %s: %v", mediaType, err)
	}

	actualDuration := time.Duration(duration * float64(time.Second))

	// Allow 5% tolerance
	minDuration := expectedDuration * 95 / 100

	if actualDuration < minDuration {
		return fmt.Errorf("%s duration too short: got %v, expected ~%v (minimum %v)",
			mediaType,
			actualDuration.Round(time.Second),
			expectedDuration.Round(time.Second),
			minDuration.Round(time.Second))
	}

	return nil
}

// muxStreams combines video and audio into final MP4
func muxStreams(tsFilename, audioFilename, outputFilename string) error {
	// -movflags +faststart is good practice for web playback
	// Use -loglevel error to suppress all output except errors
	cmdMux := exec.Command("ffmpeg", "-y", "-loglevel", "error", "-i", tsFilename, "-i", audioFilename, "-c", "copy", "-map", "0:v", "-map", "1:a", "-movflags", "+faststart", outputFilename)

	// Capture stderr in case of errors
	cmdMux.Stderr = os.Stderr

	fmt.Printf("  Remuxing file...\n")
	if err := cmdMux.Run(); err != nil {
		return fmt.Errorf("mux failed: %v", err)
	}

	return nil
}

// DownloadStream downloads video and audio, then muxes them.
// Uses sequential mode (audio first, then video) like the Perl script for reliability.
func DownloadStream(videoURL, audioURL, outputFilename string, concurrentSegments int) error {
	return downloadSequential(videoURL, audioURL, outputFilename, concurrentSegments)
}

// downloadSequential downloads audio and video concurrently
func downloadSequential(videoURL, audioURL, outputFilename string, concurrentSegments int) error {
	tsFilename := outputFilename + ".ts"
	audioFilename := outputFilename + ".m4a"
	expectedDuration := 59*time.Minute + 8*time.Second // ~59:08 for BBC content

	// Download Audio and Video concurrently
	fmt.Println("\nStep 1/3: Downloading Audio and Video concurrently...")
	if audioURL == "" {
		fmt.Println("  (No separate audio URL provided, will extract from video URL)")
	}

	// Create progress tracker for concurrent display
	progress := newProgressTracker()
	
	// Create signal channels for bidirectional thread scaling
	audioToVideo := make(chan int, 1)
	videoToAudio := make(chan int, 1)
	
	// Cross-wire the signals so each listens to the other
	progress.audioExtraWorkers = videoToAudio
	progress.videoExtraWorkers = audioToVideo

	// Channel to collect errors from goroutines
	type downloadResult struct {
		name string
		err  error
	}
	results := make(chan downloadResult, 2)

	// Download audio in goroutine
	go func() {
		err := downloadAudioWithProgress(audioURL, videoURL, audioFilename, concurrentSegments, progress)
		// Signal to Video that Audio has finished and released its thread quota
		select {
		case audioToVideo <- concurrentSegments:
		default:
		}
		
		if err != nil {
			os.Remove(audioFilename) // Clean up partial file
		}
		results <- downloadResult{name: "audio", err: err}
	}()

	// Download video in goroutine
	go func() {
		err := downloadVideoWithProgress(videoURL, tsFilename, concurrentSegments, progress)
		// Signal to Audio that Video has finished and released its thread quota
		select {
		case videoToAudio <- concurrentSegments:
		default:
		}
		
		if err != nil {
			os.Remove(tsFilename) // Clean up partial file
		}
		results <- downloadResult{name: "video", err: err}
	}()

	// Wait for both downloads to complete
	var audioErr, videoErr error
	for i := 0; i < 2; i++ {
		result := <-results
		if result.name == "audio" {
			audioErr = result.err
		} else {
			videoErr = result.err
		}
	}

	// Finish progress display and show completion
	progress.finish()
	if audioErr == nil {
		fmt.Println("  [Audio] Download Complete!")
	}
	if videoErr == nil {
		fmt.Println("  [Video] Download Complete!")
	}

	// Check for errors
	if audioErr != nil {
		os.Remove(audioFilename)
		os.Remove(tsFilename)
		return fmt.Errorf("audio download failed: %v", audioErr)
	}
	if videoErr != nil {
		os.Remove(audioFilename)
		os.Remove(tsFilename)
		return fmt.Errorf("video download failed: %v", videoErr)
	}

	// Step 2: Validate both files
	fmt.Println("\nStep 2/3: Validating downloads...")

	// Validate audio
	if err := validateMediaDuration(audioFilename, expectedDuration, "audio"); err != nil {
		os.Remove(audioFilename)
		os.Remove(tsFilename)
		return fmt.Errorf("audio validation failed: %v", err)
	}
	fmt.Println("  ✓ Audio validated")

	// Validate video
	if err := validateMediaDuration(tsFilename, expectedDuration, "video"); err != nil {
		os.Remove(tsFilename)
		os.Remove(audioFilename)
		return fmt.Errorf("video validation failed: %v", err)
	}

	// Additional file size check
	if err := validateDownloadedFiles(tsFilename, audioFilename, expectedDuration); err != nil {
		os.Remove(tsFilename)
		os.Remove(audioFilename)
		return fmt.Errorf("file size validation failed: %v", err)
	}
	fmt.Println("  ✓ Video validated")

	// Step 3: Mux
	fmt.Println("\nStep 3/3: Muxing to MP4...")
	if err := muxStreams(tsFilename, audioFilename, outputFilename); err != nil {
		return err
	}

	// Cleanup temp files after successful mux
	os.Remove(tsFilename)
	os.Remove(audioFilename)

	fmt.Println("  ✓ Mux complete!")
	return nil
}
