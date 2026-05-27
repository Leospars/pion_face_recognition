@echo off
setlocal enabledelayedexpansion

:: 1. Read input parameters or fall back to defaults if empty
set "PIX_FMT=%~1"
if "!PIX_FMT!"=="" set "PIX_FMT=yuyv422"

set "WIDTH=%~2"
if "!WIDTH!"=="" set "WIDTH=1920"

set "HEIGHT=%~3"
if "!HEIGHT!"=="" set "HEIGHT=1080"

set "RESOLUTION=!WIDTH!x!HEIGHT!"

:: 2. Define static application and device paths
set "PYTHON_EXE=D:\Code_Main\Final_Year_Project\SBC\face_recog\.venv\Scripts\python.exe"
set "PYTHON_SCRIPT=D:\Code_Main\Final_Year_Project\SBC\webrtc_video\play-from-disk\frame_pipe.py"
set "DEVICE=Arducam USB Camera"

echo [INFO] Configuration: Format=!PIX_FMT!, Resolution=!RESOLUTION!

:: 3. Launch the pipeline in a VISIBLE window while managing timeout asynchronously
echo [INFO] Starting pipeline window...
start "FFmpeg_Pipeline_Window" cmd /k "ffmpeg.exe -hide_banner -f dshow -pixel_format !PIX_FMT! -video_size !RESOLUTION! -rtbufsize 10M -i "%DEVICE%" -f rawvideo -pix_fmt !PIX_FMT! -r 5 pipe:1 | "%PYTHON_EXE%" -u "%PYTHON_SCRIPT%" "-" "!PIX_FMT!" "!WIDTH!" "!HEIGHT!""

:: 4. Count down 10 seconds for the timeout safety window
echo [INFO] Enforcing 10-second timeout safety window...
timeout /t 10 /nobreak >nul

:: 5. Forcefully terminate the processes to release camera handles
echo [WARN] Timeout reached. Stopping streaming processes...
taskkill /f /im ffmpeg.exe >nul 2>&1
taskkill /f /im python.exe >nul 2>&1

echo [SUCCESS] Pipeline terminated securely.
