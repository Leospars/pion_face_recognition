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

### Dual Output Commands

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

### Frame Format Options

- **RGB24**: Full color frames (320x320x3 bytes)
- **Grayscale**: Black and white frames (320x320 bytes)
- **Frame Rate**: 5 FPS for face recognition (independent of WebRTC FPS)

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
