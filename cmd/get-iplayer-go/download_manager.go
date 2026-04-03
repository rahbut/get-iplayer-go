package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

type DownloadStatus struct {
	PID          string
	Filename     string
	IsRunning    bool
	IsCancelling bool
	Done         bool
	Error        error
	Metadata     *ProgrammeMetadata
	Quality      string
	cancelFunc   context.CancelFunc
}

type DownloadManager struct {
	downloads map[string]*DownloadStatus
	mu        sync.RWMutex
}

var GlobalDownloadManager = &DownloadManager{
	downloads: make(map[string]*DownloadStatus),
}

// StartDownload initiates a new download with WebSocket progress updates
func (dm *DownloadManager) StartDownload(pid, videoURL, audioURL string, metadata *ProgrammeMetadata, quality string) error {
	dm.mu.Lock()
	if status, exists := dm.downloads[pid]; exists && status.IsRunning {
		dm.mu.Unlock()
		return fmt.Errorf("download already in progress for PID: %s", pid)
	}

	// Ensure downloads directory exists
	if err := os.MkdirAll("downloads", 0755); err != nil {
		dm.mu.Unlock()
		return fmt.Errorf("failed to create downloads directory: %v", err)
	}

	// Generate filename from metadata
	filename := generateFilename(pid, metadata)
	fullPath := filepath.Join("downloads", filename)

	// Create cancellable context
	ctx, cancelFunc := context.WithCancel(context.Background())

	status := &DownloadStatus{
		PID:        pid,
		Filename:   fullPath,
		IsRunning:  true,
		Metadata:   metadata,
		Quality:    quality,
		cancelFunc: cancelFunc,
	}
	dm.downloads[pid] = status
	dm.mu.Unlock()

	// Get thumbnail if available
	thumbnail := ""
	if metadata != nil && metadata.ThumbnailPID != "" {
		// BBC thumbnail URL pattern
		thumbnailURL := fmt.Sprintf("https://ichef.bbci.co.uk/images/ic/640x360/%s.jpg", metadata.ThumbnailPID)
		if thumbData, err := downloadThumbnail(thumbnailURL); err == nil {
			thumbnail = thumbData
		}
	}

	// Send initial message with metadata
	BroadcastProgress(ProgressMessage{
		Type:         "started",
		Message:      "Download started",
		PID:          pid,
		Filename:     fullPath,
		CanCancel:    true,
		Thumbnail:    thumbnail,
		ShowName:     getShowName(metadata),
		EpisodeTitle: getEpisodeTitle(metadata),
		Quality:      quality,
	})

	// Run download in goroutine
	go func() {
		defer func() {
			dm.mu.Lock()
			status.IsRunning = false
			status.Done = true
			dm.mu.Unlock()
		}()

		// Download with context for cancellation support
		if err := dm.downloadWithProgress(ctx, pid, videoURL, audioURL, fullPath, metadata, quality, thumbnail); err != nil {
			dm.mu.Lock()
			status.Error = err
			dm.mu.Unlock()

			if ctx.Err() == context.Canceled {
				BroadcastProgress(ProgressMessage{
					Type:     "cancelled",
					Message:  "Download cancelled",
					PID:      pid,
					Filename: filename,
				})
			} else {
				BroadcastProgress(ProgressMessage{
					Type:     "error",
					Message:  fmt.Sprintf("Download failed: %v", err),
					PID:      pid,
					Filename: filename,
				})
			}
			return
		}

		// Success!
		BroadcastProgress(ProgressMessage{
			Type:     "complete",
			Message:  "Download complete!",
			PID:      pid,
			Filename: filename,
		})
	}()

	return nil
}

// downloadWithProgress performs the actual download with WebSocket progress updates
func (dm *DownloadManager) downloadWithProgress(ctx context.Context, pid, videoURL, audioURL, filename string, metadata *ProgrammeMetadata, quality, thumbnail string) error {
	tsFilename := filename + ".ts"
	audioFilename := filename + ".m4a"

	// Step 1: Download Audio and Video concurrently
	BroadcastProgress(ProgressMessage{
		Type:       "step",
		Message:    "Downloading Audio and Video",
		Step:       1,
		TotalSteps: 3,
		StepName:   "Downloading",
		PID:        pid,
		CanCancel:  true,
	})

	// Create channels for results
	type downloadResult struct {
		name string
		err  error
	}
	results := make(chan downloadResult, 2)

	// Progress trackers for audio and video
	audioProgressFunc := func(percent float64, current, total int) {
		BroadcastProgress(ProgressMessage{
			Type:         "audio",
			Message:      fmt.Sprintf("Audio: %.1f%%", percent),
			Percent:      percent,
			CurrentCount: current,
			TotalCount:   total,
			PID:          pid,
			CanCancel:    true,
		})
	}

	videoProgressFunc := func(percent float64, current, total int) {
		BroadcastProgress(ProgressMessage{
			Type:         "video",
			Message:      fmt.Sprintf("Video: %.1f%%", percent),
			Percent:      percent,
			CurrentCount: current,
			TotalCount:   total,
			PID:          pid,
			CanCancel:    true,
		})
	}

	// Maintain exactly 5 concurrent workers initially for balanced loading
	baseConcurrency := 5
	audioToVideo := make(chan int, 1)
	videoToAudio := make(chan int, 1)

	// Download audio
	go func() {
		err := dm.downloadAudioWithContext(ctx, audioURL, videoURL, audioFilename, baseConcurrency, audioProgressFunc, videoToAudio)
		
		// Audio finished; send signal to Video to absorb our token capacity
		select {
		case audioToVideo <- baseConcurrency:
		default:
		}
		
		if err != nil && ctx.Err() == nil {
			os.Remove(audioFilename)
		}
		results <- downloadResult{name: "audio", err: err}
	}()

	// Download video
	go func() {
		err := dm.downloadVideoWithContext(ctx, videoURL, tsFilename, baseConcurrency, videoProgressFunc, audioToVideo)
		
		// Video finished; send signal to Audio to absorb our token capacity
		select {
		case videoToAudio <- baseConcurrency:
		default:
		}
		
		if err != nil && ctx.Err() == nil {
			os.Remove(tsFilename)
		}
		results <- downloadResult{name: "video", err: err}
	}()

	// Wait for both downloads
	var audioErr, videoErr error
	for i := 0; i < 2; i++ {
		result := <-results

		// Check for cancellation
		if ctx.Err() == context.Canceled {
			os.Remove(audioFilename)
			os.Remove(tsFilename)
			return ctx.Err()
		}

		if result.name == "audio" {
			audioErr = result.err
		} else {
			videoErr = result.err
		}
	}

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

	// Step 2: Validate
	BroadcastProgress(ProgressMessage{
		Type:       "step",
		Message:    "Validating downloads",
		Step:       2,
		TotalSteps: 3,
		StepName:   "Validating",
		PID:        pid,
		CanCancel:  false,
	})

	// Check for cancellation before validation
	if ctx.Err() == context.Canceled {
		os.Remove(audioFilename)
		os.Remove(tsFilename)
		return ctx.Err()
	}

	// Basic validation - check files exist and have size
	audioInfo, err := os.Stat(audioFilename)
	if err != nil || audioInfo.Size() == 0 {
		os.Remove(audioFilename)
		os.Remove(tsFilename)
		return fmt.Errorf("audio file validation failed")
	}

	videoInfo, err := os.Stat(tsFilename)
	if err != nil || videoInfo.Size() == 0 {
		os.Remove(audioFilename)
		os.Remove(tsFilename)
		return fmt.Errorf("video file validation failed")
	}

	BroadcastProgress(ProgressMessage{
		Type:    "status",
		Message: "✓ Files validated",
		PID:     pid,
	})

	// Step 3: Mux
	BroadcastProgress(ProgressMessage{
		Type:       "step",
		Message:    "Muxing to MP4",
		Step:       3,
		TotalSteps: 3,
		StepName:   "Muxing",
		PID:        pid,
		CanCancel:  false,
	})

	// Check for cancellation before muxing
	if ctx.Err() == context.Canceled {
		os.Remove(audioFilename)
		os.Remove(tsFilename)
		return ctx.Err()
	}

	if err := muxStreams(tsFilename, audioFilename, filename); err != nil {
		os.Remove(audioFilename)
		os.Remove(tsFilename)
		return fmt.Errorf("mux failed: %v", err)
	}

	// Cleanup temp files
	os.Remove(tsFilename)
	os.Remove(audioFilename)

	BroadcastProgress(ProgressMessage{
		Type:    "status",
		Message: "✓ Mux complete",
		PID:     pid,
	})

	// Add metadata tags if available
	if metadata != nil {
		BroadcastProgress(ProgressMessage{
			Type:    "status",
			Message: "Adding metadata tags...",
			PID:     pid,
		})

		if err := TagMP4File(filename, metadata, quality); err != nil {
			// Non-fatal error, just log it
			BroadcastProgress(ProgressMessage{
				Type:    "status",
				Message: fmt.Sprintf("Warning: Tagging failed: %v", err),
				PID:     pid,
			})
		} else {
			BroadcastProgress(ProgressMessage{
				Type:    "status",
				Message: "✓ Metadata tags added",
				PID:     pid,
			})
		}
	}

	return nil
}

// downloadAudioWithContext downloads audio with cancellation support
// Reuses the MPD parsing logic from downloader.go functions
func (dm *DownloadManager) downloadAudioWithContext(ctx context.Context, audioURL, videoURL, audioFilename string, concurrentSegments int, progressFunc func(float64, int, int), extraWorkersChan <-chan int) error {
	targetAudioURL := audioURL
	if targetAudioURL == "" {
		targetAudioURL = videoURL
	}

	// Use the helper function to parse MPD and get segments
	segments, err := parseMPDForAudio(targetAudioURL)
	if err != nil {
		return err
	}

	config := DownloadConfig{
		ConcurrentSegments: concurrentSegments,
		MaxRetries:         10,
		UserAgent:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		ExtraWorkersChan:   extraWorkersChan,
	}

	return DownloadSegmentsWithContext(ctx, segments, audioFilename, "audio", config, progressFunc)
}

// downloadVideoWithContext downloads video with cancellation support
// Reuses the MPD parsing logic from downloader.go functions
func (dm *DownloadManager) downloadVideoWithContext(ctx context.Context, videoURL, tsFilename string, concurrentSegments int, progressFunc func(float64, int, int), extraWorkersChan <-chan int) error {
	// Use the helper function to parse MPD and get segments
	segments, err := parseMPDForVideo(videoURL)
	if err != nil {
		return err
	}

	config := DownloadConfig{
		ConcurrentSegments: concurrentSegments,
		MaxRetries:         10,
		UserAgent:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		ExtraWorkersChan:   extraWorkersChan,
	}

	return DownloadSegmentsWithContext(ctx, segments, tsFilename, "video", config, progressFunc)
}

// CancelDownload cancels a running download
func (dm *DownloadManager) CancelDownload(pid string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	status, exists := dm.downloads[pid]
	if !exists {
		return fmt.Errorf("no download found for PID: %s", pid)
	}

	if !status.IsRunning {
		return fmt.Errorf("download is not running for PID: %s", pid)
	}

	if status.IsCancelling {
		return fmt.Errorf("download is already being cancelled for PID: %s", pid)
	}

	status.IsCancelling = true
	if status.cancelFunc != nil {
		status.cancelFunc()
	}

	return nil
}

// GetStatus returns the status of a download
func (dm *DownloadManager) GetStatus(pid string) *DownloadStatus {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if status, ok := dm.downloads[pid]; ok {
		// Return a copy to avoid race conditions
		statusCopy := *status
		statusCopy.cancelFunc = nil // Don't expose the cancel function
		return &statusCopy
	}
	return nil
}

// downloadThumbnail downloads and encodes a thumbnail as base64
func downloadThumbnail(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Encode as base64 with data URI prefix
	encoded := base64.StdEncoding.EncodeToString(data)
	return "data:image/jpeg;base64," + encoded, nil
}

// Helper functions to safely extract metadata
func getShowName(metadata *ProgrammeMetadata) string {
	if metadata == nil {
		return ""
	}
	return metadata.ShowName
}

func getEpisodeTitle(metadata *ProgrammeMetadata) string {
	if metadata == nil {
		return ""
	}
	return metadata.EpisodeTitle
}
