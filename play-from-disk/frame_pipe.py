#!/usr/bin/env python3
"""
Simple Python script to validate frame pipe input from FFmpeg.
Reads raw RGB24 or grayscale frames from pipe and displays basic info.
"""

import sys
import os
import time
import numpy as np

def read_frames(pipe_path, width=320, height=320, channels=3):
    """Read frames from pipe and display basic info"""
    frame_size = width * height * channels
    frame_count = 0
    
    try:
        # Open pipe for reading
        with open(pipe_path, 'rb') as pipe:
            print(f"Connected to pipe: {pipe_path}")
            print(f"Frame size: {frame_size} bytes ({width}x{height}x{channels})")
            
            while True:
                # Read exact frame size
                frame_data = pipe.read(frame_size)
                
                if len(frame_data) == 0:
                    print("End of pipe data")
                    break
                    
                if len(frame_data) != frame_size:
                    print(f"Incomplete frame: {len(frame_data)}/{frame_size} bytes")
                    continue
                
                # Convert to numpy array for processing
                frame = np.frombuffer(frame_data, dtype=np.uint8)
                
                if channels == 1:
                    frame = frame.reshape((height, width))
                    print(f"Frame {frame_count}: {frame.shape} (grayscale)")
                else:
                    frame = frame.reshape((height, width, channels))
                    print(f"Frame {frame_count}: {frame.shape} (RGB)")
                
                frame_count += 1
                
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

def main():
    if len(sys.argv) < 2:
        print("Usage: python frame_pipe.py <pipe_path> [grayscale|rgb24]")
        print("Example: python frame_pipe.py \\\\.\\pipe\\face_recog_pipe rgb24")
        sys.exit(1)
    
    pipe_path = sys.argv[1]
    format_type = sys.argv[2] if len(sys.argv) > 1 else "rgb24"
    
    if format_type == "grayscale":
        print("Reading grayscale frames...")
        read_frames(pipe_path, width=320, height=320, channels=1)
    else:
        print("Reading RGB24 frames...")
        read_frames(pipe_path, width=320, height=320, channels=3)

if __name__ == "__main__":
    main()
