# WebRTC Video Camera with Face Recognition

Two Go WebRTC camera implementations with face recognition integration via Python wrapper.

## Structure

```
webrtc_video/
├── laptop/              # Laptop development version
│   ├── main.go         # Go program for laptop (pipe-based H.264 input)
│   ├── go.mod
│   └── go.sum
├── rv1106/             # RV1106 hardware version
│   ├── main.go         # Go program for RV1106 (V4L2 + MPP hardware encoding)
│   ├── go.mod
│   └── go.sum
├── face_recog_wrapper.py    # Python wrapper for face recognition
└── known_faces/             # Directory for known face images
```

## Features

- **WebRTC peer-to-peer video streaming**
- **Face recognition** using Python wrapper with ONNX models
- **H.264 hardware encoding** (RV1106 via MPP)
- **Signaling server integration** (`vps_server.js`)
- **Identification data** sent to viewers via signaling

## Laptop Version

### Build (Windows/Linux/macOS)

```bash
cd laptop
go mod tidy
go build .
```

### Usage

```bash
# Read H.264 from file
./webrtc_video -room=ROOM_ID -video=input.h264 -facerecog=true

# Read H.264 from stdin (pipe from ffmpeg/gstreamer)
ffmpeg -i input.mp4 -c:v libx264 -f h264 - | ./webrtc_video -room=ROOM_ID -video=-
```

### Windows - Capture from Webcam with Audio

First, list available devices:
```powershell
# List video devices
ffmpeg -hide_banner -list_devices true -f dshow -i dummy

# List audio devices
ffmpeg -hide_banner -list_devices true -f dshow -i dummy | findstr Audio
```

Then capture with ffmpeg (example with common device names):
```powershell
# Capture from webcam with audio
ffmpeg -f dshow -i video="USB2.0 HD UVC WebCam" -f dshow -i audio="Microphone Array (Realtek(R) Audio)" -c:v libx264 -preset fast -f h264 - | .\webrtc_video.exe -room=ROOM_ID -video=- -audio=true

# Or use default devices
ffmpeg -f dshow -i video=0 -f dshow -i audio=0 -c:v libx264 -preset fast -f h264 - | .\webrtc_video.exe -room=ROOM_ID -video=- -audio=true
```

### Config File and CLI Overrides

Use a JSON config file:
```bash
./webrtc_video -room=ROOM_ID -c config.json
```

Override specific options via CLI:
```bash
./webrtc_video -room=ROOM_ID -C SignalingServerURL="ws://192.168.1.100:3000" -C CameraName="office_cam"
```

Copy `config.example.json` to `config.json` and customize with your Metered TURN credentials.

### Flags

**Required:**
- `-room`: Room ID to join

**Options:**
- `-video`: Video input file (`-` for stdin, default: `-`)
- `-facerecog`: Enable face recognition (default: `true`)
- `-audio`: Enable audio (requires separate audio source, default: `false`)
- `-c, -config`: Path to JSON config file
- `-C, -config-key`: Set config value (key=value format, can be used multiple times)
- `-h, -help`: Show help

**Config Keys for -C option:**
- `SignalingServerURL`: WebSocket URL of signaling server
- `StunServerURL`: STUN server for WebRTC
- `CameraName`: Identifier for this camera
- `PythonCompiler`: Path to Python executable
- `FaceRecogScript`: Path to face recognition wrapper script

## RV1106 Version

### Cross-Compilation for RV1106 (ARM Linux)

Requires ARM cross-compiler with CGO support:

**Linux/macOS:**
```bash
export CC=arm-linux-gnueabihf-gcc
export CXX=arm-linux-gnueabihf-g++
export CGO_ENABLED=1
export GOOS=linux
export GOARCH=arm
export GOARM=7
export CGO_CFLAGS="-I/path/to/mpp/include"
export CGO_LDFLAGS="-L/path/to/mpp/lib -lrockchip_mpp -lm -lpthread"
cd rv1106
go mod tidy
go build .
```

**Windows PowerShell:**
```powershell
$env:CC = "arm-linux-gnueabihf-gcc"
$env:CXX = "arm-linux-gnueabihf-g++"
$env:CGO_ENABLED = "1"
$env:GOOS = "linux"
$env:GOARCH = "arm"
$env:GOARM = "7"
$env:CGO_CFLAGS = "-I/path/to/mpp/include"
$env:CGO_LDFLAGS = "-L/path/to/mpp/lib -lrockchip_mpp -lm -lpthread"
cd rv1106
go mod tidy
go build .
```

### Build on RV1106 Device

```bash
cd rv1106
go mod tidy
go build .
```

### Usage

```bash
./webrtc_video -room=ROOM_ID
```

### Flags

**Required:**
- `-room`: Room ID to join

**Options:**
- `-device`: Video device path (default: `/dev/video0`)
- `-audio`: Enable audio capture (default: `true`)
- `-facerecog`: Enable face recognition (default: `true`)
- `-c, -config`: Path to JSON config file
- `-C, -config-key`: Set config value (key=value format, can be used multiple times)
- `-h, -help`: Show help

**Config Keys for -C option:**
- `SignalingServerURL`: WebSocket URL of signaling server
- `StunServerURL`: STUN server for WebRTC
- `CameraName`: Identifier for this camera
- `PythonCompiler`: Path to Python executable
- `FaceRecogScript`: Path to face recognition wrapper script
- `VideoWidth`: Capture width (default: `1920`)
- `VideoHeight`: Capture height (default: `1080`)
- `VideoFPS`: Capture framerate (default: `30`)
- `V4L2Device`: V4L2 video device path (default: `/dev/video0`)

## Face Recognition

The `face_recog_wrapper.py` communicates with Go programs via JSON over stdin/stdout:

### Protocol

**Go → Python (frame for analysis):**
```json
{"frame": "base64_jpeg_data", "timestamp": 1234567890}
```

**Python → Go (detection results):**
```json
{
  "persons": [
    {"name": "Alice", "confidence": 0.95, "bbox": {"x": 100, "y": 50, "w": 200, "h": 250}}
  ],
  "timestamp": 1234567890
}
```

### Setup

1. Place known face images in `known_faces/` directory (named `person_name.jpg`)
2. Ensure Python dependencies: `pip install opencv-python onnxruntime numpy`
3. Face recognition runs every 500ms on I-frames

## Signaling Server

Both programs connect to `vps_server.js` and send `identification_data` messages:

```json
{
  "type": "identification_data",
  "persons": [...],
  "timestamp": 1234567890
}
```

The server broadcasts to all viewers in the room.

## Configuration

Configuration can be set via:

1. **JSON config file** (`-config=config.json`) - Copy `config.example.json` as starting point
2. **CLI flags** (override config file values)
3. **Default values** (compiled into the program)

See `config.example.json` for Metered TURN server setup with authentication.

## Dependencies

### Go
- `github.com/pion/webrtc/v4` - WebRTC implementation
- `github.com/gorilla/websocket` - WebSocket client

### Python (face recognition)
- `opencv-python` - Image processing
- `onnxruntime` - ONNX model inference
- `numpy` - Numerical operations

### RV1106 Only
- Rockchip MPP libraries (hardware encoding)
- V4L2 for video capture
- ALSA for audio (placeholder)
