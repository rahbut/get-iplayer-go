package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local development
	},
}

// ProgressMessage represents a progress update sent to the client
type ProgressMessage struct {
	Type         string  `json:"type"`                    // "audio", "video", "status", "error", "complete", "step"
	Message      string  `json:"message"`                 // Human-readable message
	Percent      float64 `json:"percent"`                 // Progress percentage (0-100)
	CurrentCount int     `json:"current_count"`           // Current segment/frame count
	TotalCount   int     `json:"total_count"`             // Total segments/frames
	Step         int     `json:"step"`                    // Current step (1-3)
	TotalSteps   int     `json:"total_steps"`             // Total steps (usually 3)
	StepName     string  `json:"step_name"`               // Step name ("Downloading", "Validating", "Muxing")
	PID          string  `json:"pid"`                     // Programme ID
	Filename     string  `json:"filename"`                // Output filename
	CanCancel    bool    `json:"can_cancel"`              // Whether download can be cancelled
	Thumbnail    string  `json:"thumbnail,omitempty"`     // Base64 thumbnail data
	ShowName     string  `json:"show_name,omitempty"`     // Show name from metadata
	EpisodeTitle string  `json:"episode_title,omitempty"` // Episode title
	Quality      string  `json:"quality,omitempty"`       // Video quality (e.g., "1920x1080")
}

// WebSocketHub manages all active WebSocket connections
type WebSocketHub struct {
	clients    map[*websocket.Conn]bool
	broadcast  chan ProgressMessage
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	mu         sync.RWMutex
}

// Global hub instance
var hub = &WebSocketHub{
	clients:    make(map[*websocket.Conn]bool),
	broadcast:  make(chan ProgressMessage, 256),
	register:   make(chan *websocket.Conn),
	unregister: make(chan *websocket.Conn),
}

// Run starts the WebSocket hub
func (h *WebSocketHub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("WebSocket client connected (total: %d)", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.Close()
			}
			h.mu.Unlock()
			log.Printf("WebSocket client disconnected (total: %d)", len(h.clients))

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				err := client.WriteJSON(message)
				if err != nil {
					log.Printf("WebSocket write error: %v", err)
					client.Close()
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// BroadcastProgress sends a progress update to all connected clients
func BroadcastProgress(msg ProgressMessage) {
	select {
	case hub.broadcast <- msg:
	case <-time.After(100 * time.Millisecond):
		log.Println("Warning: WebSocket broadcast channel full, dropping message")
	}
}

// HandleWebSocket handles WebSocket connection upgrades
func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	hub.register <- conn

	// Keep connection alive with ping/pong
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		defer func() {
			hub.unregister <- conn
		}()

		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// Read messages from client (for potential cancel commands)
	go func() {
		defer func() {
			hub.unregister <- conn
		}()

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		for {
			var msg map[string]interface{}
			err := conn.ReadJSON(&msg)
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket read error: %v", err)
				}
				break
			}

			// Handle client messages (e.g., cancel command)
			if msgType, ok := msg["type"].(string); ok {
				if msgType == "cancel" {
					if pid, ok := msg["pid"].(string); ok {
						log.Printf("Received cancel request for PID: %s", pid)
						GlobalDownloadManager.CancelDownload(pid)
					}
				}
			}
		}
	}()
}

// SendJSONMessage is a helper to send structured messages
func SendJSONMessage(conn *websocket.Conn, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, jsonData)
}
