// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build ignore

// play-from-disk demonstrates how to send video and/or audio to your browser from files saved to disk.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
	"github.com/pion/webrtc/v4/pkg/media/oggreader"
)

// Default configuration - can be overridden via config.json or CLI
const (
	DefaultSignalingServerURL = "ws://192.168.56.1:3000"
	DefaultStunServerURL      = "stun:stun.l.google.com:19302"
	DefaultCameraName         = "stream_cam"
	DefaultVideoDevice        = "/dev/video0"
	DefaultAudioDevice        = "default"
	DefaultEncoder            = "libvpx" // VP8 for IVF
	DefaultVideoWidth         = 1280
	DefaultVideoHeight        = 720
	DefaultVideoFPS           = 30
	DefaultVideoBitrate       = "1M"
	DefaultAudioBitrate       = "128k"
	DefaultVideoFormat        = "h264"
	DefaultFaceRecogFormat    = "yuyv422"
	DefaultFaceRecogPipe      = "face_recog_pipe"
	DefaultFFmpegLogFile      = "ffmpeg.log"
	DefaultPythonCompiler     = "D:/Code_Main/Final_Year_Project/SBC/face_recog/.venv/Scripts/python.exe"
	DefaultPythonScript       = "D:/Code_Main/Final_Year_Project/SBC/webrtc_video/face_identify.py"
)

// Config holds all configuration options
type Config struct {
	SignalingServerURL string      `json:"signalingServerUrl"`
	StunServerURL      string      `json:"stunServerUrl"`
	CameraName         string      `json:"cameraName"`
	VideoDevice        string      `json:"videoDevice"`
	AudioDevice        string      `json:"audioDevice"`
	Encoder            string      `json:"encoder"`
	VideoWidth         int         `json:"videoWidth"`
	VideoHeight        int         `json:"videoHeight"`
	VideoFPS           int         `json:"videoFps"`
	VideoBitrate       string      `json:"videoBitrate"`
	AudioBitrate       string      `json:"audioBitrate"`
	FaceRecogEnabled   bool        `json:"faceRecogEnabled"`
	VideoFormat        string      `json:"videoFormat"`
	FaceRecogFormat    string      `json:"faceRecogFormat"`
	FaceRecogPipe      string      `json:"faceRecogPipe"`
	ICECredentials     []ICEServer `json:"iceCredentials,omitempty"`
	GPUAcceleration    bool        `json:"gpuAcceleration"`
	GPUDevice          string      `json:"gpuDevice"`
	FFmpegLogFile      string      `json:"ffmpegLogFile"`
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
	AudioDevice:        DefaultAudioDevice,
	Encoder:            DefaultEncoder,
	VideoWidth:         DefaultVideoWidth,
	VideoHeight:        DefaultVideoHeight,
	VideoFPS:           DefaultVideoFPS,
	VideoBitrate:       DefaultVideoBitrate,
	AudioBitrate:       DefaultAudioBitrate,
	FaceRecogEnabled:   false,
	VideoFormat:        DefaultVideoFormat,
	FaceRecogFormat:    DefaultFaceRecogFormat,
	FaceRecogPipe:      DefaultFaceRecogPipe,
	GPUAcceleration:    false,
	GPUDevice:          "auto",
	FFmpegLogFile:      filepath.ToSlash(DefaultFFmpegLogFile),
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
	Timestamp  int64   `json:"timestamp,omitempty"`
}

// Bbox represents bounding box
type BBox struct {
	x int `json:"x"`
	y int `json:"y"`
	w int `json:"w"`
	h int `json:"h"`
}

// ErrorInfo tracks last logged message for deduplication
type ErrorInfo struct {
	LastMessage string
}

// CameraQuality defines camera quality settings
type CameraQuality struct {
	Width   int
	Height  int
	FPS     int
	Bitrate string
	Format  string
	Name    string
}

// VideoCapability represents a supported video resolution and FPS
type VideoCapability struct {
	VCodec      string
	PixelFormat string
	Width       int
	Height      int
	FPS         int
}

// DeviceCapabilities holds supported device capabilities
type DeviceCapabilities struct {
	SupportedResolutions []VideoCapability
}

// Predefined camera quality levels (from highest to lowest)
var cameraQualities = []CameraQuality{
	{Width: 1920, Height: 1080, FPS: 30, Bitrate: "2M", Name: "1080p30(h264)", Format: "h264"},
	{Width: 1920, Height: 1080, FPS: 30, Bitrate: "2M", Name: "1080p30(mpeg)", Format: "mpeg"},
	{Width: 1280, Height: 720, FPS: 30, Bitrate: "1M", Name: "720p30(h264)", Format: "h264"},
	{Width: 1280, Height: 720, FPS: 30, Bitrate: "1M", Name: "720p30(mpeg)", Format: "mpeg"},
	{Width: 960, Height: 540, FPS: 30, Bitrate: "500k", Name: "540p30(mpeg)", Format: "h264"},
	{Width: 960, Height: 540, FPS: 25, Bitrate: "400k", Name: "540p25(mpeg)", Format: "mpeg"},
	{Width: 640, Height: 480, FPS: 30, Bitrate: "400k", Name: "480p30(h264)", Format: "h264"},
	{Width: 640, Height: 480, FPS: 30, Bitrate: "400k", Name: "480p30(mpeg)", Format: "mpeg"},
	{Width: 640, Height: 480, FPS: 30, Bitrate: "400k", Name: "480p30(yuyv422)", Format: "yuyv422"},
	{Width: 640, Height: 480, FPS: 30, Bitrate: "400k", Name: "480p30(nv12)", Format: "nv12"},
}

// CameraPeer manages WebRTC connection as a camera
type CameraPeer struct {
	roomID           string
	userID           string
	ws               *websocket.Conn
	pc               *webrtc.PeerConnection
	videoTrack       *webrtc.TrackLocalStaticSample
	audioTrack       *webrtc.TrackLocalStaticSample
	tracksOnce       sync.Once
	viewers          map[string]bool
	viewersMu        sync.RWMutex
	webrtcMu         sync.Mutex // serializes offer/answer/ICE and PC reset (sendOffer runs in goroutines)
	stopCh           chan struct{}
	videoDevice      string
	audioEnabled     bool
	faceRecogEnabled bool
	loopVideo        bool
	ffmpegCmd        *exec.Cmd
	videoPipe        io.ReadCloser
	h264Mode         bool // True for H.264, false for IVF
	audioPipe        io.ReadCloser
	audioTempFile    string
	facePipe         io.ReadCloser
	lastFrame        []byte
	lastFrameMu      sync.RWMutex
	lastPersonCount  int
	shuttingDown     bool
	lastErrorTime    map[string]ErrorInfo
	lastErrorMu      sync.Mutex
	currentQuality   int
	qualityMu        sync.Mutex
	restartingFFmpeg bool
	restartMu        sync.Mutex
	ffmpegRunning    bool
	ffmpegWaitDone   chan struct{}
	faceRecogFile    string
	facePythonCmd    *exec.Cmd
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

// findMatchingQuality finds the closest quality level to current config
func findMatchingQuality() int {
	if len(cameraQualities) == 0 {
		return 0
	}

	// First, try to find exact match for width and height
	exactResMatches := []int{}
	var videoFormatExists = false
	for i, quality := range cameraQualities {
		if quality.Format == config.VideoFormat {
			videoFormatExists = true
			if quality.Width == config.VideoWidth && quality.Height == config.VideoHeight {
				if quality.FPS == config.VideoFPS {
					log.Printf("Setting resolution %dx%d, FPS %d, format %s", config.VideoWidth, config.VideoHeight, config.VideoFPS, config.VideoFormat)
					return i
				}
				exactResMatches = append(exactResMatches, i)
			}
		}
	}
	if !videoFormatExists {
		log.Fatalf("Video format '%s' is not supported by this camera: '%s'", config.VideoFormat, config.CameraName)
	} else if len(exactResMatches) > 0 {
		// Find the closest FPS match
		availableFPS := []int{}
		for _, idx := range exactResMatches {
			availableFPS = append(availableFPS, cameraQualities[idx].FPS)
		}
		log.Fatalf("%d FPS not found for resolution %dx%d, format %s.\nAvailable FPS: %v", config.VideoFPS, config.VideoWidth, config.VideoHeight, config.VideoFormat, availableFPS)
	}
	log.Fatalf("No matching quality found for resolution %dx%d, FPS %d, format %s", config.VideoWidth, config.VideoHeight, config.VideoFPS, config.VideoFormat)
	return -1
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (cp *CameraPeer) stopFaceIdentify() {
	if cp.facePythonCmd == nil {
		return
	}
	if cp.facePythonCmd.Process != nil {
		_ = cp.facePythonCmd.Process.Kill()
	}
	_ = cp.facePythonCmd.Wait()
	cp.facePythonCmd = nil
}

// waitFFmpegGone waits for the supervision goroutine to finish cmd.Wait after the process exits.
func (cp *CameraPeer) waitFFmpegGone(d time.Duration) {
	if cp.ffmpegWaitDone == nil {
		return
	}
	select {
	case <-cp.ffmpegWaitDone:
	case <-time.After(d):
	}
}

// restartFFmpeg restarts FFmpeg with new quality settings
func (cp *CameraPeer) restartFFmpeg() {
	cp.restartMu.Lock()
	defer cp.restartMu.Unlock()

	// Prevent multiple concurrent restarts
	if cp.restartingFFmpeg {
		return
	}

	if cp.shuttingDown {
		return
	}

	cp.restartingFFmpeg = true
	defer func() {
		cp.restartingFFmpeg = false
	}()

	log.Printf("🔄 Restarting FFmpeg")

	cp.stopFaceIdentify()

	// Wait a moment before restart to allow pipes to settle
	time.Sleep(2 * time.Second)

	closePipes := func() {
		if cp.videoPipe != nil {
			cp.videoPipe.Close()
			cp.videoPipe = nil
		}
		if cp.audioPipe != nil {
			cp.audioPipe.Close()
			cp.audioPipe = nil
		}
		if cp.facePipe != nil {
			cp.facePipe.Close()
			cp.facePipe = nil
		}
	}

	// Terminate current FFmpeg process
	if cp.ffmpegCmd != nil && cp.ffmpegCmd.Process != nil && cp.ffmpegRunning {
		if err := cp.ffmpegCmd.Process.Kill(); err != nil {
			log.Printf("Failed to kill FFmpeg process: %v", err)
		}
	}

	closePipes()

	// Wait for supervision goroutine's Wait() to finish after kill
	if cp.ffmpegWaitDone != nil {
		select {
		case <-cp.ffmpegWaitDone:
		case <-time.After(10 * time.Second):
			log.Printf("FFmpeg process did not exit within 10s after kill")
		}
	}

	// Additional wait to ensure complete cleanup
	time.Sleep(2 * time.Second)

	// Restart FFmpeg with new settings
	if err := cp.startDualFFmpeg(); err != nil {
		log.Printf("Failed to restart FFmpeg: %v", err)
	} else {
		log.Printf("FFmpeg restarted successfully with new quality settings")
	}
}

// filterBinaryData removes non-printable characters from FFmpeg output
func filterBinaryData(data string) string {
	var result strings.Builder
	for _, r := range data {
		if r >= 32 && r <= 126 || r == '\n' || r == '\r' || r == '\t' {
			result.WriteRune(r)
		} else {
			result.WriteRune('.')
		}
	}
	return result.String()
}

// logRateLimited logs errors with deduplication to prevent screen flooding
func (cp *CameraPeer) logRateLimited(message string, _ time.Duration) {
	cp.lastErrorMu.Lock()
	defer cp.lastErrorMu.Unlock()

	if cp.lastErrorTime == nil {
		cp.lastErrorTime = make(map[string]ErrorInfo)
	}

	info, exists := cp.lastErrorTime[message]
	if !exists {
		// First occurrence - log immediately
		log.Printf(message)
		cp.lastErrorTime[message] = ErrorInfo{
			LastMessage: message,
		}
		return
	}

	// If this is the same as the last logged message, ignore it
	if info.LastMessage == message {
		return
	}

	// Different message - log it and update
	log.Printf(message)
	info.LastMessage = message
	cp.lastErrorTime[message] = info
}

// isEncoderAvailable checks if FFmpeg encoder is available
func isEncoderAvailable(encoder string) bool {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-encoders")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to get FFmpeg encoder list: %v", err)
		return false
	}

	return strings.Contains(string(output), encoder)
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

// validateInputDevices validates that input devices are available
func (cp *CameraPeer) validateInputDevices() error {
	// Validate video device
	if err := cp.validateVideoDevice(); err != nil {
		return fmt.Errorf("Video validate %v", err)
	}

	// Validate audio device if enabled
	if cp.audioEnabled {
		if err := cp.validateAudioDevice(); err != nil {
			cp.audioEnabled = false
			return fmt.Errorf("Audio validate %v, disabling audio", err)
		}
	}

	cp.currentQuality = findMatchingQuality()
	return nil
}

// validateVideoDevice checks if video device is available and generates quality levels
func (cp *CameraPeer) validateVideoDevice() error {
	if cp.videoDevice == "" {
		return fmt.Errorf("video device not specified")
	}

	// Get actual device capabilities
	capabilities, err := getDeviceCapabilities(cp.videoDevice)
	if err != nil {
		return fmt.Errorf("failed to get video device capabilities: %w", err)
	}

	// Update cameraQualities based on actual device capabilities
	updateCameraQualities(capabilities)

	// Reset currentQuality to highest quality (index 0) after dynamic generation
	cp.qualityMu.Lock()
	cp.currentQuality = 0
	cp.qualityMu.Unlock()

	log.Printf("Video device '%s' validated with %d supported resolutions", cp.videoDevice, len(capabilities.SupportedResolutions))
	return nil
}

// getDeviceCapabilities retrieves actual device capabilities using FFmpeg list_options
func getDeviceCapabilities(deviceName string) (*DeviceCapabilities, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ffmpeg", "-hide_banner", "-list_options", "true", "-f", "dshow", "-i", fmt.Sprintf("video=%s", deviceName))
	} else {
		cmd = exec.Command("ffmpeg", "-hide_banner", "-f", "v4l2", "-list_formats", "all", "-i", fmt.Sprintf("%s", deviceName))
	}

	output, err := cmd.CombinedOutput()
	outputStr := string(output)
	// FFmpeg returns error even when successful, check output for device info
	if err != nil {
		// Check if we actually got device capabilities despite the error
		if strings.Contains(outputStr, "DirectShow video device options") ||
			strings.Contains(outputStr, "pixel_format=") {
			// We got the device info, so ignore the error and continue
			log.Printf("Device capabilities found despite FFmpeg error, continuing...")
		} else if strings.Contains(outputStr, "Could not find video device") {
			return nil, fmt.Errorf("device '%s' not found", deviceName)
		} else {
			// Some other error occurred
			return nil, fmt.Errorf("failed to get device capabilities: %v", err)
		}
	}

	capabilities := &DeviceCapabilities{
		SupportedResolutions: []VideoCapability{},
	}

	// Parse FFmpeg output for supported resolutions
	lines := strings.Split(outputStr, "\n")

	resolutionMap := make(map[VideoCapability]bool)

	for _, line := range lines {
		if strings.Contains(line, "vcodec=") && strings.Contains(line, "min s=") && strings.Contains(line, "fps=") {
			// Parse vcodec format: vcodec=mjpeg  min s=1280x720 fps=30 max s=1280x720 fps=30
			vcodecMatch := regexp.MustCompile(`vcodec=(\w+)  min s=(\d+)x(\d+) fps=(\d+)`).FindStringSubmatch(line)
			if len(vcodecMatch) >= 5 {
				vcodec := vcodecMatch[1]
				width, _ := strconv.Atoi(vcodecMatch[2])
				height, _ := strconv.Atoi(vcodecMatch[3])
				fps, _ := strconv.Atoi(vcodecMatch[4])

				cap := VideoCapability{
					VCodec:      vcodec,
					PixelFormat: "", // No pixel format for vcodec entries
					Width:       width,
					Height:      height,
					FPS:         fps,
				}

				if !resolutionMap[cap] {
					resolutionMap[cap] = true
					capabilities.SupportedResolutions = append(capabilities.SupportedResolutions, cap)
				}
			}
		} else if strings.Contains(line, "pixel_format=") && strings.Contains(line, "min s=") && strings.Contains(line, "fps=") {
			// Parse pixel format: pixel_format=yuyv422  min s=1280x720 fps=10 max s=1280x720 fps=10
			pixelMatch := regexp.MustCompile(`pixel_format=(\w+)  min s=(\d+)x(\d+) fps=(\d+)`).FindStringSubmatch(line)
			if len(pixelMatch) >= 5 {
				pixelFormat := pixelMatch[1]
				width, _ := strconv.Atoi(pixelMatch[2])
				height, _ := strconv.Atoi(pixelMatch[3])
				fps, _ := strconv.Atoi(pixelMatch[4])

				cap := VideoCapability{
					VCodec:      "",
					PixelFormat: pixelFormat,
					Width:       width,
					Height:      height,
					FPS:         fps,
				}

				if !resolutionMap[cap] {
					resolutionMap[cap] = true
					capabilities.SupportedResolutions = append(capabilities.SupportedResolutions, cap)
				}
			}
		}
	} // Added missing closing brace here

	if len(capabilities.SupportedResolutions) == 0 {
		return nil, fmt.Errorf("no supported resolutions found for device '%s'", deviceName)
	}

	log.Printf("Device capabilities for '%s': %+v", deviceName, capabilities.SupportedResolutions)
	return capabilities, nil
}

// updateCameraQualities updates camera quality levels based on actual device capabilities
func updateCameraQualities(capabilities *DeviceCapabilities) {
	// Group by resolution and keep only preferred format for each
	resolutionMap := make(map[string]VideoCapability) // key: "widthxheight"

	for _, cap := range capabilities.SupportedResolutions {
		format := cap.VCodec
		if format == "" {
			format = cap.PixelFormat
		}
		key := fmt.Sprintf("%dx%d@%d(%s)", cap.Width, cap.Height, cap.FPS, format)

		// If no entry for this resolution yet, add it
		if _, exists := resolutionMap[key]; !exists {
			resolutionMap[key] = cap
		}
	}

	// Convert map back to slice and sort by resolution
	var filteredCaps []VideoCapability
	for _, cap := range resolutionMap {
		filteredCaps = append(filteredCaps, cap)
	}

	sort.Slice(filteredCaps, func(i, j int) bool {
		// Sort by resolution (highest to lowest)
		if filteredCaps[i].Width != filteredCaps[j].Width {
			return filteredCaps[i].Width > filteredCaps[j].Width
		}
		return filteredCaps[i].Height > filteredCaps[j].Height
	})

	// Generate quality levels based on actual capabilities
	cameraQualities = []CameraQuality{}
	for _, cap := range filteredCaps {
		// Skip very low resolutions
		if cap.Width < 320 || cap.Height < 240 {
			continue
		}

		// Calculate bitrate based on resolution and FPS
		bitrate := calculateBitrate(cap.Width, cap.Height, cap.FPS)
		format := cap.VCodec
		if format == "" {
			format = cap.PixelFormat
		}

		name := fmt.Sprintf("%dx%d@%d(%s)", cap.Width, cap.Height, cap.FPS, format)

		cameraQualities = append(cameraQualities, CameraQuality{
			Width:   cap.Width,
			Height:  cap.Height,
			FPS:     cap.FPS,
			Bitrate: bitrate,
			Format:  format,
			Name:    name,
		})
	}

	log.Printf("Updated camera quality levels: %+v", cameraQualities)
}

// calculateBitrate calculates appropriate bitrate based on resolution and FPS
func calculateBitrate(width, height, fps int) string {
	// Calculate base bitrate using industry-standard formulas
	// For H.264: 0.1-0.2 bits per pixel per frame for good quality
	pixels := width * height
	baseBitrate := pixels * fps / 50 // Adjusted for cleaner numbers

	// Round to nearest 100k for clean values
	if baseBitrate >= 1000000 {
		// Round to nearest 0.5M for high bitrates
		megabits := float64(baseBitrate) / 1000000
		rounded := math.Round(megabits*2) / 2 // Round to nearest 0.5
		return fmt.Sprintf("%.1fM", rounded)
	} else if baseBitrate >= 100000 {
		// Round to nearest 100k
		hundredK := math.Round(float64(baseBitrate)/100000) * 100000
		return fmt.Sprintf("%.0fk", hundredK/1000)
	} else {
		// Round to nearest 10k for low bitrates
		tenK := math.Round(float64(baseBitrate)/10000) * 10000
		return fmt.Sprintf("%.0fk", tenK/1000)
	}
}

// validateAudioDevice checks if audio device is available
func (cp *CameraPeer) validateAudioDevice() error {
	if config.AudioDevice == "" {
		return fmt.Errorf("audio device not specified")
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ffmpeg", "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	} else {
		cmd = exec.Command("ffmpeg", "-hide_banner", "-f", "pulse", "-list_devices", "true", "-i", "dummy")
	}

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// Parse device list from FFmpeg output
	if runtime.GOOS == "windows" {
		// Look for device in the dshow output format: "Device Name" (audio)
		lines := strings.Split(outputStr, "\n")
		for _, line := range lines {
			if strings.Contains(line, config.AudioDevice) && strings.Contains(line, "(audio)") {
				log.Printf("Audio device '%s' found", config.AudioDevice)
				return nil
			}
		}
		return fmt.Errorf("audio device '%s' not found in device list", config.AudioDevice)
	} else {
		// Linux pulse validation
		if err != nil {
			// Check if the error contains device information
			if strings.Contains(outputStr, config.AudioDevice) {
				log.Printf("Audio device '%s' found", config.AudioDevice)
				return nil
			}
			return fmt.Errorf("audio device '%s' not found: %v", config.AudioDevice, err)
		}
	}

	log.Printf("Audio device '%s' validated successfully", config.AudioDevice)
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
		case "VideoDevice":
			config.VideoDevice = value
			log.Printf("Override VideoDevice: %s", value)
		case "Encoder":
			if !isEncoderAvailable(value) {
				return fmt.Errorf("encoder %s not available\n"+
					"Run `ffmpeg -hide_banner -encoders` to find the appropriate gpu/cpu encoder you'd like to use", value)
			}
			config.Encoder = value
			log.Printf("Override Encoder: %s", value)
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
		case "VideoFormat":
			config.VideoFormat = value
			log.Printf("Override VideoFormat: %s", value)
		case "FaceRecogEnabled":
			if enabled, err := strconv.ParseBool(value); err == nil {
				config.FaceRecogEnabled = enabled
				log.Printf("Override FaceRecogEnabled: %v", enabled)
			}
		case "FaceRecogFormat":
			config.FaceRecogFormat = strings.TrimSpace(value)
			log.Printf("Override FaceRecogFormat: %s", value)
		case "FaceRecogPipe":
			config.FaceRecogPipe = value
			log.Printf("Override FaceRecogPipe: %s", value)
		case "AudioDevice":
			config.AudioDevice = value
			log.Printf("Override AudioDevice: %s", value)
		case "AudioBitrate":
			config.AudioBitrate = value
			log.Printf("Override AudioBitrate: %s", value)
		case "GPUAcceleration":
			if enabled, err := strconv.ParseBool(value); err == nil {
				config.GPUAcceleration = enabled
				log.Printf("Override GPUAcceleration: %v", enabled)
			}
		case "FFmpegLogFile":
			config.FFmpegLogFile = filepath.ToSlash(value)
			log.Printf("Override FFmpegLogFile: %s", value)
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
  -log string         FFmpeg report file (sets FFREPORT; see FFmpeg docs: file=NAME:level=N)
  -C, -config-key   Set config value (can be used multiple times)
                      Format: -C key=value
                      Available keys: SignalingServerURL, StunServerURL, CameraName, VideoDevice, AudioDevice, Encoder, VideoWidth, VideoHeight, VideoFPS, VideoBitrate, VideoFormat, AudioBitrate, FaceRecogEnabled, FaceRecogFormat, FaceRecogPipe, TestPipeOnly, GPUAcceleration, GPUDevice, FFmpegLogFile
  -h, -help         Show this help

Examples:
  %s -room=123
  %s -room=123 -c config.json
  %s -room=123 -C VideoDevice=/dev/video0 -C FaceRecogEnabled=true
  %s -room=123 -log ffmpeg.log
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

func main() {
	var roomID string
	var configPath string
	var configKeys arrayFlags
	var showHelp bool
	var ffmpegLogFile string

	flag.StringVar(&roomID, "room", "", "Room ID to join")
	flag.StringVar(&configPath, "config", "", "Path to config JSON file")
	flag.StringVar(&configPath, "c", "", "Path to config JSON file (shorthand)")
	flag.StringVar(&ffmpegLogFile, "log", "", "FFmpeg FFREPORT log file path (basename written under that file's directory)")
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
		log.Fatalf("Failed to load configuration: %v", err)
	}
	if ffmpegLogFile != "" {
		config.FFmpegLogFile = filepath.ToSlash(ffmpegLogFile)
		log.Printf("Using -log for FFmpeg FFREPORT file: %s", config.FFmpegLogFile)
	}

	// Now check for required room ID after config is loaded
	if roomID == "" {
		log.Fatal("Room ID is required. Use -room=YOUR_ROOM_ID")
	}

	log.Printf("Starting camera stream for room: %s", roomID)
	log.Printf("Face recognition enabled: %v", config.FaceRecogEnabled)
	log.Printf("Signaling server: %s", config.SignalingServerURL)

	peer := &CameraPeer{
		roomID:           roomID,
		viewers:          make(map[string]bool),
		stopCh:           make(chan struct{}),
		videoDevice:      config.VideoDevice,
		audioEnabled:     true,
		faceRecogEnabled: config.FaceRecogEnabled,
		lastPersonCount:  -1,
		currentQuality:   -1,
		restartingFFmpeg: false,
	}

	if err := peer.validateInputDevices(); err != nil {
		log.Fatalf("Error: %v", err)
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
			peer.waitFFmpegGone(5 * time.Second)
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

func createSharedTracks(cp *CameraPeer) error {
	var initErr error

	cp.tracksOnce.Do(func() {
		// Create video track (H.264 for H.264 mode, VP8 for IVF)
		var videoTrack *webrtc.TrackLocalStaticSample
		if cp.h264Mode {
			videoTrack, initErr = webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
				"video",
				"camera-video",
			)
		} else {
			videoTrack, initErr = webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
				"video",
				"camera-video",
			)
		}

		if initErr != nil {
			return
		}

		cp.videoTrack = videoTrack
		if _, initErr = cp.pc.AddTrack(videoTrack); initErr != nil {
			return
		}

		// Create audio track if enabled
		if cp.audioEnabled {
			audioTrack, initErr := webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
				"audio",
				"camera-audio",
			)
			if initErr != nil {
				return
			}
			cp.audioTrack = audioTrack

			if _, initErr = cp.pc.AddTrack(audioTrack); initErr != nil {
				return
			}
		}
	})

	return initErr
}

// createPeerConnection creates WebRTC peer connection
func (cp *CameraPeer) createPeerConnection() error {
	iceConfig := getICEConfiguration()

	pc, err := webrtc.NewPeerConnection(iceConfig)
	if err != nil {
		return err
	}

	cp.pc = pc

	// Create video track (H.264 for H.264 mode, VP8 for IVF)
	createErr := createSharedTracks(cp)
	if createErr != nil {
		return createErr
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
				cp.waitFFmpegGone(10 * time.Second)
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
	noViewers := len(cp.viewers) == 0
	cp.viewersMu.Unlock()

	// One PeerConnection can only pair with one remote answer. When the last viewer leaves,
	// close and recreate so the next viewer gets fresh ICE/SDP instead of stale candidates
	// and duplicate answers from the client hitting an already-stable PC.
	if noViewers {
		cp.resetPeerForNextViewer()
	}
}

// resetPeerForNextViewer closes the current PC and builds a new one (same media tracks for FFmpeg).
func (cp *CameraPeer) resetPeerForNextViewer() {
	cp.webrtcMu.Lock()
	defer cp.webrtcMu.Unlock()
	if cp.pc == nil {
		return
	}
	log.Println("All viewers left; resetting WebRTC peer connection for next join")
	_ = cp.pc.Close()
	cp.pc = nil
	if err := cp.createPeerConnection(); err != nil {
		log.Printf("Failed to recreate peer connection: %v", err)
	}
}

// sendOffer creates and sends WebRTC offer to a viewer
func (cp *CameraPeer) sendOffer(viewerID string) {
	log.Printf("Sending offer to viewer: %s", viewerID)

	cp.webrtcMu.Lock()
	defer cp.webrtcMu.Unlock()

	if cp.pc == nil {
		log.Printf("Skipping offer to %s: no peer connection", viewerID)
		return
	}

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
	if msg.Answer == nil {
		log.Println("Answer is nil")
		return
	}

	cp.webrtcMu.Lock()
	defer cp.webrtcMu.Unlock()

	if cp.pc == nil {
		return
	}

	// Clients often deliver the same answer twice (e.g. mobile + strict effects). Applying a
	// second answer while already stable triggers: stable -> SetRemote(answer) -> invalid.
	if cp.pc.SignalingState() != webrtc.SignalingStateHaveLocalOffer {
		log.Printf("Ignoring duplicate or late answer (signaling state is %s)", cp.pc.SignalingState().String())
		return
	}

	log.Printf("Received answer from viewer")
	if err := cp.pc.SetRemoteDescription(*msg.Answer); err != nil {
		log.Printf("Failed to set remote description: %v", err)
	}
}

// handleCandidate processes ICE candidate from viewer
func (cp *CameraPeer) handleCandidate(msg SignalingMessage) {
	if msg.Candidate == nil {
		return
	}

	cp.webrtcMu.Lock()
	defer cp.webrtcMu.Unlock()

	if cp.pc == nil {
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
		cp.stopFaceIdentify()
		if cp.ffmpegCmd != nil && cp.ffmpegCmd.Process != nil {
			log.Println("Terminating FFmpeg process...")
			cp.ffmpegCmd.Process.Kill()
		}
		cp.waitFFmpegGone(10 * time.Second)

		time.Sleep(1 * time.Second)
		if cp.faceRecogFile != "" {
			if err := os.Remove(cp.faceRecogFile); err != nil && !os.IsNotExist(err) {
				cp.logRateLimited(fmt.Sprintf("Failed to remove temp file %s: %v", cp.faceRecogFile, err), 10*time.Second)
			} else {
				log.Printf("Successfully removed temp file %s", cp.faceRecogFile)
			}
		}
	}

	return nil
}

// getGPUDeviceParam returns the appropriate GPU device parameter
func getGPUDeviceParam() string {
	if config.GPUAcceleration {
		// Specific GPU device requested
		if runtime.GOOS == "windows" {
			return "-gpu"
		} else if runtime.GOOS == "linux" {
			return "-vaapi_device"
		}
		return ""
	}
	return ""
}

// startDualFFmpeg launches one FFmpeg process with multiple linear outputs (no filter_complex):
// (1) WebRTC video to stdout (H.264 Annex-B or IVF), (2) optional rawvideo file for face recognition,
// (3) optional Opus to pipe:2.
func (cp *CameraPeer) startDualFFmpeg() error {
	cp.stopFaceIdentify()

	log.Println("Starting FFmpeg (multi-output: WebRTC stdout, optional face raw file, optional audio pipe:2)...")

	format := cameraQualities[cp.currentQuality].Format
	encoder := config.Encoder
	cp.h264Mode = strings.Contains(encoder, "h264") || encoder == "libx264"

	log.Printf("Video mode: %s", func() string {
		if cp.h264Mode {
			return "H.264"
		}
		return "IVF (VP8)"
	}())

	cp.faceRecogFile = ""
	facePixFmt := config.FaceRecogFormat
	if strings.TrimSpace(facePixFmt) == "" {
		facePixFmt = DefaultFaceRecogFormat
	}

	videoArgs := []string{
		"-hide_banner",
		"-thread_queue_size", "1024",
		"-f", "dshow",
		"-video_size", fmt.Sprintf("%dx%d", config.VideoWidth, config.VideoHeight),
		"-rtbufsize", "64M",
	}
	if format == "nv12" || format == "yuyv422" {
		videoArgs = append(videoArgs, "-pixel_format", format)
	} else if format == "h264" || format == "mjpeg" {
		videoArgs = append(videoArgs, "-vcodec", format)
	}
	videoArgs = append(videoArgs, "-i", fmt.Sprintf("video=%s", cp.videoDevice))

	if runtime.GOOS == "windows" {
		if cp.audioEnabled {
			videoArgs = append(videoArgs,
				"-thread_queue_size", "1024",
				"-f", "dshow",
				"-i", fmt.Sprintf("audio=%s", config.AudioDevice),
			)
		}
	} else if cp.audioEnabled {
		videoArgs = append(videoArgs,
			"-thread_queue_size", "1024",
			"-f", "pulse",
			"-i", config.AudioDevice,
		)
	}

	gpuParam := ""
	if config.GPUAcceleration {
		gpuParam = getGPUDeviceParam()
		log.Printf("(GPU: %v), GPU parameter: %s\n", config.GPUAcceleration, gpuParam)
	}

	if gpuParam != "" && config.GPUAcceleration {
		videoArgs = append(videoArgs, gpuParam, config.GPUDevice)
	}

	var outputs []string

	// (A) WebRTC video → stdout (H.264 elementary stream or IVF; never rawvideo+copy)
	if encoder == "copy" && cp.h264Mode {
		outputs = append(outputs,
			"-map", "0:v",
			"-c:v", "copy",
			"-bsf:v", "h264_mp4toannexb",
			"-f", "h264", "-",
		)
	} else if cp.h264Mode {
		outputs = append(outputs,
			"-map", "0:v",
			"-c:v", encoder,
			"-preset", "ultrafast",
			"-crf", "18", // High baseline quality
			"-b:v", config.VideoBitrate,
			"-maxrate", "2M", // Strict ceiling for network constraints
			"-bufsize", "2M", // 1-second buffer window for rate control
			"-g", "30", // Keyframe every 30 frames (1s) for fast WebRTC recovery
			"-pix_fmt", "yuv420p", // Required format for WebRTC compatibility
			"-tune", "zerolatency",
			"-f", "h264", "-",
		)
	} else {
		outputs = append(outputs,
			"-map", "0:v",
			"-c:v", encoder,
			"-preset", "ultrafast",
			"-crf", "18",
			"-b:v", config.VideoBitrate,
			"-maxrate", "2M",
			"-bufsize", "2M",
			"-g", "30",
			"-pix_fmt", "yuv420p",
			"-f", "ivf", "-",
		)
	}

	// (B) Face recognition: decoded raw video to disk (same input; not copy)
	if cp.faceRecogEnabled {
		currentDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		storeFile := filepath.Join(currentDir, "pipe", "face_recog_frames.raw")
		if err := os.MkdirAll(filepath.Dir(storeFile), 0755); err != nil {
			return fmt.Errorf("create pipe directory: %w", err)
		}
		absFace, err := filepath.Abs(filepath.FromSlash(storeFile))
		if err != nil {
			absFace = filepath.FromSlash(storeFile)
		}
		cp.faceRecogFile = absFace
		outputs = append(outputs,
			"-map", "0:v",
			"-f", "rawvideo",
			"-pix_fmt", facePixFmt,
			absFace,
		)
		log.Printf("Face recognition raw frames: %s (pix_fmt=%s)", absFace, facePixFmt)
	}

	// (C) Audio → temp file (Opus)
	if cp.audioEnabled {
		audioTemp, err := os.CreateTemp("", "webrtc_audio_*.opus")
		if err != nil {
			return fmt.Errorf("create temp audio file: %w", err)
		}
		cp.audioTempFile = audioTemp.Name()
		audioTemp.Close()
		outputs = append(outputs,
			"-map", "1:a",
			"-c:a", "libopus",
			"-b:a", config.AudioBitrate,
			"-minrate", "32k",
			"-maxrate", "64k",
			"-ar", "48000",
			"-ac", "1",
			"-f", "opus", cp.audioTempFile,
		)
	}

	ffmpegArgs := append([]string{"-y"}, append(videoArgs, outputs...)...)
	log.Printf("FFmpeg encoder (video): %s", encoder)

	cp.ffmpegCmd = exec.Command("ffmpeg", ffmpegArgs...)

	videoPipe, err := cp.ffmpegCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create video pipe: %w", err)
	}
	cp.videoPipe = videoPipe

	if config.FFmpegLogFile != "" {
		logPath := filepath.Clean(config.FFmpegLogFile)
		absLog, err := filepath.Abs(logPath)
		if err != nil {
			absLog = logPath
		}
		logDir := filepath.Dir(absLog)
		logBase := filepath.Base(absLog)
		cp.ffmpegCmd.Dir = logDir
		// FFREPORT syntax: file=FILENAME:level=N (basename avoids ':' in Windows paths being parsed as level)
		cp.ffmpegCmd.Env = append(os.Environ(), fmt.Sprintf("FFREPORT=file=%s:level=32", logBase))
	}

	if err := cp.ffmpegCmd.Start(); err != nil {
		if cp.audioPipe != nil {
			_ = cp.audioPipe.Close()
			cp.audioPipe = nil
		}
		_ = videoPipe.Close()
		return fmt.Errorf("failed to start FFmpeg: %w", err)
	}

	waitDone := make(chan struct{})
	cp.ffmpegWaitDone = waitDone
	go func() {
		defer close(waitDone)
		err := cp.ffmpegCmd.Wait()
		exitCode := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			}
		}
		log.Printf("FFmpeg exited: exitCode=%d err=%v", exitCode, err)
		cp.ffmpegRunning = false
	}()

	cp.ffmpegRunning = true
	log.Printf("FFmpeg started with args: %v", ffmpegArgs)

	if cp.h264Mode {
		go cp.readH264Pipe()
	} else {
		go cp.readIVFPipe()
	}

	if cp.audioEnabled && cp.audioPipe != nil {
		log.Println("Audio: starting Ogg Opus reader from FFmpeg pipe:3 (waits for WebRTC audio track)")
		go cp.readOGGPipe()
	}

	if cp.faceRecogEnabled && cp.faceRecogFile != "" {
		for attempt := 0; attempt < 50; attempt++ {
			if _, err := os.Stat(cp.faceRecogFile); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		cp.startFaceIdentifyJSONL(cp.faceRecogFile)
	}

	return nil
}

func (cp *CameraPeer) sendIdentificationData(persons []PersonInfo, timestamp int64) {
	msg := SignalingMessage{
		Type:      "identification_data",
		Persons:   persons,
		Timestamp: timestamp,
	}
	if err := cp.ws.WriteJSON(msg); err != nil {
		log.Printf("Failed to send identification data: %v", err)
	} else {
		log.Printf("Sent identification data: %d person(s)", len(persons))
	}
}

// startFaceIdentifyJSONL runs face_identify.py on the growing raw file and forwards JSON lines on the WebSocket.
func (cp *CameraPeer) startFaceIdentifyJSONL(rawPath string) {
	if cp.ws == nil {
		return
	}
	faceFmt := config.FaceRecogFormat
	if faceFmt == "" {
		faceFmt = DefaultFaceRecogFormat
	}

	cmd := exec.Command(DefaultPythonCompiler, "-u", DefaultPythonScript,
		"--input", rawPath,
		"--format", faceFmt,
		"--width", strconv.Itoa(config.VideoWidth),
		"--height", strconv.Itoa(config.VideoHeight),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("face identify: stdout pipe: %v", err)
		return
	}
	cp.facePythonCmd = cmd
	if err := cmd.Start(); err != nil {
		log.Printf("face identify: start: %v", err)
		cp.facePythonCmd = nil
		return
	}
	go func() {
		defer func() {
			_ = cmd.Wait()
			if cp.facePythonCmd == cmd {
				cp.facePythonCmd = nil
			}
		}()
		scanner := bufio.NewScanner(stdout)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 4*1024*1024)
		for scanner.Scan() {
			select {
			case <-cp.stopCh:
				_ = cmd.Process.Kill()
				return
			default:
			}
			line := scanner.Bytes()
			var payload struct {
				Persons []struct {
					Name       string  `json:"name"`
					Confidence float64 `json:"confidence"`
					Score      float64 `json:"score"`
					Bbox       *BBox   `json:"bbox"`
				} `json:"persons"`
				Timestamp float64 `json:"timestamp"`
			}
			if err := json.Unmarshal(line, &payload); err != nil {
				continue
			}
			if len(payload.Persons) == 0 {
				continue
			}
			out := make([]PersonInfo, 0, len(payload.Persons))
			for _, p := range payload.Persons {
				conf := p.Confidence
				if conf == 0 && p.Score != 0 {
					conf = p.Score
				}
				out = append(out, PersonInfo{
					Name:       p.Name,
					Confidence: conf,
					Bbox:       p.Bbox,
				})
			}
			ts := int64(math.Round(payload.Timestamp * 1000))
			if ts == 0 {
				ts = time.Now().UnixMilli()
			}
			cp.sendIdentificationData(out, ts)
		}
		if err := scanner.Err(); err != nil {
			log.Printf("face identify stdout: %v", err)
		}
	}()
}

// readIVFPipe reads IVF from FFmpeg stdout and sends to WebRTC
func (cp *CameraPeer) readIVFPipe() {
	log.Println("Starting IVF pipe reader for WebRTC")

	// Wait for peer connection to be established
	// This will be set when we receive existing_users or user-joined events

	// Create IVF reader from pipe
	ivf, _, err := ivfreader.NewWith(cp.videoPipe)
	if err != nil {
		log.Fatalf("Failed to create IVF reader: %v", err)
		return
	}

	frameCount := 0
	startTime := time.Now()

	for {
		frame, _, err := ivf.ParseNextFrame()
		if err != nil {
			if err == io.EOF {
				// Only restart if not shutting down
				if !cp.shuttingDown {
					log.Println("FFmpeg ended - restarting...")
					// Restart FFmpeg if it crashes
					cp.restartFFmpeg()
				} else {
					log.Println("FFmpeg ended during shutdown, not restarting")
				}
				continue
			}
			cp.logRateLimited(fmt.Sprintf("IVF parse error: %v", err), 5*time.Second)
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

// readH264Pipe reads H.264 stream from FFmpeg stdout pipe and sends to WebRTC
func (cp *CameraPeer) readH264Pipe() {
	log.Println("Starting H.264 pipe reader for WebRTC")

	// Create H.264 reader from pipe
	h264, err := h264reader.NewReader(cp.videoPipe)
	if err != nil {
		log.Fatalf("Failed to create H.264 reader: %v", err)
		return
	}

	frameCount := 0
	startTime := time.Now()
	nextVideoSampleTime := time.Now()
	timePerFrame := time.Millisecond * time.Duration(config.VideoFPS)

	for {
		select {
		case <-cp.stopCh:
			return
		default:
		}

		nal, err := h264.NextNAL()
		if err == io.EOF {
			if !cp.shuttingDown {
				log.Printf("H.264 pipe ended (%d frames in %.1fs) - restarting...", frameCount, time.Since(startTime).Seconds())
				// Restart FFmpeg if it crashes
				// cp.restartFFmpeg()
			} else {
				log.Println("H.264 pipe ended during shutdown, not restarting")
			}
			return // restartFFmpeg should re-launch this goroutine
		}
		if err != nil {
			cp.logRateLimited(fmt.Sprintf("H.264 NAL read error: %v", err), 5*time.Second)
			continue
		}

		// Timing logic for smooth playback
		// Golang's time.Sleep() is not precise enough for a consistent video stream
		// (see https://github.com/golang/go/issues/44343). Instead, calculate the
		// remaining sleep duration using wall clock time.
		nextVideoSampleTime = nextVideoSampleTime.Add(timePerFrame)
		if sleep := nextVideoSampleTime.Sub(time.Now()); sleep > 0 {
			time.Sleep(sleep)
		}

		// Send to WebRTC track when ready
		if cp.videoTrack != nil {
			if err := cp.videoTrack.WriteSample(media.Sample{
				Data:     nal.Data,
				Duration: time.Second / time.Duration(config.VideoFPS),
			}); err != nil {
				log.Printf("Failed to write H.264 sample: %v", err)
				return
			}
		}

		frameCount++
		if frameCount < 5 || frameCount%200 == 0 {
			log.Printf("H.264: sent NAL unit %d (%.1fs elapsed)", frameCount, time.Since(startTime).Seconds())
		}
	}
}

// readFFmpegAudioOGG reads Opus-in-Ogg from FFmpeg (pipe:3 via ExtraFiles), same timing idea as laptop/main.go.
func (cp *CameraPeer) readOGGPipe() {
	waitUntil := time.Now().Add(2 * time.Minute)
	for cp.audioTrack == nil && !cp.shuttingDown {
		if time.Now().After(waitUntil) {
			log.Println("Audio: no WebRTC audio track after 2m, exiting reader")
			return
		}
		select {
		case <-cp.stopCh:
			log.Println("Audio: stop before track ready")
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	if cp.audioTrack == nil {
		return
	}
	ogg, _, err := oggreader.NewWith(cp.audioPipe)
	if err != nil {
		log.Printf("Audio: oggreader: %v", err)
		return
	}

	var lastGranule uint64
	pageCount := 0
	startTime := time.Now()

	for {
		select {
		case <-cp.stopCh:
			log.Println("Audio: stop signal, exiting reader")
			return
		default:
		}
		pageData, pageHeader, err := ogg.ParseNextPage()
		if errors.Is(err, io.EOF) {
			log.Printf("Audio: Opus stream EOF (%d pages in %.1fs)", pageCount, time.Since(startTime).Seconds())
			return
		}
		if err != nil {
			log.Printf("Audio: read Ogg page: %v", err)
			return
		}
		sampleCount := float64(pageHeader.GranulePosition - lastGranule)
		lastGranule = pageHeader.GranulePosition
		sampleDuration := time.Duration((sampleCount/48000)*1000) * time.Millisecond
		if err := cp.audioTrack.WriteSample(media.Sample{Data: pageData, Duration: sampleDuration}); err != nil {
			log.Printf("Audio: WriteSample: %v", err)
			return
		}
		if sampleDuration > 0 {
			time.Sleep(sampleDuration)
		}
		pageCount++
		if pageCount < 5 || pageCount%100 == 0 {
			log.Printf("Audio: sent Ogg page %d (%.1fs elapsed)", pageCount, time.Since(startTime).Seconds())
		}
	}
}
