// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

// play-from-disk demonstrates how to send video and/or audio to your browser from files saved to disk.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
)

// Default configuration - can be overridden via config.json or CLI
const (
	DefaultSignalingServerURL = "ws://192.168.56.1:3000"
	DefaultStunServerURL      = "stun:stun.l.google.com:19302"
	DefaultCameraName         = "stream_cam"
	DefaultVideoDevice        = "/dev/video0"
	DefaultVideoCodec         = "libvpx" // VP8 for IVF
	DefaultVideoWidth         = 1280
	DefaultVideoHeight        = 720
	DefaultVideoFPS           = 30
	DefaultVideoBitrate       = "1M"
	DefaultFaceRecogFormat    = "yuv420p"
	DefaultFaceRecogPipe      = "face_recog_pipe"
)

// Config holds all configuration options
type Config struct {
	SignalingServerURL string      `json:"signalingServerUrl"`
	StunServerURL      string      `json:"stunServerUrl"`
	CameraName         string      `json:"cameraName"`
	VideoDevice        string      `json:"videoDevice"`
	VideoCodec         string      `json:"videoCodec"`
	VideoWidth         int         `json:"videoWidth"`
	VideoHeight        int         `json:"videoHeight"`
	VideoFPS           int         `json:"videoFps"`
	VideoBitrate       string      `json:"videoBitrate"`
	FaceRecogEnabled   bool        `json:"faceRecogEnabled"`
	FaceRecogFormat    string      `json:"faceRecogFormat"`
	FaceRecogPipe      string      `json:"faceRecogPipe"`
	TestPipeOnly       bool        `json:"testPipeOnly"`
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
	VideoDevice:        DefaultVideoDevice,
	VideoCodec:         DefaultVideoCodec,
	VideoWidth:         DefaultVideoWidth,
	VideoHeight:        DefaultVideoHeight,
	VideoFPS:           DefaultVideoFPS,
	VideoBitrate:       DefaultVideoBitrate,
	FaceRecogEnabled:   false,
	FaceRecogFormat:    DefaultFaceRecogFormat,
	FaceRecogPipe:      DefaultFaceRecogPipe,
	TestPipeOnly:       false,
	ICECredentials: []ICEServer{
		{URLs: "stun:stun.l.google.com:19302"},
		{URLs: "stun:stun1.l.google.com:19302"},
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
	ffmpegCmd        *exec.Cmd
	ivfPipe          io.ReadCloser
	framePipe        io.ReadCloser
	lastFrame        []byte
	lastFrameMu      sync.RWMutex
	lastPersonCount  int
	shuttingDown     bool
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

func toPtr(init webrtc.ICECandidateInit) *webrtc.ICECandidateInit {
	return &init
}

// getICEConfiguration returns WebRTC ICE configuration from config
func getICEConfiguration() webrtc.Configuration {
	var iceServers []webrtc.ICEServer

	for _, server := range config.ICECredentials {
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs:       []string{server.URLs},
			Username:   server.Username,
			Credential: server.Credential,
		})
	}

	// Always add the default STUN server if no servers configured
	if len(iceServers) == 0 {
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs: []string{config.StunServerURL},
		})
	}

	return webrtc.Configuration{ICEServers: iceServers}
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
		case "VideoDevice":
			config.VideoDevice = value
			log.Printf("Override VideoDevice: %s", value)
		case "VideoCodec":
			config.VideoCodec = value
			log.Printf("Override VideoCodec: %s", value)
		case "VideoWidth":
			if width, err := strconv.Atoi(value); err == nil {
				config.VideoWidth = width
				log.Printf("Override VideoWidth: %d", width)
			}
		case "VideoHeight":
			if height, err := strconv.Atoi(value); err == nil {
				config.VideoHeight = height
				log.Printf("Override VideoHeight: %d", height)
			}
		case "VideoFPS":
			if fps, err := strconv.Atoi(value); err == nil {
				config.VideoFPS = fps
				log.Printf("Override VideoFPS: %d", fps)
			}
		case "VideoBitrate":
			config.VideoBitrate = value
			log.Printf("Override VideoBitrate: %s", value)
		case "FaceRecogEnabled":
			if enabled, err := strconv.ParseBool(value); err == nil {
				config.FaceRecogEnabled = enabled
				log.Printf("Override FaceRecogEnabled: %v", enabled)
			}
		case "FaceRecogFormat":
			config.FaceRecogFormat = value
			log.Printf("Override FaceRecogFormat: %s", value)
		case "FaceRecogPipe":
			config.FaceRecogPipe = value
			log.Printf("Override FaceRecogPipe: %s", value)
		case "TestPipeOnly":
			if testOnly, err := strconv.ParseBool(value); err == nil {
				config.TestPipeOnly = testOnly
				log.Printf("Override TestPipeOnly: %v", testOnly)
			}
		}
	}

	return nil
}

// printHelp displays usage information
func printHelp() {
	fmt.Printf(`Usage: %s [options]

Required:
  -room string        Room ID to join

Options:
  -config string      Path to config JSON file
  -c, -config string Path to config JSON file (shorthand)
  -C, -config-key   Set config value (can be used multiple times)
                      Format: -C key=value
                      Available keys: SignalingServerURL, StunServerURL, CameraName, VideoDevice, VideoCodec, VideoWidth, VideoHeight, VideoFPS, VideoBitrate, FaceRecogEnabled, FaceRecogFormat, FaceRecogPipe, TestPipeOnly
  -h, -help         Show this help

Examples:
  %s -room=123
  %s -room=123 -c config.json
  %s -room=123 -C VideoDevice=/dev/video0 -C FaceRecogEnabled=true
`, os.Args[0], os.Args[0], os.Args[0])
}

func main() {
	var roomID string
	var configPath string
	var configKeys arrayFlags
	var showHelp bool

	flag.StringVar(&roomID, "room", "", "Room ID to join")
	flag.StringVar(&configPath, "config", "", "Path to config JSON file")
	flag.StringVar(&configPath, "c", "", "Path to config JSON file (shorthand)")
	flag.Var(&configKeys, "C", "Set config value (key=value)")
	flag.Var(&configKeys, "config-key", "Set config value (key=value)")
	flag.BoolVar(&showHelp, "help", false, "Show help")
	flag.BoolVar(&showHelp, "h", false, "Show help (shorthand)")
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

	log.Printf("Starting camera stream for room: %s", roomID)
	log.Printf("Video device: %s", config.VideoDevice)
	log.Printf("Face recognition enabled: %v", config.FaceRecogEnabled)
	log.Printf("Signaling server: %s", config.SignalingServerURL)

	peer := &CameraPeer{
		roomID:           roomID,
		viewers:          make(map[string]bool),
		stopCh:           make(chan struct{}),
		videoInput:       config.VideoDevice,
		audioEnabled:     false, // TODO: add audio support
		faceRecogEnabled: config.FaceRecogEnabled,
		lastPersonCount:  -1,
	}

	// Handle shutdown gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutdown signal received")

		// Set shutdown flag to prevent restarts
		peer.shuttingDown = true

		// Terminate FFmpeg immediately on signal
		if peer.ffmpegCmd != nil && peer.ffmpegCmd.Process != nil {
			log.Println("Terminating FFmpeg process...")
			peer.ffmpegCmd.Process.Kill()
			peer.ffmpegCmd.Wait()
		}

		close(peer.stopCh)
	}()

	if err := peer.Run(); err != nil {
		log.Fatalf("Camera peer error: %v", err)
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

	// Create video track (VP8)
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
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
			// Terminate FFmpeg before exiting
			if cp.ffmpegCmd != nil && cp.ffmpegCmd.Process != nil {
				log.Println("Terminating FFmpeg process...")
				cp.ffmpegCmd.Process.Kill()
				cp.ffmpegCmd.Wait()
			}
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

// Run starts the camera peer with FFmpeg dual pipe output
func (cp *CameraPeer) Run() error {
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

	// Start FFmpeg with dual pipe output
	if err := cp.startDualFFmpeg(); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %w", err)
	}

	// Wait for stop signal
	<-cp.stopCh
	log.Println("Camera peer shutting down")

	// Cleanup temporary files
	if cp.faceRecogEnabled {
		// Ensure FFmpeg is terminated before cleanup
		if cp.ffmpegCmd != nil && cp.ffmpegCmd.Process != nil {
			log.Println("Terminating FFmpeg process...")
			cp.ffmpegCmd.Process.Kill()
			cp.ffmpegCmd.Wait()
		}

		// Give FFmpeg more time to release the file
		time.Sleep(1 * time.Second)
		tempFile := "face_recog_frames.raw"
		if err := os.Remove(tempFile); err != nil && !os.IsNotExist(err) {
			log.Printf("Failed to remove temp file %s: %v", tempFile, err)
		} else {
			log.Printf("Successfully removed temp file %s", tempFile)
		}
	}

	return nil
}

// startDualFFmpeg launches FFmpeg with tee filter for dual output
func (cp *CameraPeer) startDualFFmpeg() error {
	log.Println("Starting FFmpeg with dual pipe output...")

	// Build FFmpeg command with tee filter
	var ffmpegArgs []string

	if runtime.GOOS == "windows" {
		if cp.faceRecogEnabled {
			// Windows: Use file for face recognition (fallback from named pipe)
			tempFile := "face_recog_frames.raw"
			ffmpegArgs = []string{
				"-f", "dshow",
				"-i", fmt.Sprintf("video=%s", cp.videoInput),
				"-rtbufsize", "64M",
				"-thread_queue_size", "1024",
				"-filter_complex", fmt.Sprintf(
					"split=2[v1][v2];[v1]copy[v1out];[v2]scale=%d:%d:flags=fast_bilinear,format=%s,fps=5[v2out]",
					320, 320, cp.getFaceFormat()),
				"-map", "[v1out]",
				"-c:v", config.VideoCodec,
				"-b:v", config.VideoBitrate,
				"-maxrate", "1.5M",
				"-bufsize", "2M",
				"-g", "30",
				"-keyint_min", "30",
				"-f", "ivf", "pipe:1",
				"-map", "[v2out]",
				"-f", "rawvideo", tempFile,
			}
			log.Printf("Using file for face recognition: %s", tempFile)
			// Start frame reader that reads from the file
			go cp.readFrameFile(tempFile)
		} else {
			// Windows: Only output IVF for WebRTC (no face recognition)
			ffmpegArgs = []string{
				"-f", "dshow",
				"-i", fmt.Sprintf("video=%s", cp.videoInput),
				"-rtbufsize", "64M",
				"-thread_queue_size", "1024",
				"-c:v", config.VideoCodec,
				"-b:v", config.VideoBitrate,
				"-maxrate", "1.5M",
				"-bufsize", "2M",
				"-g", "30",
				"-keyint_min", "30",
				"-f", "ivf", "pipe:1",
			}
		}
	} else {
		// Linux/Unix: Use file for face recognition (same approach as Windows)
		if cp.faceRecogEnabled {
			tempFile := "face_recog_frames.raw"
			ffmpegArgs = []string{
				"-f", "v4l2",
				"-i", cp.videoInput,
				"-rtbufsize", "64M",
				"-thread_queue_size", "1024",
				"-filter_complex", fmt.Sprintf(
					"split=2[v1][v2];[v1]copy[v1out];[v2]scale=%d:%d:flags=fast_bilinear,format=%s,fps=5[v2out]",
					320, 320, cp.getFaceFormat()),
				"-map", "[v1out]",
				"-c:v", config.VideoCodec,
				"-b:v", config.VideoBitrate,
				"-maxrate", "1.5M",
				"-bufsize", "2M",
				"-g", "30",
				"-keyint_min", "30",
				"-f", "ivf", "pipe:1",
				"-map", "[v2out]",
				"-f", "rawvideo", tempFile,
			}
			log.Printf("Using file for face recognition: %s", tempFile)
			// Start frame reader that reads from the file
			go cp.readFrameFile(tempFile)
		} else {
			// Linux: Only output IVF for WebRTC (no face recognition)
			ffmpegArgs = []string{
				"-f", "v4l2",
				"-i", cp.videoInput,
				"-rtbufsize", "64M",
				"-thread_queue_size", "1024",
				"-c:v", config.VideoCodec,
				"-b:v", config.VideoBitrate,
				"-maxrate", "1.5M",
				"-bufsize", "2M",
				"-g", "30",
				"-keyint_min", "30",
				"-f", "ivf", "pipe:1",
			}
		}
	}

	cp.ffmpegCmd = exec.Command("ffmpeg", ffmpegArgs...)

	// Get stdout pipe for IVF
	ivfPipe, err := cp.ffmpegCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create IVF pipe: %w", err)
	}
	cp.ivfPipe = ivfPipe

	// Get stderr pipe
	stderrPipe, err := cp.ffmpegCmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			log.Printf("[FFmpeg stderr] %s", scanner.Text())
		}
	}()

	// Start FFmpeg
	if err := cp.ffmpegCmd.Start(); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %w", err)
	}

	log.Printf("FFmpeg started with args: %v", ffmpegArgs)

	// Start reading IVF from pipe for WebRTC
	go cp.readIVFPipe()

	return nil
}

// getFaceFormat returns FFmpeg format string for face recognition
func (cp *CameraPeer) getFaceFormat() string {
	switch config.FaceRecogFormat {
	case "gray":
		return "gray"
	case "rgb24":
		return "rgb24"
	case "yuv420p":
		return "yuv420p"
	default:
		return "yuv420p"
	}
}

// readIVFPipe reads IVF from FFmpeg stdout and sends to WebRTC
func (cp *CameraPeer) readIVFPipe() {
	log.Println("Starting IVF pipe reader for WebRTC")

	// Wait for peer connection to be established
	// This will be set when we receive existing_users or user-joined events

	// Create IVF reader from pipe
	ivf, _, err := ivfreader.NewWith(cp.ivfPipe)
	if err != nil {
		log.Printf("Failed to create IVF reader: %v", err)
		return
	}

	frameCount := 0
	startTime := time.Now()

	for {
		frame, _, err := ivf.ParseNextFrame()
		if err != nil {
			if err == io.EOF {
				log.Println("FFmpeg ended - restarting...")
				// Restart FFmpeg if it crashes
				cp.restartFFmpeg()
				continue
			}
			log.Printf("IVF parse error: %v", err)
			continue
		}

		// Send to WebRTC track when ready
		if cp.videoTrack != nil {
			if err := cp.videoTrack.WriteSample(media.Sample{
				Data:     frame,
				Duration: time.Second / time.Duration(config.VideoFPS),
			}); err != nil {
				log.Printf("Failed to write video sample: %v", err)
			}
		}

		frameCount++
		if frameCount < 10 || frameCount%100 == 0 {
			log.Printf("Video: sent frame %d (%.1fs elapsed)", frameCount, time.Since(startTime).Seconds())
		}
	}
}

// readFrameFile reads raw frames from file for face recognition (Windows fallback)
func (cp *CameraPeer) readFrameFile(filename string) {
	log.Println("Starting frame file reader for face recognition")

	frameSize := 320 * 320 * 3 // Default RGB24
	switch config.FaceRecogFormat {
	case "gray":
		frameSize = 320 * 320 // Grayscale
	case "rgb24":
		frameSize = 320 * 320 * 3 // RGB24
	case "yuv420p":
		frameSize = 320 * 320 * 3 / 2 // YUV420 (1.5 bytes per pixel)
	}

	frameCount := 0

	// Wait for file to be created and start reading
	for {
		select {
		case <-cp.stopCh:
			return
		default:
		}

		// Open file and read from end (like tail -f)
		file, err := os.Open(filename)
		if err != nil {
			if os.IsNotExist(err) {
				// File not created yet, wait a bit
				time.Sleep(100 * time.Millisecond)
				continue
			}
			log.Printf("Failed to open frame file: %v", err)
			return
		}

		// Seek to end of file
		stat, err := file.Stat()
		if err != nil {
			file.Close()
			log.Printf("Failed to stat file: %v", err)
			return
		}

		offset := stat.Size()
		frameData := make([]byte, frameSize)

		for {
			// Read from current position
			n, err := file.ReadAt(frameData, offset)
			if err != nil && err != io.EOF {
				log.Printf("Frame file read error: %v", err)
				break
			}

			if n == frameSize {
				// Store frame for face recognition (make a copy to avoid race condition)
				frameCopy := make([]byte, frameSize)
				copy(frameCopy, frameData)
				cp.lastFrameMu.Lock()
				cp.lastFrame = frameCopy
				cp.lastFrameMu.Unlock()

				frameCount++
				if frameCount < 10 || frameCount%50 == 0 {
					log.Printf("Face recognition: sent frame %d", frameCount)
				}

				offset += int64(frameSize)
			} else if n == 0 {
				// No new data, wait a bit
				time.Sleep(50 * time.Millisecond)
			} else {
				// Partial read, skip to next frame boundary
				offset += int64(n)
			}
		}

		file.Close()
		time.Sleep(100 * time.Millisecond)
	}
}

// readFramePipe reads raw frames for face recognition
func (cp *CameraPeer) readFramePipe() {
	log.Println("Starting frame pipe reader for face recognition")

	var frameReader io.Reader

	if runtime.GOOS == "windows" {
		// Windows: Connect to named pipe
		pipePath := `\\.\pipe\` + config.FaceRecogPipe
		// Wait for pipe to be available
		for i := 0; i < 30; i++ {
			if conn, err := os.Open(pipePath); err == nil {
				frameReader = conn
				break
			}
			if i < 29 {
				time.Sleep(time.Second)
			}
		}
		if frameReader == nil {
			log.Printf("Failed to connect to named pipe: %s", pipePath)
			return
		}
	} else {
		// Linux: Read from pipe:4 (this won't work directly, need different approach)
		log.Println("Frame pipe reading not implemented for Linux - use separate FFmpeg instance")
		return
	}

	frameSize := 320 * 320 * 3 // Default RGB24
	switch config.FaceRecogFormat {
	case "gray":
		frameSize = 320 * 320 // Grayscale
	case "rgb24":
		frameSize = 320 * 320 * 3 // RGB24
	case "yuv420p":
		frameSize = 320 * 320 * 3 / 2 // YUV420 (1.5 bytes per pixel)
	}

	frameCount := 0
	for {
		frameData := make([]byte, frameSize)
		n, err := io.ReadFull(frameReader, frameData)
		if err != nil {
			if err == io.EOF {
				log.Println("Frame pipe ended")
				return
			}
			log.Printf("Frame read error: %v", err)
			continue
		}

		if n != frameSize {
			log.Printf("Incomplete frame: %d/%d bytes", n, frameSize)
			continue
		}

		// Store frame for face recognition (make a copy to avoid race condition)
		frameCopy := make([]byte, frameSize)
		copy(frameCopy, frameData)
		cp.lastFrameMu.Lock()
		cp.lastFrame = frameCopy
		cp.lastFrameMu.Unlock()

		frameCount++
		if frameCount < 10 || frameCount%50 == 0 {
			log.Printf("Face recognition: sent frame %d", frameCount)
		}
	}
}

// restartFFmpeg restarts the FFmpeg process
func (cp *CameraPeer) restartFFmpeg() {
	// Don't restart if shutting down
	if cp.shuttingDown {
		log.Println("Shutdown in progress, not restarting FFmpeg")
		return
	}

	if cp.ffmpegCmd != nil && cp.ffmpegCmd.Process != nil {
		log.Println("Terminating existing FFmpeg process...")
		cp.ffmpegCmd.Process.Kill()
		cp.ffmpegCmd.Wait()
	}

	time.Sleep(2 * time.Second)

	if err := cp.startDualFFmpeg(); err != nil {
		log.Printf("Failed to restart FFmpeg: %v", err)
	}
}
