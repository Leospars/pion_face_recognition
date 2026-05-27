# Stream.go - Live Camera Streaming with Face Recognition

This enhanced version of stream.go provides live camera streaming with dual FFmpeg output for WebRTC and face recognition pipeline integration.

## Overview

`stream.go` transforms the original file-based streaming into a live camera streaming solution that:
- Captures video from camera devices in real-time
- Outputs IVF format for WebRTC streaming
- Simultaneously extracts frames for face recognition processing
- Integrates with WebSocket signaling server for automatic WebRTC setup
- Supports both Windows and Linux platforms

## Quick Start

### Basic Usage
```bash
# Start streaming with default camera
go run stream.go -room=YOUR_ROOM_ID

# Enable face recognition with RGB frames
go run stream.go -room=YOUR_ROOM_ID -C FaceRecogEnabled=true -C FaceRecogFormat=rgb24

# Use specific camera device
go run stream.go -room=YOUR_ROOM_ID -C VideoDevice="USB2.0 HD UVC WebCam"
```

### Configuration File
Create `config.json`:
```json
{
  "signalingServerUrl": "ws://192.168.56.1:3000",
  "videoDevice": "/dev/video0",
  "faceRecogEnabled": true,
  "faceRecogFormat": "rgb24",
  "faceRecogPipe": "face_recog_pipe",
  "videoCodec": "libvpx",
  "videoWidth": 1280,
  "videoHeight": 720,
  "videoFPS": 30,
  "videoBitrate": "1M"
}
```

Then run:
```bash
go run stream.go -room=YOUR_ROOM_ID -c config.json
```

## Architecture

```
Camera Device → FFmpeg (tee filter) → [IVF pipe] + [Raw frame file]
                                      ↓                    ↓
                                    WebRTC streaming      Face recognition
```

### Pipeline Components

1. **Camera Capture**: FFmpeg captures from camera device
2. **Dual Output**: Tee filter splits stream into two outputs
3. **IVF Pipeline**: Encodes to VP8 for WebRTC compatibility
4. **Frame Pipeline**: Extracts YUV420 frames to file for face recognition
5. **WebRTC**: Streams video to browser via signaling server
6. **Face Recognition**: Reads frames from file for person detection

### Codec Configuration

- **WebRTC**: VP8 (libvpx) + IVF format for compatibility with ivfreader
- **Face Recognition**: YUV420 format (1.5 bytes per pixel, more efficient than RGB24)
- **Platform**: Cross-platform file-based approach (Windows & Linux)

## Command Line Options

| Option | Description | Example |
|--------|-------------|---------|
| `-room` | Room ID to join (required) | `-room=abc123` |
| `-config` | Path to JSON config file | `-c config.json` |
| `-C` | Set config value (multiple allowed) | `-C VideoDevice=/dev/video0` |

### Configurable Settings

| Key | Default | Description |
|-----|---------|-------------|
| `SignalingServerUrl` | `ws://192.168.56.1:3000` | WebSocket server URL |
| `VideoDevice` | `/dev/video0` | Camera device path |
| `VideoCodec` | `libvpx` | FFmpeg codec (VP8 for IVF) |
| `VideoWidth` | `1280` | Video resolution width |
| `VideoHeight` | `720` | Video resolution height |
| `VideoFPS` | `30` | WebRTC frame rate |
| `VideoBitrate` | `1M` | Video bitrate |
| `FaceRecogEnabled` | `false` | Enable face recognition |
| `FaceRecogFormat` | `rgb24` | Frame format (rgb24/gray) |
| `FaceRecogPipe` | `face_recog_pipe` | Named pipe name |
| `TestPipeOnly` | `false` | Test FFmpeg without WebRTC |

## Platform-Specific Setup

### Windows
```powershell
# List available cameras
ffmpeg -list_devices true -f dshow -i dummy

# list supported qualities
ffmpeg -hide_banner -list_options true -f dshow -i video="video_device_name"  
ffmpeg -hide_banner -list_options true -f dshow -i audio="audio_device_name"  

# Start streaming with face recognition
go run stream.go -room=test -C VideoDevice="USB2.0 HD UVC WebCam" -C FaceRecogEnabled=true

# Test pipe output only
go run stream.go -room=test -C TestPipeOnly=true -C FaceRecogEnabled=true
```

### Linux
```bash
# List available cameras
ls /dev/video*

# Start streaming with default camera
go run stream.go -room=test -C VideoDevice=/dev/video0 -C FaceRecogEnabled=true

# High quality streaming
go run stream.go -room=hq -C VideoWidth=1920 -C VideoHeight=1080 -C VideoBitrate=2M
```

## FFmpeg Integration

The stream.go application generates FFmpeg commands dynamically based on platform, codec selection, and face recognition mode. It supports two video codec modes: **IVF (VP8/VP9)** for cross-platform compatibility and **H.264** for lower bandwidth requirements.

### Video Codec Comparison

| Feature | IVF (VP8) | H.264 |
|---------|-----------|--------|
| **Format** | IVF container | Raw NAL stream |
| **Bitrate** | ~1-2M for 720p@30fps | ~500k-1M for 720p@30fps |
| **Compatibility** | Universal WebRTC | Most browsers/devices |
| **Quality at same bitrate** | Excellent | Good |
| **CPU Usage** | Higher | Lower |
| **Best for** | Streaming servers | Mobile/embedded |

### Adaptive Bitrate with CRF and VBR

Stream.go uses two complementary rate control mechanisms:

**CRF (Constant Rate Factor)**
- Adjusts quality dynamically around a target bitrate
- `-crf 23`: Quality level (0-51, lower = higher quality)
- Lower values use more bandwidth, higher values reduce quality
- Sweet spot: 18-28 for video streaming

**VBR (Variable Bit Rate)**
- Sets bitrate boundaries to prevent extremes
- `-b:v 1M`: Target bitrate (1Mbps nominal)
- `-minrate 500k`: Don't go below 500kbps (minimum quality)
- `-maxrate 2M`: Don't exceed 2Mbps (bandwidth cap)
- `-bufsize 2M`: Buffer size for rate control smoothing

**Combined Effect**: Video maintains consistent quality while adapting to network conditions between min/max boundaries.

### Platform & Mode Specific Commands

#### 1. Windows with VP8/IVF + Face Recognition
```powershell
ffmpeg -y -f dshow -i video="USB2.0 HD UVC WebCam" -rtbufsize 64M -thread_queue_size 1024 `
  -pixel_format nv12 `
  -f dshow -i audio="Microphone" `
  -filter_complex "split=2[v1][v2];[v1]copy[v1out];[v2]scale=320:320:flags=fast_bilinear,format=nv12,fps=5[v2out]" `
  -map "[v1out]" -c:v libvpx -crf 23 -b:v 1M -minrate 500k -maxrate 2M -bufsize 2M -g 30 -keyint_min 30 -f ivf pipe:1 `
  -map "[v2out]" -f rawvideo face_recog_frames.raw `
  -map 1:a -c:a libopus -b:a 48k -minrate 32k -maxrate 64k -ar 48000 -ac 2 -f opus pipe:2
```

**What this does:**
- Captures video from USB camera (dshow format)
- Captures audio from microphone
- Splits video into two streams using filter_complex
- Stream 1: Full resolution, encoded as VP8 IVF to pipe:1 (WebRTC)
- Stream 2: Scaled to 320x320 at 5 FPS, raw YUV420 to file (face recognition)
- Audio: Encoded as Opus to pipe:2 (WebRTC)

#### 2. Windows with H.264 + Face Recognition
```powershell
ffmpeg -y -f dshow -i video="USB2.0 HD UVC WebCam" -rtbufsize 64M -thread_queue_size 1024 \
  -pixel_format nv12 \
  -f dshow -i audio="Microphone" \
  -filter_complex "split=2[v1][v2];[v1]copy[v1out];[v2]scale=320:320:flags=fast_bilinear,format=nv12,fps=5[v2out]" \
  -map "[v1out]" -c:v libx264 -crf 23 -b:v 1M -minrate 500k -maxrate 2M -bufsize 2M -g 30 -keyint_min 30 -f h264 pipe:1 \
  -map "[v2out]" -f rawvideo face_recog_frames.raw \
  -map 1:a -c:a libopus -b:a 48k -minrate 32k -maxrate 64k -ar 48000 -ac 2 -f opus pipe:2
```

**What this does:**
- Same capture as VP8 version, but uses libx264 H.264 encoder
- H.264 format outputs raw NAL units (not wrapped in IVF container)
- Lower bitrate requirement (~20% less bandwidth than VP8 at same quality)
- Better compatibility with mobile and embedded devices

#### 3. Linux with VP8/IVF + Face Recognition
```bash
ffmpeg -y -f v4l2 -i /dev/video0 -rtbufsize 64M -thread_queue_size 1024 \
  -input_format nv12 \
  -f pulse -i default \
  -filter_complex "split=2[v1][v2];[v1]copy[v1out];[v2]scale=320:320:flags=fast_bilinear,format=nv12,fps=5[v2out]" \
  -map "[v1out]" -c:v libvpx -crf 23 -b:v 1M -minrate 500k -maxrate 2M -bufsize 2M -g 30 -keyint_min 30 -f ivf pipe:1 \
  -map "[v2out]" -f rawvideo face_recog_frames.raw \
  -map 1:a -c:a libopus -b:a 48k -minrate 32k -maxrate 64k -ar 48000 -ac 2 -f opus pipe:2
```

**What this does:**
- Captures from Linux V4L2 camera interface
- Audio from PulseAudio (default device)
- Input format preference: NV12 for efficiency
- Same dual-stream split as Windows version
- Output to pipes for cross-platform pipe handling

#### 4. Linux with H.264 + Face Recognition
```bash
ffmpeg -y -f v4l2 -i /dev/video0 -rtbufsize 64M -thread_queue_size 1024 \
  -input_format nv12 \
  -f pulse -i default \
  -filter_complex "split=2[v1][v2];[v1]copy[v1out];[v2]scale=320:320:flags=fast_bilinear,format=nv12,fps=5[v2out]" \
  -map "[v1out]" -c:v libx264 -crf 23 -b:v 1M -minrate 500k -maxrate 2M -bufsize 2M -g 30 -keyint_min 30 -f h264 pipe:1 \
  -map "[v2out]" -f rawvideo face_recog_frames.raw \
  -map 1:a -c:a libopus -b:a 48k -minrate 32k -maxrate 64k -ar 48000 -ac 2 -f opus pipe:2
```

**What this does:**
- Same V4L2 capture as VP8 version with H.264 encoding
- Better for embedded systems and mobile streaming
- Reduced bandwidth while maintaining good quality

### Input Parameters Explained

**Video Capture:**
- `-y`: Overwrite output files without asking
- `-f dshow` / `-f v4l2`: Input format (Windows DirectShow / Linux Video4Linux2)
- `-i video="Camera_Name"` / `-i /dev/video0`: Video input device
- `-rtbufsize 64M`: Real-time buffer size (64MB prevents frame drops on busy systems)
- `-thread_queue_size 1024`: Queue size for input threads (higher = more memory, less frame drops)

**Format Preferences:**
- `-pixel_format nv12` (Windows dshow): Preferred camera output format
- `-input_format nv12` (Linux v4l2): Preferred camera input format
- NV12 is superior to YUYV422: same quality, 50% less bandwidth

**Audio Input:**
- `-f dshow` (Windows): DirectShow audio capture
- `-f pulse` (Linux): PulseAudio capture
- `-i "Microphone"` or `-i default`: Audio device selection

### Video Encoding Parameters

**Codec Selection:**
- `-c:v libvpx`: VP8 encoder (IVF output)
- `-c:v libvpx-vp9`: VP9 encoder (more efficient VP8)
- `-c:v libx264`: H.264 encoder (best compatibility)
- `-c:v h264_nvenc`: NVIDIA GPU acceleration (Windows/Linux)
- `-c:v hevc_nvenc`: NVIDIA H.265 encoder (newer, smaller files)

**Rate Control (CRF + VBR):**
- `-crf 23`: Constant Rate Factor (quality: 0=lossless, 51=worst, 23=good balance)
- `-b:v 1M`: Target bitrate (1Mbps)
- `-minrate 500k`: Minimum bitrate floor (never drop below this quality)
- `-maxrate 2M`: Maximum bitrate ceiling (never exceed this bandwidth)
- `-bufsize 2M`: Encoder buffer size for rate control smoothing

**Keyframe Control (critical for streaming):**
- `-g 30`: GOP size - keyframe every 30 frames (1 sec at 30fps)
- `-keyint_min 30`: Minimum keyframe interval
- Keyframes required for: stream start, seeking, error recovery

**Output Format:**
- `-f ivf`: IVF container format (VP8/VP9 for WebRTC)
- `-f h264`: Raw H.264 NAL units (not wrapped in container)
- `pipe:1`: Output to stdout (pipe 1)

### Face Recognition Stream Parameters

**Filter Complex:**
```
split=2[v1][v2];[v1]copy[v1out];[v2]scale=320:320:flags=fast_bilinear,format=nv12,fps=5[v2out]
```
- `split=2[v1][v2]`: Split input into two independent streams
- `[v1]copy[v1out]`: Stream 1 - copy unchanged (pass-through to WebRTC)
- `[v2]scale=320:320`: Stream 2 - scale to 320x320 for face recognition
- `flags=fast_bilinear`: Fast scaling algorithm (good quality/speed tradeoff)
- `format=nv12`: Convert to NV12 format (efficient for face models)
- `fps=5`: Reduce to 5 FPS (enough for face detection, saves CPU)

**Output Mapping:**
- `-map "[v1out]"`: Map stream 1 to main video output (WebRTC)
- `-map "[v2out]"`: Map stream 2 to face recognition file
- `-f rawvideo`: Output raw pixels (no container)
- `face_recog_frames.raw`: File path (cross-platform compatible)

### Audio Encoding Parameters

**Codec and Basic Settings:**
- `-c:a libopus`: Opus audio codec (best for VoIP/streaming)
- `-b:a 48k`: Target audio bitrate (48kbps)
- `-ar 48000`: Audio sample rate (48kHz, WebRTC standard)
- `-ac 2`: Audio channels (stereo)

**Adaptive Audio Bitrate:**
- `-minrate 32k`: Minimum audio bitrate (lower quality for constrained bandwidth)
- `-maxrate 64k`: Maximum audio bitrate (full quality)
- Opus automatically adjusts between min/max based on content

**Output:**
- `-f opus`: Opus container format
- `pipe:2`: Output to pipe 2 (stderr equivalent, separate from video pipe:1)

### Pipe Outputs

FFmpeg outputs to three locations simultaneously:

1. **pipe:1** - Main video stream (to WebRTC)
   - IVF or H.264 format
   - Full resolution
   - 30fps (configurable)

2. **Face recognition file** - Secondary processed video
   - Raw YUV420 pixel data
   - Scaled to 320x320
   - 5fps (independent of main stream)
   - Suitable for machine learning models

3. **pipe:2** - Audio stream (to WebRTC)
   - Opus encoded
   - 48kHz stereo
   - Adaptive bitrate

### Dual Output Commands (Legacy)

**Windows (Named Pipes):**
```powershell
ffmpeg -f dshow -i video="USB2.0 HD UVC WebCam" `
  -filter_complex "split=2[v1][v2];[v1]copy[v1out];[v2]scale=320:320:flags=fast_bilinear,format=rgb24,fps=5[v2out]" `
  -map "[v1out]" -c:v libvpx -b:v 1M -f ivf pipe:1 `
  -map "[v2out]" -f rawvideo \\.\pipe\face_recog_pipe
```

**Linux (Pipe Numbers):**
```bash
ffmpeg -f v4l2 -i /dev/video0 \
  -filter_complex "split=2[v1][v2];[v1]copy[v1out];[v2]scale=320:320:flags=fast_bilinear,format=rgb24,fps=5[v2out]" \
  -map "[v1out]" -c:v libvpx -b:v 1M -f ivf pipe:1 \
  -map "[v2out]" -f rawvideo pipe:4
```

## Face Recognition Testing

### Python Validation Script
Use the included `frame_pipe.py` to test dual pipe output:

```bash
# Test RGB24 frames on Windows
python frame_pipe.py \\.\pipe\face_recog_pipe rgb24

# Test grayscale frames on Linux
python frame_pipe.py /tmp/face_pipe gray
```

### Manual Testing
```bash
# Test FFmpeg dual output directly
ffmpeg -f dshow -i video="Your Camera" \
  -filter_complex "split=2[v1][v2];[v1]copy[v1out];[v2]scale=320:320:flags=fast_bilinear,format=rgb24,fps=5[v2out]" \
  -map "[v1out]" -c:v libvpx -b:v 1M -f ivf pipe:1 \
  -map "[v2out]" -f rawvideo \\.\pipe\test_pipe
```

## Signaling Server Integration

The application automatically connects to a WebSocket signaling server for:
- Room joining/management
- WebRTC offer/answer exchange
- ICE candidate negotiation
- Connection state monitoring

### Server Requirements
- WebSocket server on specified port
- Room-based communication support
- JSON message format
- ICE candidate forwarding

## Error Handling

### FFmpeg Recovery
- Automatic restart on FFmpeg crash
- Process monitoring and health checks
- Graceful shutdown on SIGINT/SIGTERM

### Pipe Management
- Robust pipe connection handling
- Timeout and retry logic
- Cross-platform pipe compatibility

### WebRTC Resilience
- Connection state monitoring
- Automatic reconnection attempts
- ICE candidate error handling

## Troubleshooting

### Common Issues

**Camera not found:**
```bash
# Linux
ls /dev/video*

# Windows
ffmpeg -list_devices true -f dshow -i dummy
```

**Permission denied:**
- Run as administrator (Windows)
- Check camera permissions
- Verify device access rights

**FFmpeg errors:**
- Check FFmpeg installation: `ffmpeg -version`
- Verify codec support: `ffmpeg -codecs | grep libvpx`
- Test camera capture separately

**WebRTC connection issues:**
- Verify signaling server is running
- Check network connectivity
- Test STUN server accessibility

**Face recognition pipe issues:**
- Use Python test script to validate
- Check for pipe name conflicts
- Verify FFmpeg dual output

### Debug Mode
Enable verbose logging:
```bash
go run stream.go -room=test -C FaceRecogEnabled=true 2>&1 | tee debug.log
```

## Dependencies

### Required
- **Go 1.19+**: For compilation
- **FFmpeg**: Camera capture and encoding
- **Signaling Server**: WebSocket server for WebRTC

### Optional
- **Python 3.x**: For frame validation testing
- **NumPy**: Required by Python test script

## Performance Considerations

### Resource Usage
- **CPU**: FFmpeg encoding (VP8) + scaling
- **Memory**: Frame buffers + pipe management
- **Network**: WebRTC streaming overhead

### Optimization Tips
- Adjust video bitrate based on network conditions
- Use appropriate resolution for bandwidth constraints
- Consider hardware acceleration if available
- Monitor FFmpeg process resource usage

## Security Considerations

### Network Security
- Use secure WebSocket (wss://) in production
- Implement authentication for signaling server
- Validate room access permissions

### System Security
- Limit camera device access
- Secure named pipe permissions
- Monitor FFmpeg process execution

## Examples

### Development Setup
```bash
# Test with local camera
go run stream.go -room=dev -C VideoDevice="Integrated Camera"

# Enable face recognition for testing
go run stream.go -room=dev -C FaceRecogEnabled=true -C FaceRecogFormat=rgb24
```

### Production Deployment
```bash
# Production config with high quality
go run stream.go -room=production -c production.json

# Multiple cameras (different rooms)
go run stream.go -room=camera1 -C VideoDevice=/dev/video0 &
go run stream.go -room=camera2 -C VideoDevice=/dev/video1 &
```

### Testing Scenarios
```bash
# Test dual pipe without WebRTC
go run stream.go -room=test -C TestPipeOnly=true -C FaceRecogEnabled=true

# Low bandwidth configuration
go run stream.go -room=lowbw -C VideoWidth=640 -C VideoHeight=480 -C VideoBitrate=500k
```

## API Reference

### Main Functions

- `main()`: Entry point, handles CLI parsing and initialization
- `CameraPeer.Run()`: Main execution loop for camera streaming
- `startDualFFmpeg()`: Launches FFmpeg with tee filter
- `readIVFPipe()`: Reads IVF data for WebRTC
- `readFramePipe()`: Reads raw frames for face recognition

### Configuration Structures

- `Config`: Main configuration container
- `CameraPeer`: WebRTC peer management
- `SignalingMessage`: WebSocket message format
- `ICEServer`: STUN/TURN server configuration

## Contributing

When modifying stream.go:
1. Maintain cross-platform compatibility
2. Test both Windows and Linux
3. Validate FFmpeg command generation
4. Test face recognition pipeline
5. Update documentation accordingly

## TODO

- Fix face_recog_frames.raw file not deleting because of ffmpeg remoain using file on close.
- Fix audio not sending over
- Improve resource management for lower latency video transmission
