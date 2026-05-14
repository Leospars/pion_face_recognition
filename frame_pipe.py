#!/usr/bin/env python3
"""
Simple Python script to validate frame pipe input from FFmpeg.
Reads raw RGB24 or grayscale frames from pipe and displays basic info.
"""

import sys
import os
import numpy as np
import cv2
import signal

def signal_handler(sig, frame):
    """Handle Ctrl+C and other signals to clean up properly"""
    print("\nCleaning up...")
    cv2.destroyAllWindows()
    sys.exit(0)

def read_frames(pipe_path, width=None, height=None, channels=3, show_display=False, capture_frames=(lambda x: None)):
    """Read frames from pipe and display them in a window"""
    # If dimensions not specified, we'll need to detect them from the first frame
    frame_size = None
    frame_count = 0
    window_name = "Face Recognition Stream"

    if not width or not height:
        print("Width and height must be specified")
        return

    # Set up signal handlers
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    # Create window for display
    if (show_display == True):
        cv2.namedWindow(window_name, cv2.WINDOW_AUTOSIZE)
        print(f"Created display window: {window_name}")
        print("Press 'q' to quit or close the window")

    try:
        # Open pipe for reading (handle stdin if "-" is specified)
        if pipe_path == "-":
            import sys
            pipe = sys.stdin.buffer
            print("Connected to stdin pipe")
        else:
            pipe = open(pipe_path, 'rb')
            print(f"Connected to pipe: {pipe_path}")

        print("Press 'q' in the window to quit")
        print("Waiting for first frame to detect dimensions...")

        if not width or not height:
            # Smart detection - analyze frame boundaries
            # Read a moderate chunk to analyze patterns
            chunk = pipe.read(8192)  # 8K chunk should contain frame boundary

            if len(chunk) == 0:
                print("End of pipe data")
                return

            # Look for frame boundaries by analyzing byte patterns
            # Common frame sizes for RGB24:
            frame_sizes = [
                (1920, 1080, 6220800),  # Full HD
                (1280, 720, 2764800),   # HD
                (640, 480, 921600),     # VGA
            ]

            # Find the most likely frame size based on available data
            for test_w, test_h, test_size in frame_sizes:
                if len(chunk) >= test_size:
                    # We have at least one complete frame
                    width, height = test_w, test_h
                    frame_size = test_size
                    print(f"Auto-detected: {width}x{height} = {frame_size} bytes")
                    break

            # Fallback if no match found
            if frame_size is None:
                # Estimate based on chunk size
                if len(chunk) >= 6000:
                    width, height = 1920, 1080
                elif len(chunk) >= 2500:
                    width, height = 1280, 720
                else:
                    width, height = 640, 480
                frame_size = width * height * channels

                print(f"Estimated: {width}x{height} = {frame_size} bytes")
                # Read the rest of the first frame
                remaining_bytes = frame_size - len(chunk)
                remaining_data = pipe.read(remaining_bytes)
                frame_data = chunk + remaining_data
                # this singular frames is lost because user didn't input dimensions 😔 RIP
            else:
                frame_size = width * height * channels
                print(f"Using provided dimensions: {width}x{height} = {frame_size} bytes")

        while True:
            frame_data = None  # Initialize frame_data
            frame_data = pipe.read(frame_size)

            if len(frame_data) == 0:
                print("End of pipe data")
                break

            if len(frame_data) != frame_size:
                print(f"Incomplete frame: {len(frame_data)}/{frame_size} bytes")
                continue

            # Convert to numpy array for processing
            frame = np.frombuffer(frame_data, dtype=np.uint8)
            capture_frames(frame)

            if display_frame:
                # Reshape based on dimensions and channels
                if channels == 1:
                    frame = frame.reshape((height, width))
                elif channels == 2:
                    # YUYV422 format - convert to RGB
                    frame = frame.reshape((height, width, 2))
                    frame = cv2.cvtColor(frame, cv2.COLOR_YUV2BGR_YUYV)
                else:
                    frame = frame.reshape((height, width, channels))

                # Convert to BGR for OpenCV display if needed
                if channels == 3:
                    frame = cv2.cvtColor(frame, cv2.COLOR_RGB2BGR)

                print(f"Frame {frame_count}: {frame.shape} ({'RGB' if channels == 3 else 'Grayscale' if channels == 1 else 'YUYV422'})")
                display_frame = frame
                # Display the frame
                cv2.imshow(window_name, display_frame)
            frame_count += 1

            # Check for quit key or window close
            key = cv2.waitKey(1) & 0xFF
            if key == ord('q') or cv2.getWindowProperty(window_name, cv2.WND_PROP_VISIBLE) < 1:
                print("Quit requested by user")
                break

            # Simple motion detection simulation
            if frame_count % 100 == 0:
                avg_brightness = np.mean(frame)
                print(f"Average brightness: {avg_brightness:.2f}")

    except FileNotFoundError:
        print(f"Error: Pipe '{pipe_path}' not found")
        print("Make sure FFmpeg is running and writing to this pipe")
        sys.exit(1)
    except Exception as e:
        print(f"Error reading from pipe: {e}")
        sys.exit(1)
    finally:
        # Clean up
        if pipe_path != "-":
            pipe.close()

        try:
            os.close(sys.stdin.fileno())
        except:
            print("Failed to close stdin")
            pass  # Already closed or error, ignore

        cv2.destroyAllWindows()
        print(f"Processed {frame_count} frames")
        # if ffmpeg.pid file exist
        if os.path.exists("ffmpeg.pid"):
            print("Killing ffmpeg process")
            with open("ffmpeg.pid", "r") as f:
                pid = int(f.read())
                os.kill(pid, 9)
            os.remove("ffmpeg.pid")
        else:
            # Kill all ffmpeg process
            os.system("taskkill /f /IM ffmpeg.exe")

# (Start-Process "ffmpeg.exe" "-hide_banner -f dshow -pixel_format yuyv422 -video_size 1920x1080 -rtbufsize 10M -i video=@device_pnp_\\?\usb#vid_0c45&pid_6366&mi_00#6&d2e721e&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global -f rawvideo -pix_fmt yuyv422 -r 5 pipe:1" -PassThru -NoNewWindow).WaitForExit(10000) | & "D:\Code_Main\Final_Year_Project\SBC\face_recog\.venv\Scripts\python.exe" -u "d:\Code_Main\Final_Year_Project\SBC\webrtc_video\play-from-disk\frame_pipe.py" "-" "yuyv422" "1920" "1080"

def main():
    if len(sys.argv) < 2:
        print("Usage: python frame_pipe.py <pipe_path> [quality] [width] [height]")
        print(r"Example: python frame_pipe.py \\.\pipe\face_recog_pipe verylow")
        print(r"Example: python frame_pipe.py \\.\pipe\face_recog_pipe low")
        print(r"Example: python frame_pipe.py \\.\pipe\face_recog_pipe medium 1280 720")
        print(r"Example: python frame_pipe.py \\.\pipe\face_recog_pipe high 1920 1080")
        print("Quality presets: verylow(320x240), low(640x480), medium(1280x720), high(1920x1080)")
        print("Format: rgb24 or grayscale (default: rgb24)")
        sys.exit(1)

    pipe_path = sys.argv[1]

    # Parse quality preset or format
    if len(sys.argv) > 2:
        arg2 = sys.argv[2].lower()
        if arg2 in ['verylow', 'low', 'medium', 'high']:
            # Quality preset
            if arg2 == 'verylow':
                width, height = 320, 240
            elif arg2 == 'low':
                width, height = 640, 480
            elif arg2 == 'medium':
                width, height = 1280, 720
            elif arg2 == 'high':
                width, height = 1920, 1080
            format_type = sys.argv[3] if len(sys.argv) > 3 else "rgb24"
        else:
            # Format specified
            format_type = arg2
            width = int(sys.argv[3]) if len(sys.argv) > 3 else None
            height = int(sys.argv[4]) if len(sys.argv) > 4 else None
    else:
        format_type = "rgb24"
        width, height = None, None

    # Determine channels based on format
    if format_type == "grayscale":
        channels = 1
    elif format_type == "yuyv422":
        channels = 2  # YUYV422 uses 2 bytes per pixel
    else:
        channels = 3  # RGB24 uses 3 bytes per pixel

    print(f"Starting frame reader: {format_type} format, {width or 'auto'}x{height or 'auto'} resolution")
    read_frames(pipe_path, width=width, height=height, channels=channels)

if __name__ == "__main__":
    main()
