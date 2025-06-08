package main

import (
	"bufio"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// PageData holds data for the template
type PageData struct {
	Title      string
	WindowName string
}

var tmpl *template.Template
var windowName string

func init() {
	// Load template from file
	templatePath := filepath.Join("templates", "index.html")
	var err error
	tmpl, err = template.ParseFiles(templatePath)
	if err != nil {
		log.Fatalf("Failed to parse template: %v", err)
	}
}

func main() {
	// Parse command line flags
	flag.StringVar(&windowName, "window", "", "Name of the window to stream")
	flag.Parse()

	// Set up graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// List all available windows for debugging
	listAllWindows()

	// Serve the single page
	http.HandleFunc("/", serveWindowStream)

	fmt.Printf("🚀 Window Stream Server starting...\n")
	fmt.Printf("🎯 Streaming window: %s\n", windowName)
	fmt.Printf("🌐 Web interface: http://localhost:8181\n")

	// Start HTTP server
	go func() {
		log.Fatal(http.ListenAndServe(":8181", nil))
	}()

	// Wait for shutdown signal
	<-c
	fmt.Println("\n👋 Shutting down server...")
}

func serveWindowStream(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/stream" {
		streamWindow(w, r)
		return
	}

	// Serve template
	data := PageData{
		Title:      windowName + " Stream",
		WindowName: windowName,
	}
	tmpl.Execute(w, data)
}

func streamWindow(w http.ResponseWriter, r *http.Request) {
	// Set headers for MJPEG stream
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=ffmpeg")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Connection", "close")
	w.Header().Set("Pragma", "no-cache")

	// Find the specified window
	windowID, err := findWindow()
	if err != nil {
		log.Printf("❌ Failed to find window: %v", err)
		http.Error(w, "Window not found", http.StatusInternalServerError)
		return
	}

	// Get window position and size
	posCmd := exec.Command("sh", "-c", fmt.Sprintf("DISPLAY=:99 xwininfo -id %s | grep -E 'Absolute|Width|Height'", windowID))
	posOutput, err := posCmd.Output()
	if err != nil {
		log.Printf("❌ Failed to get window position: %v", err)
		http.Error(w, "Failed to get window position", http.StatusInternalServerError)
		return
	}

	// Parse window position and size
	posLines := strings.Split(string(posOutput), "\n")
	var x, y, width, height int
	for _, line := range posLines {
		if strings.Contains(line, "Absolute upper-left X:") {
			fmt.Sscanf(line, "  Absolute upper-left X:  %d", &x)
		} else if strings.Contains(line, "Absolute upper-left Y:") {
			fmt.Sscanf(line, "  Absolute upper-left Y:  %d", &y)
		} else if strings.Contains(line, "Width:") {
			fmt.Sscanf(line, "  Width: %d", &width)
		} else if strings.Contains(line, "Height:") {
			fmt.Sscanf(line, "  Height: %d", &height)
		}
	}

	if width == 0 || height == 0 {
		log.Printf("❌ Failed to parse window dimensions")
		http.Error(w, "Failed to get window dimensions", http.StatusInternalServerError)
		return
	}

	// Use FFmpeg to capture Firefox window with high quality settings
	cmd := exec.Command("ffmpeg",
		"-f", "x11grab",
		"-video_size", fmt.Sprintf("%dx%d", width, height),
		"-framerate", "60",
		"-i", fmt.Sprintf(":99+%d,%d", x, y),
		"-c:v", "mjpeg",
		"-q:v", "1", // Lower value means higher quality (1-31)
		"-huffman", "optimal", // Better compression
		"-pix_fmt", "yuvj444p", // Use full color information
		"-f", "mpjpeg",
		"-")
	cmd.Env = append(os.Environ(), "DISPLAY=:99")

	// Get pipes for command I/O
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("❌ Failed to create stderr pipe: %v", err)
		http.Error(w, "Stream error", http.StatusInternalServerError)
		return
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("❌ Failed to create stdout pipe: %v", err)
		http.Error(w, "Stream error", http.StatusInternalServerError)
		return
	}

	// Start the command
	err = cmd.Start()
	if err != nil {
		log.Printf("❌ Failed to start FFmpeg: %v", err)
		http.Error(w, "Stream error", http.StatusInternalServerError)
		return
	}

	fmt.Printf("📹 %s stream started\n", windowName)

	// Log command output in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("FFmpeg: %s", scanner.Text())
		}
	}()

	// Clean up when done
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
		fmt.Printf("📹 %s stream stopped\n", windowName)
	}()

	// Stream the data directly to the client
	buffer := make([]byte, 65536) // 64KB buffer
	for {
		n, err := stdout.Read(buffer)
		if err != nil {
			log.Printf("❌ Stream read error: %v", err)
			break
		}

		if n > 0 {
			_, writeErr := w.Write(buffer[:n])
			if writeErr != nil {
				log.Printf("❌ Stream write error: %v", writeErr)
				break
			}

			// Flush immediately
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
}

func listAllWindows() {
	// Get all visible windows
	cmd := exec.Command("sh", "-c", "DISPLAY=:99 xdotool search --onlyvisible --name . 2>/dev/null || echo ''")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		fmt.Println("❌ No windows found")
		return
	}

	// Get window IDs
	windowIDs := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(windowIDs) == 0 || windowIDs[0] == "" {
		fmt.Println("❌ No windows found")
		return
	}

	fmt.Println("🔍 Available windows:")
	for _, id := range windowIDs {
		// Get window name
		nameCmd := exec.Command("sh", "-c", fmt.Sprintf("DISPLAY=:99 xdotool getwindowname %s 2>/dev/null || echo 'Unknown'", id))
		nameOutput, err := nameCmd.Output()
		name := "Unknown"
		if err == nil {
			name = strings.TrimSpace(string(nameOutput))
		}
		fmt.Printf("   - [%s] %s\n", id, name)
	}
	fmt.Println()
}

func findWindow() (string, error) {
	// Search for the specified window
	cmd := exec.Command("sh", "-c", fmt.Sprintf("DISPLAY=:99 xdotool search --onlyvisible --name '%s' 2>/dev/null || echo ''", windowName))
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		return "", fmt.Errorf("no window matching '%s' found", windowName)
	}

	// Get the first window ID
	windowIDs := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(windowIDs) == 0 || windowIDs[0] == "" {
		return "", fmt.Errorf("no window matching '%s' found", windowName)
	}

	return windowIDs[0], nil
}
