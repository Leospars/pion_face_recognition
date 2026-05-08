@echo off
echo Generating WebRTC test files...
echo.

echo Creating video file (IVF with VP8)...
ffmpeg -i Recording.m4a -g 30 -b:v 2M output.ivf

echo Creating audio file (OGG with Opus)...
ffmpeg -i Recording.m4a -vn -c:a libopus -ac 2 -ar 48000 -page_duration 20000 -map_metadata -1 output.ogg

echo.
echo Test files generated successfully!
echo - output.ivf (VP8 video)
echo - output.ogg (Opus audio, 20ms pages)
echo.
echo Important: The audio file uses 20ms page duration for WebRTC compatibility.
