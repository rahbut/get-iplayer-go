package main

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrorPattern represents a pattern to detect in ffmpeg output
type ErrorPattern struct {
	Pattern string
	IsFatal bool
	Message string
}

// Common ffmpeg error patterns that indicate failure
var ffmpegErrorPatterns = []ErrorPattern{
	{"could not find sync byte", true, "Stream corruption detected"},
	{"max resync size reached", true, "Stream data corrupted or truncated"},
	{"Connection timed out", true, "Network connection timeout"},
	{"Connection refused", true, "Server refused connection"},
	{"HTTP error 403", true, "Authentication failed (403 Forbidden)"},
	{"HTTP error 404", true, "Resource not found (404)"},
	{"No such file or directory", true, "File not found"},
	{"Invalid data found when processing input", true, "Invalid or corrupted input data"},
	{"Error opening input", true, "Failed to open input file/stream"},
	{"Server returned 4", true, "HTTP client error (4xx)"},
	{"Server returned 5", true, "HTTP server error (5xx)"},
	{"moov atom not found", true, "Incomplete or corrupted media file"},
}

// ProgressTracker tracks download progress and detects stalls
type ProgressTracker struct {
	sync.Mutex
	LastUpdate      time.Time
	LastPercent     float64
	LastFrame       int
	FrameStallCount int
	StallThreshold  time.Duration
	HasError        bool
	ErrorMessage    string
}

// RunCommandWithProgress runs the command and parses ffmpeg output for progress.
// It detects errors, stalls, and provides detailed progress tracking.
func RunCommandWithProgress(taskName string, name string, args []string, onProgress func(string)) error {
	return RunCommandWithProgressAndContext(context.Background(), taskName, name, args, onProgress)
}

// RunCommandWithProgressAndContext runs command with context support for cancellation
func RunCommandWithProgressAndContext(ctx context.Context, taskName string, name string, args []string, onProgress func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// Create log file to capture full ffmpeg output for debugging
	logFile, err := os.Create(fmt.Sprintf("ffmpeg_%s.log", strings.ToLower(taskName)))
	if err != nil {
		fmt.Printf("Warning: could not create log file: %v\n", err)
	}
	defer func() {
		if logFile != nil {
			logFile.Close()
		}
	}()

	// Progress tracker with stall detection
	tracker := &ProgressTracker{
		LastUpdate:     time.Now(),
		StallThreshold: 30 * time.Second, // 30 seconds without progress = stalled
	}

	// Start watchdog goroutine to detect stalls
	watchdogCtx, watchdogCancel := context.WithCancel(ctx)
	defer watchdogCancel()

	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				tracker.Lock()
				timeSinceUpdate := time.Since(tracker.LastUpdate)
				lastPercent := tracker.LastPercent
				tracker.Unlock()

				if timeSinceUpdate > tracker.StallThreshold {
					// Stalled! Try to kill the process
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
					tracker.Lock()
					tracker.HasError = true
					tracker.ErrorMessage = fmt.Sprintf("Download stalled at %.1f%% for %v",
						lastPercent, timeSinceUpdate.Round(time.Second))
					tracker.Unlock()
					return
				}
			case <-watchdogCtx.Done():
				return
			}
		}
	}()

	// Regex patterns
	reDuration := regexp.MustCompile(`Duration: (\d{2}:\d{2}:\d{2}\.\d{2})`)
	reTime := regexp.MustCompile(`time=(\d{2}:\d{2}:\d{2}\.\d{2})`)
	reFrame := regexp.MustCompile(`frame=\s*(\d+)`)

	var totalDuration time.Duration
	var detectedError string

	scanner := bufio.NewScanner(stderr)
	scanner.Split(scanLinesWithCR)

	for scanner.Scan() {
		line := scanner.Text()

		// Log all output to file
		if logFile != nil {
			logFile.WriteString(line + "\n")
		}

		// Check for error patterns
		for _, pattern := range ffmpegErrorPatterns {
			if strings.Contains(line, pattern.Pattern) && pattern.IsFatal {
				detectedError = fmt.Sprintf("%s: %s", pattern.Message, line)
				tracker.Lock()
				tracker.HasError = true
				tracker.ErrorMessage = detectedError
				tracker.Unlock()
				// Log the error immediately so user can see it
				fmt.Printf("\n  [ERROR DETECTED] %s\n", detectedError)
				// Continue reading to completion but remember the error
			}
		}

		// Parse Duration if we haven't yet
		if totalDuration == 0 {
			matches := reDuration.FindStringSubmatch(line)
			if len(matches) > 1 {
				totalDuration, _ = parseTime(matches[1])
			}
		}

		// Parse frame count (for video)
		if frameMatches := reFrame.FindStringSubmatch(line); len(frameMatches) > 1 {
			frame, _ := strconv.Atoi(strings.TrimSpace(frameMatches[1]))

			tracker.Lock()
			if frame == tracker.LastFrame && frame > 0 {
				tracker.FrameStallCount++

				// If same frame for too long, it's stalled
				if tracker.FrameStallCount > 20 && time.Since(tracker.LastUpdate) > 15*time.Second {
					tracker.HasError = true
					tracker.ErrorMessage = fmt.Sprintf("Video encoding stalled at frame %d", frame)
					tracker.Unlock()
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
					continue
				}
			} else if frame > tracker.LastFrame {
				tracker.LastFrame = frame
				tracker.FrameStallCount = 0
			}
			tracker.Unlock()
		}

		// Parse Time and calculate percentage
		if strings.Contains(line, "time=") {
			matches := reTime.FindStringSubmatch(line)
			if len(matches) > 1 {
				currentTime, _ := parseTime(matches[1])
				if totalDuration > 0 {
					percent := math.Min(100, (float64(currentTime)/float64(totalDuration))*100)
					msg := fmt.Sprintf("%s: %.1f%%", taskName, percent)
					onProgress(msg)

					// Update progress tracker
					tracker.Lock()
					if percent > tracker.LastPercent {
						tracker.LastUpdate = time.Now()
						tracker.LastPercent = percent
					}
					tracker.Unlock()
				}
			}
		}
	}

	// Wait for command to finish
	cmdErr := cmd.Wait()

	// Stop watchdog
	watchdogCancel()
	<-watchdogDone

	// Check if we detected any errors
	tracker.Lock()
	hasError := tracker.HasError
	errorMessage := tracker.ErrorMessage
	tracker.Unlock()

	if hasError {
		return fmt.Errorf("%s", errorMessage)
	}

	return cmdErr
}

// scanLinesWithCR is a split function for bufio.Scanner that handles \r as a line ending
func scanLinesWithCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := indexOf(data, '\n'); i >= 0 {
		return i + 1, dropCR(data[0:i]), nil
	}
	if i := indexOf(data, '\r'); i >= 0 {
		return i + 1, dropCR(data[0:i]), nil
	}
	if atEOF {
		return len(data), dropCR(data), nil
	}
	return 0, nil, nil
}

func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[0 : len(data)-1]
	}
	return data
}

func parseTime(timeStr string) (time.Duration, error) {
	// Format is usually 00:00:00.00
	parts := strings.Split(timeStr, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid time format")
	}

	h, _ := strconv.ParseFloat(parts[0], 64)
	m, _ := strconv.ParseFloat(parts[1], 64)
	s, _ := strconv.ParseFloat(parts[2], 64) // This includes the decimal part

	seconds := h*3600 + m*60 + s
	return time.Duration(seconds * float64(time.Second)), nil
}

func indexOf(data []byte, b byte) int {
	for i, c := range data {
		if c == b {
			return i
		}
	}
	return -1
}
