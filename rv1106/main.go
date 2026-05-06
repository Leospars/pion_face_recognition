package main

/*
#cgo CFLAGS: -I${SRCDIR}/../rv1106-system/media/mpp/release_mpp_rv1106_arm-rockchip830-linux-uclibcgnueabihf/include
#cgo LDFLAGS: -L${SRCDIR}/../rv1106-system/media/mpp/release_mpp_rv1106_arm-rockchip830-linux-uclibcgnueabihf/lib -lrockchip_mpp

#include <rockchip/rk_mpi.h>
#include <rockchip/rk_type.h>
#include <rockchip/rk_mpi_venc.h>
#include <rockchip/mpp_buffer.h>
#include <stdlib.h>
#include <string.h>

// Forward declarations for callback
extern void onFrameReceived(void* data, int size, long long pts);

// MPP context wrapper
typedef struct {
    MppCtx ctx;
    MppApi* mpi;
    MppBufferGroup frame_group;
    MppBufferGroup packet_group;
    int width;
    int height;
    int fps;
    int running;
} MppVencCtx;

// Initialize MPP VENC
MppVencCtx* mpp_venc_init(int width, int height, int fps) {
    MppVencCtx* venc = (MppVencCtx*)calloc(1, sizeof(MppVencCtx));
    if (!venc) return NULL;

    venc->width = width;
    venc->height = height;
    venc->fps = fps;
    venc->running = 1;

    MppCtx ctx = NULL;
    MppApi* mpi = NULL;

    // Create MPP context for VENC
    MPP_RET ret = mpp_create(&ctx, &mpi);
    if (ret != MPP_OK) {
        free(venc);
        return NULL;
    }

    venc->ctx = ctx;
    venc->mpi = mpi;

    // Init VENC
    ret = mpp_init(ctx, MPP_CTX_ENC, MPP_VIDEO_CodingAVC);
    if (ret != MPP_OK) {
        mpp_destroy(ctx);
        free(venc);
        return NULL;
    }

    // Set encoder parameters
    MppEncCfg cfg = NULL;
    ret = mpp_enc_cfg_get(&cfg);
    if (ret != MPP_OK) {
        mpp_destroy(ctx);
        free(venc);
        return NULL;
    }

    // Configure encoding parameters
    mpp_enc_cfg_set_s32(cfg, "prep:width", width);
    mpp_enc_cfg_set_s32(cfg, "prep:height", height);
    mpp_enc_cfg_set_s32(cfg, "prep:hor_stride", width);
    mpp_enc_cfg_set_s32(cfg, "prep:ver_stride", height);
    mpp_enc_cfg_set_s32(cfg, "prep:format", MPP_FMT_YUV420SP);

    mpp_enc_cfg_set_s32(cfg, "rc:mode", MPP_ENC_RC_MODE_VBR);
    mpp_enc_cfg_set_s32(cfg, "rc:fps_in_num", fps);
    mpp_enc_cfg_set_s32(cfg, "rc:fps_in_denorm", 1);
    mpp_enc_cfg_set_s32(cfg, "rc:fps_out_num", fps);
    mpp_enc_cfg_set_s32(cfg, "rc:fps_out_denorm", 1);

    mpp_enc_cfg_set_s32(cfg, "rc:gop", fps * 2);
    mpp_enc_cfg_set_s32(cfg, "rc:skip_thr", 0);

    mpp_enc_cfg_set_s32(cfg, "hw:qbps", 4000000);
    mpp_enc_cfg_set_s32(cfg, "hw:mb_rc", 1);

    ret = mpi->control(ctx, MPP_ENC_SET_CFG, cfg);
    mpp_enc_cfg_deinit(cfg);

    if (ret != MPP_OK) {
        mpp_destroy(ctx);
        free(venc);
        return NULL;
    }

    // Create buffer groups
    ret = mpp_buffer_group_get_internal(&venc->frame_group, MPP_BUFFER_TYPE_DRM);
    if (ret != MPP_OK) {
        mpp_destroy(ctx);
        free(venc);
        return NULL;
    }

    ret = mpp_buffer_group_get_internal(&venc->packet_group, MPP_BUFFER_TYPE_DRM);
    if (ret != MPP_OK) {
        mpp_buffer_group_put(venc->frame_group);
        mpp_destroy(ctx);
        free(venc);
        return NULL;
    }

    return venc;
}

// Send raw frame to encoder
int mpp_venc_send_frame(MppVencCtx* venc, void* data, int size, long long pts) {
    if (!venc || !venc->running) return -1;

    MppFrame frame = NULL;
    MppBuffer buf = NULL;
    MPP_RET ret;

    // Get buffer from group
    ret = mpp_buffer_get(venc->frame_group, &buf, size);
    if (ret != MPP_OK) return -1;

    // Copy frame data
    memcpy(mpp_buffer_get_ptr(buf), data, size);

    // Create frame
    ret = mpp_frame_init(&frame);
    if (ret != MPP_OK) {
        mpp_buffer_put(buf);
        return -1;
    }

    mpp_frame_set_buffer(frame, buf);
    mpp_frame_set_width(frame, venc->width);
    mpp_frame_set_height(frame, venc->height);
    mpp_frame_set_hor_stride(frame, venc->width);
    mpp_frame_set_ver_stride(frame, venc->height);
    mpp_frame_set_fmt(frame, MPP_FMT_YUV420SP);
    mpp_frame_set_pts(frame, pts);

    // Send to encoder
    ret = venc->mpi->encode_put_frame(venc->ctx, frame);

    mpp_frame_deinit(&frame);
    mpp_buffer_put(buf);

    return (ret == MPP_OK) ? 0 : -1;
}

// Get encoded packet from encoder
int mpp_venc_get_packet(MppVencCtx* venc, void** data, int* size, long long* pts) {
    if (!venc || !venc->running) return -1;

    MppPacket packet = NULL;
    MPP_RET ret = venc->mpi->encode_get_packet(venc->ctx, &packet);

    if (ret != MPP_OK || !packet) return -1;

    void* ptr = mpp_packet_get_pos(packet);
    size_t len = mpp_packet_get_length(packet);

    *data = ptr;
    *size = (int)len;
    *pts = mpp_packet_get_pts(packet);

    // Call Go callback
    onFrameReceived(ptr, (int)len, *pts);

    // Release packet
    mpp_packet_deinit(&packet);

    return 0;
}

// Deinitialize MPP VENC
void mpp_venc_deinit(MppVencCtx* venc) {
    if (!venc) return;

    venc->running = 0;

    if (venc->frame_group) {
        mpp_buffer_group_put(venc->frame_group);
    }
    if (venc->packet_group) {
        mpp_buffer_group_put(venc->packet_group);
    }
    if (venc->ctx) {
        mpp_destroy(venc->ctx);
    }

    free(venc);
}

// V4L2 capture structures
typedef struct {
    int fd;
    void* buffers[4];
    int buffer_count;
    int width;
    int height;
} V4L2Capture;

// Open and init V4L2 capture
V4L2Capture* v4l2_capture_open(const char* device, int width, int height) {
    // This would contain full V4L2 setup code
    // For now, returning NULL as placeholder
    // Production code should use proper V4L2 ioctls
    return NULL;
}

// Read frame from V4L2
int v4l2_capture_read(V4L2Capture* cap, void* buffer, int size) {
    if (!cap) return -1;
    // Production: VIDIOC_DQBUF and copy
    return 0;
}

// Close V4L2 capture
void v4l2_capture_close(V4L2Capture* cap) {
    if (!cap) return;
    // Production: VIDIOC_STREAMOFF, munmap, close
    free(cap);
}
*/
import "C"
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
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Default configuration - can be overridden via config.json or CLI
const (
	DefaultSignalingServerURL = "ws://192.168.56.1:3000"
	DefaultStunServerURL      = "stun:stun.l.google.com:19302"
	DefaultCameraName         = "rv1106_cam"
	DefaultPythonCompiler     = "python3"
	DefaultFaceRecogScript    = "../face_recog_wrapper.py"
	DefaultVideoWidth         = 1920
	DefaultVideoHeight        = 1080
	DefaultVideoFPS           = 30
	DefaultV4L2Device         = "/dev/video0"
	FaceRecogFrameInterval    = 500 * time.Millisecond
)

// Config holds all configuration options
type Config struct {
	SignalingServerURL string      `json:"signalingServerUrl"`
	StunServerURL      string      `json:"stunServerUrl"`
	CameraName         string      `json:"cameraName"`
	PythonCompiler     string      `json:"pythonCompiler"`
	FaceRecogScript    string      `json:"faceRecogScript"`
	VideoWidth         int         `json:"videoWidth"`
	VideoHeight        int         `json:"videoHeight"`
	VideoFPS           int         `json:"videoFps"`
	V4L2Device         string      `json:"v4l2Device"`
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
	VideoWidth:         DefaultVideoWidth,
	VideoHeight:        DefaultVideoHeight,
	VideoFPS:           DefaultVideoFPS,
	V4L2Device:         DefaultV4L2Device,
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

//export onFrameReceived
func onFrameReceived(data unsafe.Pointer, size C.int, pts C.longlong) {
	// This will be called from C when encoded frame is ready
	// Need to copy data and send to WebRTC track
	frameData := C.GoBytes(data, size)
	globalPeer.sendEncodedFrame(frameData, int64(pts))
}

// Global reference to peer for C callbacks
var globalPeer *CameraPeerRV1106

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
}

// CameraPeerRV1106 manages WebRTC connection for RV1106 hardware
type CameraPeerRV1106 struct {
	roomID           string
	userID           string
	ws               *websocket.Conn
	pc               *webrtc.PeerConnection
	videoTrack       *webrtc.TrackLocalStaticSample
	audioTrack       *webrtc.TrackLocalStaticSample
	viewers          map[string]bool
	viewersMu        sync.RWMutex
	stopCh           chan struct{}
	venc             *C.MppVencCtx
	audioEnabled     bool
	frameCh          chan []byte
	faceRecogEnabled bool
	faceRecogCmd     *exec.Cmd
	faceRecogStdin   io.WriteCloser
	faceRecogStdout  *bufio.Scanner
	faceRecogMu      sync.Mutex
	lastFrame        []byte
	lastFrameMu      sync.RWMutex
}

func main() {
	var roomID string
	var enableAudio bool
	var v4l2Device string
	var enableFaceRecog bool
	var configPath string
	var configKeys arrayFlags
	var showHelp bool

	flag.StringVar(&roomID, "room", "", "Room ID to join")
	flag.BoolVar(&enableAudio, "audio", true, "Enable audio capture")
	flag.StringVar(&v4l2Device, "device", config.V4L2Device, "V4L2 video device")
	flag.BoolVar(&enableFaceRecog, "facerecog", true, "Enable face recognition")
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

	log.Printf("Starting RV1106 camera peer for room: %s", roomID)
	log.Printf("Video: %dx%d@%dfps from %s", config.VideoWidth, config.VideoHeight, config.VideoFPS, v4l2Device)
	log.Printf("Audio enabled: %v", enableAudio)
	log.Printf("Face recognition enabled: %v", enableFaceRecog)
	log.Printf("Signaling server: %s", config.SignalingServerURL)

	peer := &CameraPeerRV1106{
		roomID:           roomID,
		viewers:          make(map[string]bool),
		stopCh:           make(chan struct{}),
		audioEnabled:     enableAudio,
		faceRecogEnabled: enableFaceRecog,
		frameCh:          make(chan []byte, 30),
	}

	// Set global reference for C callbacks
	globalPeer = peer

	// Handle shutdown gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutdown signal received")
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
  -device string      V4L2 video device path (default "/dev/video0")
  -audio              Enable audio capture (default true)
  -facerecog          Enable face recognition (default true)
  -c, -config string  Path to config JSON file
  -C, -config-key     Set config value (can be used multiple times)
                      Format: -C key=value
                      Available keys: SignalingServerURL, StunServerURL, CameraName, PythonCompiler, FaceRecogScript
  -h, -help           Show this help

Examples:
  %s -room=123
  %s -room=123 -c config.json
  %s -room=123 -C PythonCompiler=/usr/bin/python3 -C CameraName=rv1106_cam
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0])
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
		}
	}

	return nil
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

// Run starts the camera peer
func (cp *CameraPeerRV1106) Run() error {
	// Initialize face recognition if enabled
	if cp.faceRecogEnabled {
		if err := cp.initFaceRecognition(); err != nil {
			log.Printf("Failed to initialize face recognition: %v", err)
			cp.faceRecogEnabled = false
		}
		defer cp.stopFaceRecognition()
	}

	// Initialize MPP hardware encoder
	if err := cp.initMPPEncoder(); err != nil {
		return fmt.Errorf("failed to init MPP encoder: %w", err)
	}
	defer cp.deinitMPPEncoder()

	// Connect to signaling server
	if err := cp.connectSignaling(); err != nil {
		return fmt.Errorf("failed to connect to signaling server: %w", err)
	}
	defer cp.ws.Close()

	// Join room as camera
	if err := cp.joinRoom(); err != nil {
		return fmt.Errorf("failed to join room: %w", err)
	}

	// Start handling WebSocket messages (this will handle room-not-found or existing_users)
	go cp.handleSignaling()

	// Start V4L2 capture
	go cp.captureAndEncode()

	// Start face recognition processing
	if cp.faceRecogEnabled {
		go cp.processFaceRecognition()
	}

	// Wait for stop signal
	<-cp.stopCh
	log.Println("Camera peer shutting down")

	return nil
}

// initMPPEncoder initializes Rockchip MPP hardware encoder
func (cp *CameraPeerRV1106) initMPPEncoder() error {
	log.Println("Initializing MPP hardware encoder...")

	venc := C.mpp_venc_init(C.int(config.VideoWidth), C.int(config.VideoHeight), C.int(config.VideoFPS))
	if venc == nil {
		return fmt.Errorf("failed to initialize MPP VENC")
	}

	cp.venc = venc
	log.Println("MPP hardware encoder initialized")
	return nil
}

// deinitMPPEncoder deinitializes MPP encoder
func (cp *CameraPeerRV1106) deinitMPPEncoder() {
	if cp.venc != nil {
		log.Println("Deinitializing MPP encoder...")
		C.mpp_venc_deinit(cp.venc)
		cp.venc = nil
	}
}

// connectSignaling establishes WebSocket connection
func (cp *CameraPeerRV1106) connectSignaling() error {
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
func (cp *CameraPeerRV1106) joinRoom() error {
	msg := SignalingMessage{
		Type:   "join",
		RoomID: cp.roomID,
		Name:   config.CameraName,
		IsCam:  true,
	}

	return cp.ws.WriteJSON(msg)
}

// createPeerConnection creates WebRTC peer connection
func (cp *CameraPeerRV1106) createPeerConnection() error {
	iceConfig := getICEConfiguration()

	pc, err := webrtc.NewPeerConnection(iceConfig)
	if err != nil {
		return err
	}

	cp.pc = pc

	// Create video track (H.264 from MPP hardware encoder)
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

	// Create audio track (Opus)
	if cp.audioEnabled {
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

	// Handle incoming tracks (for two-way audio from viewer)
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		log.Printf("Received track: %s (%s)", track.ID(), track.Kind().String())
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			go cp.playRemoteAudio(track)
		}
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
				Candidate:    candidate.ToJSON(),
			}
			if err := cp.ws.WriteJSON(msg); err != nil {
				log.Printf("Failed to send ICE candidate: %v", err)
			}
		}
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Peer connection state: %s", state.String())
	})

	return nil
}

// handleSignaling processes WebSocket messages
func (cp *CameraPeerRV1106) handleSignaling() {
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
func (cp *CameraPeerRV1106) handleExistingUsers(users []UserInfo) {
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
func (cp *CameraPeerRV1106) handleUserJoined(userID, name string, isCam bool) {
	log.Printf("User joined: %s (%s), isCam: %v", name, userID, isCam)

	if !isCam {
		cp.viewersMu.Lock()
		cp.viewers[userID] = true
		cp.viewersMu.Unlock()
		go cp.sendOffer(userID)
	}
}

// handleUserLeft handles user leaving
func (cp *CameraPeerRV1106) handleUserLeft(userID string) {
	log.Printf("User left: %s", userID)
	cp.viewersMu.Lock()
	delete(cp.viewers, userID)
	cp.viewersMu.Unlock()
}

// sendOffer creates and sends WebRTC offer to a viewer
func (cp *CameraPeerRV1106) sendOffer(viewerID string) {
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
func (cp *CameraPeerRV1106) handleAnswer(msg SignalingMessage) {
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
func (cp *CameraPeerRV1106) handleCandidate(msg SignalingMessage) {
	if msg.Candidate == nil {
		return
	}

	if err := cp.pc.AddICECandidate(*msg.Candidate); err != nil {
		log.Printf("Failed to add ICE candidate: %v", err)
	}
}

// captureAndEncode captures video from V4L2 and encodes with MPP
func (cp *CameraPeerRV1106) captureAndEncode() {
	log.Println("Starting V4L2 capture and MPP encoding...")

	// Open V4L2 device
	v4l2 := C.v4l2_capture_open(C.CString(config.V4L2Device), C.int(config.VideoWidth), C.int(config.VideoHeight))
	if v4l2 == nil {
		// For now, use file input as fallback
		log.Println("V4L2 capture not implemented, using test pattern")
		cp.generateTestPattern()
		return
	}
	defer C.v4l2_capture_close(v4l2)

	// Frame buffer (YUV420SP size)
	frameSize := config.VideoWidth * config.VideoHeight * 3 / 2
	frameBuffer := make([]byte, frameSize)

	ticker := time.NewTicker(time.Second / time.Duration(config.VideoFPS))
	defer ticker.Stop()

	for {
		select {
		case <-cp.stopCh:
			return
		case <-ticker.C:
			// Read frame from V4L2
			ret := C.v4l2_capture_read(v4l2, unsafe.Pointer(&frameBuffer[0]), C.int(frameSize))
			if ret < 0 {
				log.Printf("Failed to read V4L2 frame")
				continue
			}

			// Send to MPP encoder
			pts := time.Now().UnixMilli()
			C.mpp_venc_send_frame(cp.venc, unsafe.Pointer(&frameBuffer[0]), C.int(frameSize), C.longlong(pts))

			// Get encoded packet
			var data unsafe.Pointer
			var size C.int
			var outPts C.longlong
			if C.mpp_venc_get_packet(cp.venc, &data, &size, &outPts) == 0 {
				frameData := C.GoBytes(data, size)
				cp.sendEncodedFrame(frameData, int64(outPts))
			}
		}
	}
}

// generateTestPattern generates test video pattern (fallback)
func (cp *CameraPeerRV1106) generateTestPattern() {
	log.Println("Generating H.264 test pattern")

	// For testing without hardware, create a simple pattern
	// In production, this would be replaced with actual MPP encoded frames
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-cp.stopCh:
			return
		case <-ticker.C:
			// Placeholder: In production, MPP encoder would provide frames
			// For now, this is a stub
		}
	}
}

// sendEncodedFrame sends encoded frame to channel for WebRTC streaming
func (cp *CameraPeerRV1106) sendEncodedFrame(data []byte, pts int64) {
	select {
	case cp.frameCh <- data:
	default:
		// Drop frame if channel is full
	}
}

// streamVideo sends encoded H.264 frames to WebRTC track
func (cp *CameraPeerRV1106) streamVideo() {
	log.Println("Starting video streaming to WebRTC")

	for {
		select {
		case <-cp.stopCh:
			return
		case frame := <-cp.frameCh:
			if err := cp.videoTrack.WriteSample(media.Sample{
				Data:     frame,
				Duration: time.Second / time.Duration(config.VideoFPS),
			}); err != nil {
				log.Printf("Failed to write video sample: %v", err)
			}
		}
	}
}

// streamAudio captures audio and sends via WebRTC
func (cp *CameraPeerRV1106) streamAudio() {
	log.Println("Audio streaming started (ALSA capture)")

	// ALSA audio capture would go here
	// For now, this is a placeholder
	// Production: Use ALSA API or similar to capture from microphone

	<-cp.stopCh
	log.Println("Audio streaming stopped")
}

// playRemoteAudio plays audio received from remote peer (two-way audio)
func (cp *CameraPeerRV1106) playRemoteAudio(track *webrtc.TrackRemote) {
	log.Println("Playing remote audio from viewer")

	// ALSA audio playback would go here
	// For now, read and discard (placeholder for two-way audio)

	for {
		select {
		case <-cp.stopCh:
			return
		default:
		}

		_, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("Error reading remote audio: %v", err)
			return
		}
	}
}

// initFaceRecognition starts the Python face recognition wrapper with stderr capture
func (cp *CameraPeerRV1106) initFaceRecognition() error {
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
			log.Printf("[Python stderr] read error: %v", err)
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

	select {
	case success := <-done:
		if !success {
			cp.stopFaceRecognition()
			return initError
		}
	case <-time.After(10 * time.Second):
		cp.stopFaceRecognition()
		return fmt.Errorf("timeout waiting for face recognition to initialize")
	}

	log.Println("Face recognition initialized successfully")
	return nil
}

// stopFaceRecognition stops the Python wrapper
func (cp *CameraPeerRV1106) stopFaceRecognition() {
	if cp.faceRecogStdin != nil {
		cp.faceRecogStdin.Close()
	}
	if cp.faceRecogCmd != nil && cp.faceRecogCmd.Process != nil {
		cp.faceRecogCmd.Process.Kill()
		cp.faceRecogCmd.Wait()
	}
}

// processFaceRecognition continuously processes frames for face recognition
func (cp *CameraPeerRV1106) processFaceRecognition() {
	ticker := time.NewTicker(FaceRecogFrameInterval)
	defer ticker.Stop()

	for {
		select {
		case <-cp.stopCh:
			return
		case <-ticker.C:
			cp.processSingleFrame()
		}
	}
}

// processSingleFrame sends one frame to face recognition and handles result
func (cp *CameraPeerRV1106) processSingleFrame() {
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

	// Send identification data to signaling server
	if len(response.Persons) > 0 {
		cp.sendIdentificationData(response.Persons, response.Timestamp)
	}
}

// sendIdentificationData sends identified persons to viewers via signaling server
func (cp *CameraPeerRV1106) sendIdentificationData(persons []PersonInfo, timestamp int64) {
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
