package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	"github.com/pion/webrtc/v4/pkg/media/oggreader"
)

// Default configuration - can be overridden via config.json or CLI
const (
	DefaultSignalingServerURL = "ws://192.168.56.1:3000"
	DefaultStunServerURL      = "stun:stun.l.google.com:19302"
	DefaultCameraName         = "laptop_cam"
	DefaultPythonCompiler     = "../../face_recog/.venv/Scripts/python"
	DefaultFaceRecogScript    = "../face_identify.py"
	FaceRecogFrameInterval    = 500 * time.Millisecond
)

// Config holds all configuration options
type Config struct {
	SignalingServerURL string      `json:"signalingServerUrl"`
	StunServerURL      string      `json:"stunServerUrl"`
	CameraName         string      `json:"cameraName"`
	PythonCompiler     string      `json:"pythonCompiler"`
	FaceRecogScript    string      `json:"faceRecogScript"`
	LoopVideo          bool        `json:"loopVideo"`
	AudioFile          string      `json:"audioFile"`
	ICECredentials     []ICEServer `json:"iceCredentials,omitempty"`
}

// ICEServer represents a TURN/STUN server with optional auth
type ICEServer struct {
	URLs       string `json:"urls"`
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// Global config with defaults
var config = Config{
	SignalingServerURL: DefaultSignalingServerURL,
	StunServerURL:      DefaultStunServerURL,
	CameraName:         DefaultCameraName,
	PythonCompiler:     DefaultPythonCompiler,
	FaceRecogScript:    DefaultFaceRecogScript,
	ICECredentials: []ICEServer{
		{URLs: "stun:stun.l.google.com:19302"},
		{URLs: "stun:stun1.l.google.com:19302"},
		{URLs: "stun:stun.relay.metered.ca:80"},
		{URLs: "turn:global.relay.metered.ca:80", Username: "", Credential: ""},
		{URLs: "turn:global.relay.metered.ca:80?transport=tcp", Username: "", Credential: ""},
		{URLs: "turn:global.relay.metered.ca:443", Username: "", Credential: ""},
		{URLs: "turns:global.relay.metered.ca:443?transport=tcp", Username: "", Credential: ""},
	},
}

// SignalingMessage represents WebSocket messages
type SignalingMessage struct {
	Type         string                     `json:"type"`
	RoomID       string                     `json:"roomId,omitempty"`
	Name         string                     `json:"name,omitempty"`
	IsCam        bool                       `json:"isCam,omitempty"`
	TargetUserID string                     `json:"targetUserId,omitempty"`
	Offer        *webrtc.SessionDescription `json:"offer,omitempty"`
	Answer       *webrtc.SessionDescription `json:"answer,omitempty"`
	Candidate    *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
	SDP          string                     `json:"sdp,omitempty"`
	MyUserID     string                     `json:"myUserId,omitempty"`
	Users        []UserInfo                 `json:"users,omitempty"`
	UserID       string                     `json:"userId,omitempty"`
	Persons      []PersonInfo               `json:"persons,omitempty"`
	Timestamp    int64                      `json:"timestamp,omitempty"`
	Message      string                     `json:"message,omitempty"`
}

// UserInfo represents a user in the room
type UserInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	IsCam bool   `json:"isCam"`
}

// PersonInfo represents a detected person
type PersonInfo struct {
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
	Bbox       *BBox   `json:"bbox,omitempty"`
}

// Bbox represents bounding box
type BBox struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// FaceRecogRequest is sent to Python wrapper
type FaceRecogRequest struct {
	Frame     string `json:"frame"`
	Timestamp int64  `json:"timestamp"`
}

// FaceRecogResponse from Python wrapper
type FaceRecogResponse struct {
	Persons   []PersonInfo `json:"persons"`
	Timestamp int64        `json:"timestamp"`
	Error     string       `json:"error,omitempty"`
	DetTime   float64      `json:"det_time"`
	RecTime   float64      `json:"rec_time"`
}

// CameraPeer manages WebRTC connection as a camera
type CameraPeer struct {
	roomID           string
	userID           string
	ws               *websocket.Conn
	pc               *webrtc.PeerConnection
	videoTrack       *webrtc.TrackLocalStaticSample
	audioTrack       *webrtc.TrackLocalStaticSample
	viewers          map[string]bool
	viewersMu        sync.RWMutex
	stopCh           chan struct{}
	videoInput       string
	audioEnabled     bool
	faceRecogEnabled bool
	loopVideo        bool
	audioFile        string
	faceRecogCmd     *exec.Cmd
	faceRecogStdin   io.WriteCloser
	faceRecogStdout  *bufio.Scanner
	faceRecogMu      sync.Mutex
	lastFrame        []byte
	lastFrameMu      sync.RWMutex
	lastPersonCount  int
}

func toPtr(init webrtc.ICECandidateInit) *webrtc.ICECandidateInit {
	return &init
}

func main() {
	var roomID string
	var videoInput string
	var enableAudio bool
	var enableFaceRecog bool
	var configPath string
	var configKeys arrayFlags
	var showHelp bool
	var loopVideo bool
	var audioFile string

	flag.StringVar(&roomID, "room", "", "Room ID to join")
	flag.StringVar(&videoInput, "video", "-", "Video input file (use - for stdin)")
	flag.BoolVar(&enableAudio, "audio", false, "Enable audio capture (requires separate audio source)")
	flag.BoolVar(&enableFaceRecog, "facerecog", true, "Enable face recognition")
	flag.StringVar(&configPath, "config", "", "Path to config JSON file")
	flag.StringVar(&configPath, "c", "", "Path to config JSON file (shorthand)")
	flag.Var(&configKeys, "C", "Set config value (key=value)")
	flag.Var(&configKeys, "config-key", "Set config value (key=value)")
	flag.BoolVar(&showHelp, "help", false, "Show help")
	flag.BoolVar(&showHelp, "h", false, "Show help (shorthand)")
	flag.BoolVar(&loopVideo, "loop", false, "Loop video when it ends")
	flag.StringVar(&audioFile, "audiofile", "", "Audio file to stream (OPUS/OGG or AAC)")
	flag.Parse()

	if showHelp {
		printHelp()
		return
	}

	// Load config file and apply overrides
	overrides := make(map[string]string)

	// Parse -C key=value flags
	for _, kv := range configKeys {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			log.Printf("Invalid config format: %s (expected key=value)", kv)
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		overrides[key] = value
	}

	if err := loadConfig(configPath, overrides); err != nil {
		log.Printf("Config error: %v", err)
	}

	// Now check for required room ID after config is loaded
	if roomID == "" {
		log.Fatal("Room ID is required. Use -room=YOUR_ROOM_ID")
	}

	enableAudio = enableAudio || audioFile != ""
	log.Printf("Starting laptop camera peer for room: %s", roomID)
	log.Printf("Video input: %s (use '-' for stdin)", videoInput)
	log.Printf("Audio enabled: %v", enableAudio)
	log.Printf("Face recognition enabled: %v", enableFaceRecog)
	log.Printf("Signaling server: %s", config.SignalingServerURL)

	peer := &CameraPeer{
		roomID:           roomID,
		viewers:          make(map[string]bool),
		stopCh:           make(chan struct{}),
		videoInput:       videoInput,
		audioEnabled:     enableAudio || audioFile != "",
		faceRecogEnabled: enableFaceRecog,
		loopVideo:        loopVideo || config.LoopVideo,
		audioFile:        audioFile,
		lastPersonCount:  -1,
	}

	// Handle shutdown gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutdown signal received")
		if peer.videoInput == "-" {
			log.Println("Stream ended - closing connection")
		}
		close(peer.stopCh)
	}()

	if err := peer.Run(); err != nil {
		log.Fatalf("Camera peer error: %v", err)
	}
}

// ConfigOverride holds CLI override values
type ConfigOverride struct {
	SignalingServerURL string
	StunServerURL      string
	CameraName         string
	PythonCompiler     string
	FaceRecogScript    string
}

// arrayFlags allows multiple -C flags
type arrayFlags []string

func (a *arrayFlags) String() string {
	return strings.Join(*a, ", ")
}

func (a *arrayFlags) Set(value string) error {
	*a = append(*a, value)
	return nil
}

// printHelp displays usage information
func printHelp() {
	fmt.Printf(`Usage: %s [options]

Required:
  -room string        Room ID to join

Options:
  -video string       Video input file (use - for stdin) (default "-")
  -audio              Enable audio capture (requires separate audio source) (default false)
  -audiofile string   Audio file to stream (OPUS/OGG or AAC)
  -loop               Loop video when it ends (default false)
  -facerecog          Enable face recognition (default true)
  -c, -config string  Path to config JSON file
  -C, -config-key     Set config value (can be used multiple times)
                      Format: -C key=value
                      Available keys: SignalingServerURL, StunServerURL, CameraName, PythonCompiler, FaceRecogScript, LoopVideo, AudioFile
  -h, -help           Show this help

Examples:
  %s -room=123
  %s -room=123 -c config.json
  %s -room=123 -C PythonCompiler=/usr/bin/python3 -C CameraName=office_cam
  %s -room=123 -video=test.h264 -audiofile=test.opus -loop
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

// Run starts the camera peer
func (cp *CameraPeer) Run() error {
	// Initialize face recognition if enabled
	if cp.faceRecogEnabled {
		if err := cp.initFaceRecognition(); err != nil {
			log.Printf("Failed to initialize face recognition: %v", err)
			cp.faceRecogEnabled = false
		}
		defer cp.stopFaceRecognition()
	}

	// Connect to signaling server
	if err := cp.connectSignaling(); err != nil {
		return fmt.Errorf("failed to connect to signaling server: %w", err)
	}
	defer cp.ws.Close()

	// Join room as camera
	if err := cp.joinRoom(); err != nil {
		return fmt.Errorf("failed to join room: %w", err)
	}

	// Start handling WebSocket messages
	go cp.handleSignaling()

	// Start video streaming
	go cp.streamVideo()

	// Start audio streaming if enabled
	if cp.audioEnabled && cp.audioFile != "" {
		go cp.streamAudio()
	}

	// Start face recognition processing
	if cp.faceRecogEnabled {
		go cp.processFaceRecognition()
	}

	// Wait for stop signal
	<-cp.stopCh
	log.Println("Camera peer shutting down")

	return nil
}

// loadConfig loads configuration from JSON file and applies CLI overrides
func loadConfig(configPath string, overrides map[string]string) error {
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf("failed to read config file: %w", err)
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("failed to parse config file: %w", err)
		}
		log.Printf("Loaded config from: %s", configPath)
	}

	// Apply CLI overrides
	for key, value := range overrides {
		switch key {
		case "SignalingServerURL":
			config.SignalingServerURL = value
			log.Printf("Override SignalingServerURL: %s", value)
		case "StunServerURL":
			config.StunServerURL = value
			log.Printf("Override StunServerURL: %s", value)
		case "CameraName":
			config.CameraName = value
			log.Printf("Override CameraName: %s", value)
		case "PythonCompiler":
			config.PythonCompiler = value
			log.Printf("Override PythonCompiler: %s", value)
		case "FaceRecogScript":
			config.FaceRecogScript = value
			log.Printf("Override FaceRecogScript: %s", value)
		case "LoopVideo":
			if value == "true" {
				config.LoopVideo = true
				log.Printf("Override LoopVideo: %s", value)
			}
		case "AudioFile":
			config.AudioFile = value
			log.Printf("Override AudioFile: %s", value)
		}
	}

	return nil
}

// getICEConfiguration returns WebRTC ICE configuration from config
func getICEConfiguration() webrtc.Configuration {
	var iceServers []webrtc.ICEServer

	for _, server := range config.ICECredentials {
		// Only add TURN servers with credentials if they're provided
		if server.Username != "" && server.Credential != "" {
			iceServers = append(iceServers, webrtc.ICEServer{
				URLs:       []string{server.URLs},
				Username:   server.Username,
				Credential: server.Credential,
			})
		} else {
			// Add STUN servers without credentials
			iceServers = append(iceServers, webrtc.ICEServer{
				URLs: []string{server.URLs},
			})
		}
	}

	// Always add the default STUN server if no servers configured
	if len(iceServers) == 0 {
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs: []string{config.StunServerURL},
		})
	}

	return webrtc.Configuration{ICEServers: iceServers}
}

// initFaceRecognition starts the Python face recognition wrapper with stderr capture
func (cp *CameraPeer) initFaceRecognition() error {
	cmd := exec.Command(config.PythonCompiler, config.FaceRecogScript)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Capture stderr in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("[Python stderr] %s", line)
		}
		if err := scanner.Err(); err != nil {
			// Don't log read errors that occur when process is terminated
			if !strings.Contains(err.Error(), "file already closed") &&
				!strings.Contains(err.Error(), "closed pipe") {
				log.Printf("[Python stderr] read error: %v", err)
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start face recognition '%s': %w", config.PythonCompiler, err)
	}

	cp.faceRecogCmd = cmd
	cp.faceRecogStdin = stdin
	cp.faceRecogStdout = bufio.NewScanner(stdout)

	// Wait for ready signal with timeout
	done := make(chan bool, 1)
	var initError error

	// Monitor process exit
	processDone := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		processDone <- err
	}()

	go func() {
		if cp.faceRecogStdout.Scan() {
			var response map[string]interface{}
			if err := json.Unmarshal(cp.faceRecogStdout.Bytes(), &response); err != nil {
				initError = fmt.Errorf("failed to parse ready signal: %w", err)
				done <- false
				return
			}
			if status, ok := response["status"].(string); ok {
				if status == "ready" {
					log.Printf("Face recognition status: %s", status)
					done <- true
				} else if status == "error" {
					if msg, ok := response["message"].(string); ok {
						initError = fmt.Errorf("face recognition init failed: %s", msg)
					} else {
						initError = fmt.Errorf("face recognition init failed")
					}
					done <- false
				}
			}
		} else {
			initError = fmt.Errorf("no response from face recognition process")
			done <- false
		}
	}()

	// Wait for either success, timeout, or process exit
	select {
	case success := <-done:
		if !success {
			cp.stopFaceRecognition()
			return initError
		}
	case err := <-processDone:
		if err != nil {
			return fmt.Errorf("face recognition process exited: %w", err)
		}
		return fmt.Errorf("face recognition process exited unexpectedly")
	case <-time.After(30 * time.Second):
		cp.stopFaceRecognition()
		return fmt.Errorf("timeout waiting for face recognition to initialize")
	}

	log.Println("Face recognition initialized successfully")
	return nil
}

// stopFaceRecognition stops the Python wrapper
func (cp *CameraPeer) stopFaceRecognition() {
	if cp.faceRecogStdin != nil {
		cp.faceRecogStdin.Close()
		cp.faceRecogStdin = nil
	}
	if cp.faceRecogCmd != nil && cp.faceRecogCmd.Process != nil {
		log.Printf("Stopping face recognition process (PID: %d)", cp.faceRecogCmd.Process.Pid)
		cp.faceRecogCmd.Process.Kill()
		// Wait for process to actually exit, but with timeout
		done := make(chan error, 1)
		go func() {
			done <- cp.faceRecogCmd.Wait()
		}()
		select {
		case <-done:
			log.Println("Face recognition process stopped")
		case <-time.After(5 * time.Second):
			log.Println("Face recognition process did not exit gracefully, force cleanup")
		}
		cp.faceRecogCmd = nil
	}
}

// processFaceRecognition continuously processes frames for face recognition
func (cp *CameraPeer) processFaceRecognition() {
	ticker := time.NewTicker(FaceRecogFrameInterval)
	defer ticker.Stop()

	// Monitor if Python process is still alive
	processDone := make(chan error, 1)
	if cp.faceRecogCmd != nil && cp.faceRecogCmd.Process != nil {
		go func() {
			err := cp.faceRecogCmd.Wait()
			processDone <- err
		}()
	}

	for {
		select {
		case <-cp.stopCh:
			return
		case <-ticker.C:
			cp.processSingleFrame()
		case err := <-processDone:
			if err != nil {
				log.Printf("Face recognition process died: %v", err)
			} else {
				log.Println("Face recognition process exited normally")
			}
			log.Println("Disabling face recognition due to process exit")
			cp.faceRecogEnabled = false
			return
		}
	}
}

// processSingleFrame sends one frame to face recognition and handles result
func (cp *CameraPeer) processSingleFrame() {
	cp.lastFrameMu.RLock()
	frameData := cp.lastFrame
	cp.lastFrameMu.RUnlock()

	if len(frameData) == 0 {
		return
	}

	// Encode frame to base64
	base64Frame := base64.StdEncoding.EncodeToString(frameData)

	request := FaceRecogRequest{
		Frame:     base64Frame,
		Timestamp: time.Now().UnixMilli(),
	}

	requestJSON, err := json.Marshal(request)
	if err != nil {
		log.Printf("Failed to marshal face recog request: %v", err)
		return
	}

	cp.faceRecogMu.Lock()
	defer cp.faceRecogMu.Unlock()

	// Send to Python wrapper
	if _, err := cp.faceRecogStdin.Write(append(requestJSON, '\n')); err != nil {
		log.Printf("Failed to send frame to face recognition: %v", err)
		return
	}

	// Read response
	if !cp.faceRecogStdout.Scan() {
		return
	}

	var response FaceRecogResponse
	if err := json.Unmarshal(cp.faceRecogStdout.Bytes(), &response); err != nil {
		log.Printf("Failed to parse face recog response: %v", err)
		return
	}

	if response.Error != "" {
		log.Printf("Face recognition error: %s", response.Error)
		return
	}

	currentCount := len(response.Persons)
	if currentCount != cp.lastPersonCount {
		if currentCount == 0 {
			log.Println("Face recognition: no faces detected")
		} else {
			log.Printf("Face recognition: detected %d person(s)", currentCount)
			cp.logFaceRecogPerformance(response)
		}
		cp.lastPersonCount = currentCount
	}

	// Always send identification data to signaling server when persons detected
	if currentCount > 0 {
		cp.sendIdentificationData(response.Persons, response.Timestamp)
	}
}

// logFaceRecogPerformance logs face recognition timing metrics
func (cp *CameraPeer) logFaceRecogPerformance(response FaceRecogResponse) {
	if response.DetTime > 0 || response.RecTime > 0 {
		log.Printf("[FaceRecog] Detection: %.2fms | Recognition: %.2fms | Persons: %d",
			response.DetTime, response.RecTime, len(response.Persons))
	}
}

// sendIdentificationData sends identified persons to viewers via signaling server
func (cp *CameraPeer) sendIdentificationData(persons []PersonInfo, timestamp int64) {
	msg := SignalingMessage{
		Type:      "identification_data",
		Persons:   persons,
		Timestamp: timestamp,
	}

	if err := cp.ws.WriteJSON(msg); err != nil {
		log.Printf("Failed to send identification data: %v", err)
	}
}

// connectSignaling establishes WebSocket connection to signaling server
func (cp *CameraPeer) connectSignaling() error {
	log.Printf("Connecting to signaling server: %s", config.SignalingServerURL)

	ws, _, err := websocket.DefaultDialer.Dial(config.SignalingServerURL, nil)
	if err != nil {
		return err
	}

	cp.ws = ws
	log.Println("Connected to signaling server")
	return nil
}

// joinRoom sends join message to signaling server
func (cp *CameraPeer) joinRoom() error {
	msg := SignalingMessage{
		Type:   "join",
		RoomID: cp.roomID,
		Name:   config.CameraName,
		IsCam:  true,
	}

	return cp.ws.WriteJSON(msg)
}

// createPeerConnection creates WebRTC peer connection
func (cp *CameraPeer) createPeerConnection() error {
	iceConfig := getICEConfiguration()

	pc, err := webrtc.NewPeerConnection(iceConfig)
	if err != nil {
		return err
	}

	cp.pc = pc

	// Create video track (H.264)
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video",
		"camera-video",
	)
	if err != nil {
		return err
	}
	cp.videoTrack = videoTrack

	if _, err = pc.AddTrack(videoTrack); err != nil {
		return err
	}

	// Create audio track if enabled
	if cp.audioEnabled && cp.audioFile != "" {
		audioTrack, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
			"audio",
			"camera-audio",
		)
		if err != nil {
			return err
		}
		cp.audioTrack = audioTrack

		if _, err = pc.AddTrack(audioTrack); err != nil {
			return err
		}
	}

	// Handle incoming tracks
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		log.Printf("Received track: %s (%s)", track.ID(), track.Kind().String())
	})

	// Handle ICE candidates
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}

		cp.viewersMu.RLock()
		viewers := make([]string, 0, len(cp.viewers))
		for viewerID := range cp.viewers {
			viewers = append(viewers, viewerID)
		}
		cp.viewersMu.RUnlock()

		for _, viewerID := range viewers {
			msg := SignalingMessage{
				Type:         "candidate",
				TargetUserID: viewerID,
				Candidate:    toPtr(candidate.ToJSON()),
			}
			if err := cp.ws.WriteJSON(msg); err != nil {
				log.Printf("Failed to send ICE candidate: %v", err)
			}
		}
	})

	// Handle connection state changes
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Peer connection state: %s", state.String())
	})

	return nil
}

// handleSignaling processes WebSocket messages
func (cp *CameraPeer) handleSignaling() {
	for {
		select {
		case <-cp.stopCh:
			return
		default:
		}

		var msg SignalingMessage
		if err := cp.ws.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			return
		}

		switch msg.Type {
		case "existing_users":
			cp.handleExistingUsers(msg.Users)
		case "user-joined":
			cp.handleUserJoined(msg.UserID, msg.Name, msg.IsCam)
		case "user-left":
			cp.handleUserLeft(msg.UserID)
		case "answer":
			cp.handleAnswer(msg)
		case "candidate":
			cp.handleCandidate(msg)
		case "room-not-found":
			log.Printf("ERROR: Room '%s' not found. %s", cp.roomID, msg.Message)
			log.Printf("Please check if the room ID is correct or create the room first.")
			close(cp.stopCh)
			return
		default:
			log.Printf("Unknown message type: %s", msg.Type)
		}
	}
}

// handleExistingUsers processes existing users when joining
func (cp *CameraPeer) handleExistingUsers(users []UserInfo) {
	// Create peer connection now that we know the room exists
	if err := cp.createPeerConnection(); err != nil {
		log.Printf("Failed to create peer connection: %v", err)
		close(cp.stopCh)
		return
	}

	if len(users) == 0 {
		log.Printf("Joined empty room, waiting for viewers...")
	} else {
		log.Printf("Found %d users in room", len(users))
	}

	for _, user := range users {
		if !user.IsCam {
			cp.viewersMu.Lock()
			cp.viewers[user.ID] = true
			cp.viewersMu.Unlock()
			log.Printf("Found existing viewer: %s (%s)", user.Name, user.ID)

			go cp.sendOffer(user.ID)
		}
	}
}

// handleUserJoined handles new user joining
func (cp *CameraPeer) handleUserJoined(userID, name string, isCam bool) {
	log.Printf("User joined: %s (%s), isCam: %v", name, userID, isCam)

	if !isCam {
		cp.viewersMu.Lock()
		cp.viewers[userID] = true
		cp.viewersMu.Unlock()
		go cp.sendOffer(userID)
	}
}

// handleUserLeft handles user leaving
func (cp *CameraPeer) handleUserLeft(userID string) {
	log.Printf("User left: %s", userID)
	cp.viewersMu.Lock()
	delete(cp.viewers, userID)
	cp.viewersMu.Unlock()
}

// sendOffer creates and sends WebRTC offer to a viewer
func (cp *CameraPeer) sendOffer(viewerID string) {
	log.Printf("Sending offer to viewer: %s", viewerID)

	offer, err := cp.pc.CreateOffer(nil)
	if err != nil {
		log.Printf("Failed to create offer: %v", err)
		return
	}

	if err := cp.pc.SetLocalDescription(offer); err != nil {
		log.Printf("Failed to set local description: %v", err)
		return
	}

	msg := SignalingMessage{
		Type:         "offer",
		TargetUserID: viewerID,
		Offer:        &offer,
	}

	if err := cp.ws.WriteJSON(msg); err != nil {
		log.Printf("Failed to send offer: %v", err)
	}
}

// handleAnswer processes answer from viewer
func (cp *CameraPeer) handleAnswer(msg SignalingMessage) {
	log.Printf("Received answer from viewer")

	if msg.Answer == nil {
		log.Println("Answer is nil")
		return
	}

	if err := cp.pc.SetRemoteDescription(*msg.Answer); err != nil {
		log.Printf("Failed to set remote description: %v", err)
	}
}

// handleCandidate processes ICE candidate from viewer
func (cp *CameraPeer) handleCandidate(msg SignalingMessage) {
	if msg.Candidate == nil {
		return
	}

	if err := cp.pc.AddICECandidate(*msg.Candidate); err != nil {
		log.Printf("Failed to add ICE candidate: %v", err)
	}
}

// streamVideo reads H.264 from file/pipe and sends via WebRTC
func (cp *CameraPeer) streamVideo() {
	var reader io.Reader
	var closer func()

	if cp.videoInput == "-" {
		reader = os.Stdin
		closer = func() {}
		log.Println("Reading video from stdin")
	} else {
		// Check if file exists before opening
		absPath, _ := filepath.Abs(cp.videoInput)
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			log.Fatalf("Video file not found: %s (absolute path: %s)", cp.videoInput, absPath)
			return
		}
		file, err := os.Open(absPath)
		if err != nil {
			log.Fatalf("Failed to open video file (%s): %v", absPath, err)
			return
		}
		reader = file
		closer = func() { file.Close() }
		log.Printf("Reading video from file: %s", cp.videoInput)
	}
	defer closer()

	cp.readH264NALUnits(reader)
}

// readH264NALUnits reads H.264 NAL units and sends them to WebRTC
func (cp *CameraPeer) readH264NALUnits(reader io.Reader) {
	// Use Pion's H264Reader for proper Annex B parsing
	h264Reader, err := h264reader.NewReader(reader)
	if err != nil {
		log.Printf("Failed to create H264 reader: %v", err)
		return
	}

	frameCount := 0
	loopCount := 0
	startTime := time.Now()
	var sps []byte
	var pps []byte
	var lastIDR []byte
	flushed := false

	// Frame pacing for 30fps streaming
	const frameInterval = time.Second / 30
	lastFrameTime := time.Now()

	for {
		select {
		case <-cp.stopCh:
			return
		default:
		}

		nal, err := h264Reader.NextNAL()
		if err != nil {
			if err == io.EOF {
				log.Printf("EOF reached after %d frames (%.1fs elapsed)", frameCount, time.Since(startTime).Seconds())

				if cp.loopVideo && cp.videoInput == "-" {
					log.Printf("Error: cannot loop a constant video stream (stdin)")
					return
				}
				if cp.loopVideo && cp.videoInput != "-" {
					// Reopen file for looping
					file, err := os.Open(cp.videoInput)
					if err != nil {
						log.Printf("Failed to reopen video file for looping: %v", err)
						return
					}
					h264Reader, err = h264reader.NewReader(file)
					if err != nil {
						log.Printf("Failed to create H264 reader for loop: %v", err)
						return
					}
					loopCount++
					log.Printf("Video loop #%d - restarted playback (frames: %d)", loopCount, frameCount)
					frameCount = 0
					startTime = time.Now()
					flushed = false
					lastFrameTime = time.Now()
					continue
				} else {
					log.Printf("Video stream ended (total frames: %d, duration: %.1fs)", frameCount, time.Since(startTime).Seconds())
					log.Println("Stream ended - closing connection")
					return
				}
			}
			log.Printf("Error reading NAL unit: %v", err)
			continue
		}

		if nal == nil {
			continue
		}

		// Get NAL data (h264reader returns raw NAL without start codes)
		data := nal.Data
		if len(data) == 0 {
			continue
		}

		nalType := data[0] & 0x1F

		// Log first few frames for debugging
		if frameCount < 10 || frameCount%100 == 0 {
			truncated := data
			if len(truncated) > 16 {
				truncated = data[:16]
			}
			log.Printf("Frame %d: NAL type=%d, %d bytes, first 16 bytes: %x", frameCount, nalType, len(data), truncated)
		}

		// Use raw NAL data (no start code prefix)
		nalData := make([]byte, len(data))
		copy(nalData, data)

		// Handle SPS: cache if track not ready, send immediately if ready
		if nalType == 7 {
			if cp.videoTrack == nil {
				sps = make([]byte, len(nalData))
				copy(sps, nalData)
			} else {
				cp.videoTrack.WriteSample(media.Sample{Data: nalData, Duration: 0})
			}
			continue
		}

		// Handle PPS: cache if track not ready, send immediately if ready
		if nalType == 8 {
			if cp.videoTrack == nil {
				pps = make([]byte, len(nalData))
				copy(pps, nalData)
			} else {
				cp.videoTrack.WriteSample(media.Sample{Data: nalData, Duration: 0})
			}
			continue
		}

		// Cache IDR for possible decoder init if track not ready yet
		if nalType == 5 {
			lastIDR = make([]byte, len(nalData))
			copy(lastIDR, nalData)
		}

		// Store I-frames for face recognition (NAL type 5 = IDR)
		if cp.faceRecogEnabled && nalType == 5 {
			cp.lastFrameMu.Lock()
			cp.lastFrame = make([]byte, len(nalData))
			copy(cp.lastFrame, nalData)
			cp.lastFrameMu.Unlock()
		}

		// Skip non-parameter-set frames if video track not ready yet
		if cp.videoTrack == nil {
			continue
		}

		// On first write after track becomes ready, flush cached init NALs
		if !flushed {
			if len(sps) > 0 {
				log.Println("Flushing cached SPS to video track")
				cp.videoTrack.WriteSample(media.Sample{Data: sps, Duration: 0})
				sps = nil
			}
			if len(pps) > 0 {
				log.Println("Flushing cached PPS to video track")
				cp.videoTrack.WriteSample(media.Sample{Data: pps, Duration: 0})
				pps = nil
			}
			// If current frame is not an IDR but we have a cached one, send it first
			if len(lastIDR) > 0 && nalType != 5 {
				log.Println("Flushing cached IDR to video track")
				cp.videoTrack.WriteSample(media.Sample{Data: lastIDR, Duration: time.Second / 30})
				lastIDR = nil
			}
			flushed = true
		}

		// Write current NAL unit with start code prefix
		// WebRTC Duration field handles proper RTP timing
		if err := cp.videoTrack.WriteSample(media.Sample{
			Data:     nalData,
			Duration: frameInterval,
		}); err != nil {
			log.Printf("Failed to write video sample: %v", err)
		}

		frameCount++

		// Pace video frames (IDR and P-frames) at 30fps
		// Parameter sets (SPS/PPS) are not paced
		if nalType == 5 || nalType == 1 {
			elapsed := time.Since(lastFrameTime)
			if sleepTime := frameInterval - elapsed; sleepTime > 0 {
				time.Sleep(sleepTime)
			}
			lastFrameTime = time.Now()
		}
	}
}

// streamAudio reads Opus audio from Ogg container and sends via WebRTC
func (cp *CameraPeer) streamAudio() {
	if cp.audioFile == "" || cp.audioTrack == nil {
		return
	}

	file, err := os.Open(cp.audioFile)
	if err != nil {
		log.Printf("Failed to open audio file: %v", err)
		return
	}
	defer file.Close()

	log.Printf("Streaming Opus audio from: %s", cp.audioFile)

	// Create the Pion Ogg reader
	ogg, _, err := oggreader.NewWith(file)
	if err != nil {
		log.Printf("Failed to parse Ogg container: %v", err)
		return
	}

	// Read and send Opus pages as fast as possible
	// WebRTC Duration field handles RTP timing
	pageCount := 0
	for {
		select {
		case <-cp.stopCh:
			return
		default:
		}

		// Read exactly one Opus page from the container
		pageData, _, err := ogg.ParseNextPage()
		if err != nil {
			if err == io.EOF && cp.loopVideo {
				file.Seek(0, 0)
				// Re-initialize the reader for the loop
				ogg, _, _ = oggreader.NewWith(file)
				log.Printf("Audio loop restarted")
				continue
			}
			if err != io.EOF {
				log.Printf("Error reading audio page: %v", err)
			}
			return
		}

		if pageCount < 3 {
			log.Printf("Audio page %d: %d bytes", pageCount, len(pageData))
		}
		pageCount++

		if err := cp.audioTrack.WriteSample(media.Sample{
			Data:     pageData,
			Duration: time.Millisecond * 20,
		}); err != nil {
			log.Printf("Failed to write audio sample: %v", err)
		}
	}
}

// isIFrame checks if H.264 NAL unit is an I-frame (IDR slice)
func isIFrame(data []byte) bool {
	if len(data) < 5 {
		return false
	}
	// Find NAL unit start
	offset := 0
	if data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		offset = 4
	} else if data[0] == 0 && data[1] == 0 && data[2] == 1 {
		offset = 3
	} else {
		return false
	}
	// NAL type 5 = IDR slice (I-frame)
	if offset < len(data) {
		nalType := data[offset] & 0x1F
		return nalType == 5
	}
	return false
}
