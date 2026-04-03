package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DownloadThumbnail downloads the programme thumbnail from BBC's image CDN
func DownloadThumbnail(thumbnailPID string, outputPath string) error {
	if thumbnailPID == "" {
		return fmt.Errorf("no thumbnail PID provided")
	}

	// Try 1920x1080 first (best quality)
	urls := []string{
		fmt.Sprintf("https://ichef.bbci.co.uk/images/ic/1920x1080/%s.jpg", thumbnailPID),
		fmt.Sprintf("https://ichef.bbci.co.uk/images/ic/1280x720/%s.jpg", thumbnailPID),
	}

	var lastErr error
	for _, url := range urls {
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("thumbnail request failed with status: %d", resp.StatusCode)
			continue
		}

		// Success - write to file
		outFile, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create thumbnail file: %v", err)
		}
		defer outFile.Close()

		_, err = io.Copy(outFile, resp.Body)
		if err != nil {
			return fmt.Errorf("failed to write thumbnail: %v", err)
		}

		return nil
	}

	return fmt.Errorf("failed to download thumbnail: %v", lastErr)
}

// TagMP4File adds iTunes-compatible metadata tags to an MP4 file
func TagMP4File(filename string, metadata *ProgrammeMetadata, resolution string) error {
	if metadata == nil {
		return fmt.Errorf("no metadata provided")
	}

	fmt.Printf("  Tagging MP4 file: %s\n", filepath.Base(filename))

	// Download thumbnail to temp file
	var thumbnailPath string
	var thumbnailData []byte
	if metadata.ThumbnailPID != "" {
		thumbnailPath = filepath.Join(os.TempDir(), fmt.Sprintf("bbc_thumb_%s.jpg", metadata.PID))
		err := DownloadThumbnail(metadata.ThumbnailPID, thumbnailPath)
		if err != nil {
			fmt.Printf("  Warning: Could not download thumbnail: %v\n", err)
		} else {
			// Read thumbnail data for embedding
			thumbnailData, err = os.ReadFile(thumbnailPath)
			if err != nil {
				fmt.Printf("  Warning: Could not read thumbnail: %v\n", err)
				thumbnailData = nil
			}
			// Schedule cleanup
			defer os.Remove(thumbnailPath)
		}
	}

	// Determine HD video flag
	hdVideo := 0
	if strings.Contains(resolution, "1920x1080") {
		hdVideo = 2 // 1080p
	} else if strings.Contains(resolution, "1280x720") {
		hdVideo = 1 // 720p
	}

	// Build lyrics field (long description + links)
	lyrics := metadata.LongSynopsis
	if metadata.PlayerURL != "" {
		lyrics += fmt.Sprintf("\n\nPLAY: %s", metadata.PlayerURL)
	}
	if metadata.WebURL != "" {
		lyrics += fmt.Sprintf("\n\nINFO: %s", metadata.WebURL)
	}

	// Extract year from first broadcast date
	year := time.Now().Format("2006")
	if metadata.FirstBroadcast != "" {
		if len(metadata.FirstBroadcast) >= 4 {
			year = metadata.FirstBroadcast[:4]
		}
	}

	// Build copyright string
	copyright := fmt.Sprintf("%s BBC (programme data only)", year)

	// Print what we're about to tag
	fmt.Printf("  Metadata to write:\n")
	fmt.Printf("    Title: %s\n", metadata.EpisodeTitle)
	fmt.Printf("    Show: %s\n", metadata.ShowName)
	fmt.Printf("    Episode: S%02dE%02d\n", metadata.SeriesNum, metadata.EpisodeNum)
	fmt.Printf("    Channel: %s\n", metadata.Channel)
	fmt.Printf("    HD Flag: %d\n", hdVideo)
	if len(thumbnailData) > 0 {
		fmt.Printf("    Artwork: %d bytes\n", len(thumbnailData))
	}

	// Use ffmpeg to write metadata tags
	// Create temp output file
	tempFile := filename + ".tagged.mp4"

	// Build ffmpeg command with metadata
	args := []string{
		"-i", filename,
	}

	// Add artwork as second input if available
	if len(thumbnailData) > 0 && thumbnailPath != "" {
		args = append(args, "-i", thumbnailPath)
	}

	// Map streams
	args = append(args, "-map", "0") // Map all streams from main input
	if len(thumbnailData) > 0 && thumbnailPath != "" {
		args = append(args, "-map", "1") // Map artwork from second input
	}

	// Copy without re-encoding
	args = append(args, "-c", "copy")

	// Set artwork disposition if present
	if len(thumbnailData) > 0 && thumbnailPath != "" {
		args = append(args, "-disposition:v:1", "attached_pic")
	}

	// Add metadata tags
	if metadata.EpisodeTitle != "" {
		args = append(args, "-metadata", fmt.Sprintf("title=%s", metadata.EpisodeTitle))
	}
	if metadata.Channel != "" {
		args = append(args, "-metadata", fmt.Sprintf("artist=%s", metadata.Channel))
	}
	if metadata.ShowName != "" {
		args = append(args, "-metadata", fmt.Sprintf("album=%s", metadata.ShowName))
		args = append(args, "-metadata", fmt.Sprintf("show=%s", metadata.ShowName))
	}
	if metadata.SeriesNum > 0 {
		args = append(args, "-metadata", fmt.Sprintf("season_number=%d", metadata.SeriesNum))
	}
	if metadata.EpisodeNum > 0 {
		args = append(args, "-metadata", fmt.Sprintf("episode_sort=%d", metadata.EpisodeNum))
	}
	if year != "" {
		args = append(args, "-metadata", fmt.Sprintf("date=%s", year))
	}
	if metadata.Genre != "" {
		args = append(args, "-metadata", fmt.Sprintf("genre=%s", metadata.Genre))
	}
	if metadata.ShortSynopsis != "" {
		args = append(args, "-metadata", fmt.Sprintf("comment=%s", metadata.ShortSynopsis))
		args = append(args, "-metadata", fmt.Sprintf("description=%s", metadata.ShortSynopsis))
	}
	if lyrics != "" {
		args = append(args, "-metadata", fmt.Sprintf("synopsis=%s", lyrics))
	}
	if copyright != "" {
		args = append(args, "-metadata", fmt.Sprintf("copyright=%s", copyright))
	}
	if metadata.Channel != "" {
		args = append(args, "-metadata", fmt.Sprintf("network=%s", metadata.Channel))
	}
	if hdVideo > 0 {
		args = append(args, "-metadata", fmt.Sprintf("hd_video=%d", hdVideo))
	}

	// Output file
	args = append(args, "-y", tempFile)

	// Run ffmpeg
	cmd := exec.Command("ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg tagging failed: %v\nOutput: %s", err, string(output))
	}

	// Replace original file with tagged version
	if err := os.Remove(filename); err != nil {
		return fmt.Errorf("failed to remove original file: %v", err)
	}
	if err := os.Rename(tempFile, filename); err != nil {
		return fmt.Errorf("failed to rename tagged file: %v", err)
	}

	fmt.Printf("  ✓ Metadata tags written successfully\n")
	return nil
}
