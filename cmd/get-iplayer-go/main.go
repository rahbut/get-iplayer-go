package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed templates/*
var resources embed.FS

var t = template.Must(template.ParseFS(resources, "templates/*"))

// Concurrent segments per stream is fixed at 5

// ensureDownloadDir creates the downloads directory if it doesn't exist
func ensureDownloadDir() error {
	downloadDir := "downloads"
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return fmt.Errorf("failed to create downloads directory: %v", err)
	}
	return nil
}

// getDownloadPath returns the full path for a filename in the downloads directory
func getDownloadPath(filename string) string {
	return filepath.Join("downloads", filename)
}

func main() {
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("Usage: get-iplayer-go <PID or URL>")
		fmt.Println("       get-iplayer-go web")
		fmt.Println("")
		fmt.Println("  <PID or URL>: The episode PID or URL to download.")
		fmt.Println("  web: Start the web server interface.")
		fmt.Println("\nOptions:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	input := args[0]

	// Check if user wants web mode
	if input == "web" {
		startWebServer()
		return
	}

	fmt.Printf("get-iplayer-go %s (%s)\n", Version, BuildTime)
	runCLI(input)
}

func runCLI(input string) {
	// Ensure downloads directory exists
	if err := ensureDownloadDir(); err != nil {
		log.Fatalf("Error creating downloads directory: %v", err)
	}

	pid, err := ResolvePID(input)
	if err != nil {
		log.Fatalf("Error resolving PID: %v", err)
	}
	fmt.Printf("Resolved PID: %s\n", pid)

	fmt.Println("Searching for streams...")
	streams, err := FindStreams(pid)
	if err != nil {
		log.Fatalf("Error finding streams: %v", err)
	}

	if len(streams) == 0 {
		log.Fatalf("No streams found for PID: %s", pid)
	}

	// Count streams by format
	dashCount, hlsCount := countStreamsByFormat(streams)
	formatBreakdown := ""
	if dashCount > 0 && hlsCount > 0 {
		formatBreakdown = fmt.Sprintf(" (%d dash, %d hls)", dashCount, hlsCount)
	} else if dashCount > 0 {
		formatBreakdown = fmt.Sprintf(" (%d dash)", dashCount)
	} else if hlsCount > 0 {
		formatBreakdown = fmt.Sprintf(" (%d hls)", hlsCount)
	}

	// Best stream is already first due to sorting
	bestStream := streams[0]
	fmt.Printf("Found %d streams%s, selecting best quality: %s @ %d kbps (%s)\n",
		len(streams), formatBreakdown, bestStream.Resolution, bestStream.Bitrate, bestStream.Format)

	// Generate human-readable filename if metadata available, otherwise use PID
	filename := generateFilename(pid, bestStream.Metadata)
	fullPath := getDownloadPath(filename)
	fmt.Printf("\nStarting download to %s...\n", fullPath)

	// Use 5 concurrent segments initially per stream (dynamically scales to 10)
	const concurrentSegments = 5
	if err := DownloadStream(bestStream.URL, bestStream.AudioURL, fullPath, concurrentSegments); err != nil {
		log.Fatalf("Download failed: %v", err)
	}

	fmt.Println("\nDownload complete!")

	// Tag the file with metadata if available
	if bestStream.Metadata != nil {
		fmt.Println("\nAdding metadata tags...")
		if err := TagMP4File(fullPath, bestStream.Metadata, bestStream.Resolution); err != nil {
			fmt.Printf("Warning: Tagging failed: %v\n", err)
			fmt.Println("(File downloaded successfully, but without metadata tags)")
		} else {
			fmt.Println("Metadata tagging complete!")
		}
	} else {
		fmt.Println("\nNo metadata available for tagging")
	}

	fmt.Println("\nAll done!")
}

// generateFilename creates a human-readable filename from metadata
// Format: Show.Name.SxxExx.mp4 (e.g., Death.in.Paradise.S15E01.mp4)
// Falls back to PID.mp4 if metadata unavailable
// NOTE: Series number comes from BBC metadata which may occasionally be incorrect
func generateFilename(pid string, metadata *ProgrammeMetadata) string {
	if metadata == nil || metadata.ShowName == "" || metadata.SeriesNum == 0 || metadata.EpisodeNum == 0 {
		// No metadata or incomplete info, use PID
		return fmt.Sprintf("%s.mp4", pid)
	}

	// Sanitize show name: replace spaces with periods, remove problematic characters
	showName := strings.ReplaceAll(metadata.ShowName, " ", ".")
	// Remove characters that might be problematic in filenames
	showName = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return -1 // Remove character
		}
		return r
	}, showName)

	// Format as Show.Name.SxxExx.mp4
	return fmt.Sprintf("%s.S%02dE%02d.mp4", showName, metadata.SeriesNum, metadata.EpisodeNum)
}

// countStreamsByFormat returns counts of streams by format type
func countStreamsByFormat(streams []StreamInfo) (dash int, hls int) {
	for _, s := range streams {
		if s.Format == "dash" {
			dash++
		} else if s.Format == "hls" {
			hls++
		}
	}
	return dash, hls
}

// --- Web Server Logic (Preserved but secondary) ---

func startWebServer() {
	// Start WebSocket hub
	go hub.Run()

	port := os.Getenv("PORT")
	if port == "" {
		port = "7373"
	}

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/process", processHandler)
	http.HandleFunc("/download", downloadHandler)
	http.HandleFunc("/ws", HandleWebSocket)
	http.HandleFunc("/cancel", cancelHandler)

	fmt.Printf("Starting server on http://localhost:%s\n", port)
	fmt.Printf("Open your browser and navigate to: http://localhost:%s\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

var (
	BuildTime = "development"
	Version   = "dev"
)

type PageData struct {
	PID        string
	BestStream *StreamInfo
	Error      string
	BuildTime  string
	Version    string
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	t.ExecuteTemplate(w, "index.html", PageData{
		BuildTime: BuildTime,
		Version:   Version,
	})
}

func processHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	input := r.FormValue("input")
	fmt.Printf("Processing request for input: %s\n", input)

	w.Header().Set("Content-Type", "application/json")

	// Check if this is a series URL
	if IsSeriesURL(input) {
		episodes, err := FetchSeriesEpisodes(input)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": fmt.Sprintf("Error fetching series episodes: %v", err),
			})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"is_series":       true,
			"series_episodes": episodes,
		})
		return
	}

	pid, err := ResolvePID(input)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("Error resolving PID: %v", err),
		})
		return
	}

	streams, err := FindStreams(pid)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("Error searching for streams: %v", err),
		})
		return
	}

	if len(streams) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "No streams found",
		})
		return
	}

	// Because we sorted streams in FindStreams, the first one is the best.
	bestStream := &streams[0]

	// Prepare metadata
	metadata := map[string]interface{}{
		"pid":       pid,
		"url":       bestStream.URL,
		"audio_url": bestStream.AudioURL,
		"quality":   bestStream.Resolution,
		"format":    bestStream.Format,
		"bitrate":   bestStream.Bitrate,
	}

	// Add programme metadata if available
	if bestStream.Metadata != nil {
		metadata["show_name"] = bestStream.Metadata.ShowName
		metadata["episode_title"] = bestStream.Metadata.EpisodeTitle
		metadata["series_num"] = bestStream.Metadata.SeriesNum
		metadata["episode_num"] = bestStream.Metadata.EpisodeNum

		// Add thumbnail URL if available
		if bestStream.Metadata.ThumbnailPID != "" {
			metadata["thumbnail"] = fmt.Sprintf("https://ichef.bbci.co.uk/images/ic/640x360/%s.jpg", bestStream.Metadata.ThumbnailPID)
		}
	}

	json.NewEncoder(w).Encode(metadata)
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	videoURL := r.FormValue("url")
	audioURL := r.FormValue("audio_url")
	pid := r.FormValue("pid")
	quality := r.FormValue("quality")

	// Get metadata for this PID
	streams, err := FindStreams(pid)
	if err != nil || len(streams) == 0 {
		http.Error(w, "Failed to find stream metadata", http.StatusInternalServerError)
		return
	}

	bestStream := &streams[0]

	if err := GlobalDownloadManager.StartDownload(pid, videoURL, audioURL, bestStream.Metadata, quality); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start download: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success": true, "pid": "%s"}`, pid)
}

func cancelHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pid := r.FormValue("pid")
	if pid == "" {
		http.Error(w, "PID required", http.StatusBadRequest)
		return
	}

	if err := GlobalDownloadManager.CancelDownload(pid); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success": true}`)
}
