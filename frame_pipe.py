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
    # print("\nCleaning up...")
    cv2.destroyAllWindows()
    sys.exit(0)

def read_frames(pipe_path, width=0, height=0, channels=3, show_display=True, face_recog=lambda frame: []):
    """Read frames from pipe and display them in a window"""
    # If dimensions not specified, we'll need to detect them from the first frame
    frame_size = None
    frame_count = 0
    window_name = "Face Recognition Stream"

    # Set up signal handlers
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    # Create window for display
    if (show_display == True):
        cv2.namedWindow(window_name, cv2.WINDOW_AUTOSIZE)
        # print(f"Created display window: {window_name}")
        # print("Press 'q' to quit or close the window")

    try:
        # Open pipe for reading (handle stdin if "-" is specified)
        if pipe_path == "-":
            import sys
            pipe = sys.stdin.buffer
            # print("Connected to stdin pipe")
        elif pipe_path == "dummy" :
            import subprocess
            import shlex
            video_device = "Arducam USB Camera"
            command_str = (
                "ffmpeg -hide_banner -f dshow -pixel_format yuyv422 -video_size {width}x{height} "
                "-rtbufsize 10M -i video=\"{video_device}\" -f rawvideo -pix_fmt yuyv422 -r 5 "
                "pipe:1"
            ).format(
                width=width,
                height=height,
                video_device=video_device,
            )

            command = shlex.split(command_str)
            pipe = subprocess.Popen(command, stdout=subprocess.PIPE).stdout
        else :
            pipe = open(pipe_path, 'rb')
            # print(f"Connected to pipe: {pipe_path}")

        if not width or not height:
            # print("Waiting for first frame to detect dimensions...")
            # Smart detection - analyze frame boundaries
            # Read a moderate chunk to analyze patterns
            chunk = pipe.read(8192)  # 8K chunk should contain frame boundary

            if len(chunk) == 0:
                # print("End of pipe data")
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
                    # print(f"Auto-detected: {width}x{height} = {frame_size} bytes")
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
                # print(f"Estimated: {width}x{height} = {frame_size} bytes")
                # Read the rest of the first frame
                remaining_bytes = frame_size - len(chunk)
                remaining_data = pipe.read(remaining_bytes)
                frame_data = chunk + remaining_data
                # this singular frames is lost because user didn't input dimensions 😔 RIP

        frame_size = width * height * channels
        # print(f"Using provided dimensions: {width}x{height} = {frame_size} bytes")

        while True:
            frame_data = None  # Initialize frame_data
            frame_data = pipe.read(frame_size)

            if len(frame_data) == 0:
                if pipe_path != "-" and pipe_path != "dummy":
                    import time
                    time.sleep(0.03)
                    continue
                break

            if len(frame_data) != frame_size:
                # print(f"Incomplete frame: {len(frame_data)}/{frame_size} bytes")
                continue
            try:
                # Convert to numpy array for processing
                frame = np.frombuffer(frame_data, dtype=np.uint8)

                # Reshape based on dimensions and channels for face models
                if channels == 1:
                    frame = frame.reshape((height, width))
                elif channels == 2:
                    # YUYV422 format - convert to RGB
                    frame = frame.reshape((height, width, 2))
                    frame = cv2.cvtColor(frame, cv2.COLOR_YUV2BGR_YUYV)
                elif channels == 3:
                    # Convert to BGR for OpenCV display if needed
                    frame = cv2.cvtColor(frame, cv2.COLOR_RGB2BGR)
                elif channels == 4:
                    # Convert RGBA to BGR (drops the 4th Alpha channel safely)
                    frame = frame.reshape((height, width, 4))
                    frame = cv2.cvtColor(frame, cv2.COLOR_RGBA2BGR)
                else:
                    # Fallback for unknown multi-channel formats
                    frame = frame.reshape((height, width, channels))
                    # Slice to keep ONLY the first 3 channels
                    frame = frame[:, :, :3]
                try:
                    persons = face_recog(frame)
                    # print(f"Faces detected: {persons}")
                except Exception as e:
                    print(f"Failed to process captured frame {frame_count}: {e}")
                    break

                if show_display:
                    if persons:
                        for person in persons:
                            face = person["bbox"]
                            # Draw bounding box around detected face
                            x, y, w, h = int(face["x"]), int(face["y"]), int(face["w"]), int(face["h"])
                            cv2.rectangle(frame, (x, y), (x + w, y + h), (0, 255, 0), 2)

                            # Add label with confidence score
                            score = person.get("confidence", person.get("score", 0))
                            name = person["name"]
                            label = f"{name}: {score:.2f}"
                            cv2.putText(frame, label, (x, y - 10), cv2.FONT_HERSHEY_SIMPLEX, 0.5, (0, 255, 0), 2)

                    # Display the frame
                    cv2.imshow(window_name, frame)

                    # Check for quit key or window close
                    key = cv2.waitKey(1) & 0xFF
                    try:
                        if cv2.getWindowProperty(window_name, cv2.WND_PROP_VISIBLE) < 1:
                            # print("Quit requested by window close button")
                            break
                    except cv2.error:
                        # Catch instances where window was abruptly closed mid-cycle
                        break
                    if key == ord('q'):
                        # print("Quit requested by user")
                        break

            except Exception as e:
                print(f"Error displaying frame {frame_count}: {e}")
                break

            frame_count += 1
            # Simple motion detection simulation
            if frame_count % 100 == 0:
                avg_brightness = np.mean(frame)
                # print(f"Average brightness: {avg_brightness:.2f}")

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
            if pipe_path == "dummy":
                # pipe is a subprocess.Popen object
                pipe.terminate()
            else:
                pipe.close()

        try:
            os.close(sys.stdin.fileno())
        except:
            # print("Failed to close stdin")
            pass  # Already closed or error, ignore

        # if ffmpeg.pid file exist
        if os.path.exists("ffmpeg.pid"):
            # print("Killing ffmpeg process")
            try:
                with open("ffmpeg.pid", "r") as f:
                    pid = int(f.read())
                    os.kill(pid, 9)
                os.remove("ffmpeg.pid")
            except:
                pass
        elif pipe_path == "dummy":
            # Kill all ffmpeg process
            os.system("taskkill /f /IM ffmpeg.exe")

        cv2.destroyAllWindows()
        # print(f"Processed {frame_count} frames")

# # Terminal command example:
# ffmpeg -hide_banner -f dshow -pixel_format yuyv422 -video_size 1920x1080 -rtbufsize 10M -i video="Arducam USB Camera" -f `
# rawvideo -pix_fmt yuyv422 -r 5 pipe:1 | & "D:\Code_Main\Final_Year_Project\SBC\face_recog\.venv\Scripts\python.exe" -u `
# "d:\Code_Main\Final_Year_Project\SBC\webrtc_video\frame_pipe.py" "-" "yuyv422" "1920" "1080"

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
    format_type = "rgb24"
    width, height = None, None

    if len(sys.argv) >= 5:
        # Explicitly read out direct format, width, and height structure
        format_type = sys.argv[2].lower()
        width = int(sys.argv[3])
        height = int(sys.argv[4])
    elif len(sys.argv) == 3:
        arg2 = sys.argv[2].lower()
        if arg2 == 'verylow': width, height = 320, 240
        elif arg2 == 'low': width, height = 640, 480
        elif arg2 == 'medium': width, height = 1280, 720
        elif arg2 == 'high': width, height = 1920, 1080
        else: format_type = arg2

    if format_type == "grayscale":
        channels = 1
    elif format_type == "yuyv422":
        channels = 2
    else:
        channels = 3

    try:
        # print(f"Starting frame reader: {format_type} format, {width}x{height} resolution ({channels} channels)")
        read_frames(pipe_path, width=width, height=height, channels=channels)
    except Exception as e:
        print(f"Unexpected error: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)

if __name__ == "__main__":
    main()
