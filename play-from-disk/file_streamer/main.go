// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

// play-from-disk demonstrates how to send video and/or audio to your browser from files saved to disk.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
	"github.com/pion/webrtc/v4/pkg/media/oggreader"
)

const (
	audioFileName   = "../output.ogg"
	videoFileName   = "../output.h264"
	oggPageDuration = time.Millisecond * 20
)

// SignalingMessage represents WebSocket messages
type SignalingMessage struct {
	Type         string                     `json:"type"`
	RoomID       string                     `json:"roomId,omitempty"`
	Name         string                     `json:"name,omitempty"`
	IsCam        bool                       `json:"isCam,omitempty"`
	SenderUserID string                     `json:"senderUserID,omitempty"`
	TargetUserID string                     `json:"targetUserId,omitempty"`
	Offer        *webrtc.SessionDescription `json:"offer,omitempty"`
	Answer       *webrtc.SessionDescription `json:"answer,omitempty"`
	Candidate    *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
	SDP          string                     `json:"sdp,omitempty"`
	MyUserID     string                     `json:"myUserId,omitempty"`
	Users        []UserInfo                 `json:"users,omitempty"`
	UserID       string                     `json:"userId,omitempty"`
	Message      string                     `json:"message,omitempty"`
}

// UserInfo represents a user in room
type UserInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	IsCam bool   `json:"isCam"`
}

// FilePeer represents a peer that streams from files
type FilePeer struct {
	roomID        string
	ws            *websocket.Conn
	viewers       map[string]bool
	viewersMu     sync.RWMutex
	peerConns     map[string]*webrtc.PeerConnection
	peerConnsMu   sync.RWMutex
	videoTrack    *webrtc.TrackLocalStaticSample
	audioTrack    *webrtc.TrackLocalStaticSample
	stopCh        chan struct{}
	startTime     time.Time
	connectStart  time.Time
	joinStart     time.Time
	offerStart    time.Time
	tracksOnce    sync.Once
	tracksErr     error
	streamingOnce sync.Once
}

func main() { //nolint:gocognit,cyclop,gocyclo,maintidx
	var roomID string
	var signalingServer string

	flag.StringVar(&roomID, "room", "", "Room ID to join (uses signaling server)")
	flag.StringVar(&signalingServer, "server", "ws://192.168.56.1:3000", "Signaling server URL")
	flag.Parse()

	// If room ID is provided, use signaling server mode
	if roomID != "" {
		log.Printf("Using signaling server mode for room: %s", roomID)
		runSignalingMode(roomID, signalingServer)
		return
	}

	// Otherwise, use file-based SDP exchange mode
	log.Println("Using file-based SDP exchange mode")
	runFileMode()
}

func runSignalingMode(roomID, signalingServer string) {
	// Check if we have required files
	_, err := os.Stat(videoFileName)
	haveVideoFile := !os.IsNotExist(err)

	_, err = os.Stat(audioFileName)
	haveAudioFile := !os.IsNotExist(err)

	if !haveAudioFile && !haveVideoFile {
		log.Fatalf("Could not find %s or %s", audioFileName, videoFileName)
	}

	peer := &FilePeer{
		roomID:    roomID,
		peerConns: make(map[string]*webrtc.PeerConnection),
		viewers:   make(map[string]bool),
		stopCh:    make(chan struct{}),
		startTime: time.Now(),
	}

	// Connect to signaling server
	if err := peer.connectSignaling(signalingServer); err != nil {
		log.Fatalf("Failed to connect to signaling server: %v", err)
	}

	// Start signaling
	go peer.handleSignaling()

	// Join room
	if err := peer.joinRoom(); err != nil {
		log.Fatalf("Failed to join room: %v", err)
	}

	// Wait for shutdown
	<-peer.stopCh
	log.Println("Shutting down gracefully")
}

func runFileMode() {
	// Original file-based implementation
	// Assert that we have an audio or video file
	_, err := os.Stat(videoFileName)
	haveVideoFile := !os.IsNotExist(err)

	_, err = os.Stat(audioFileName)
	haveAudioFile := !os.IsNotExist(err)

	if !haveAudioFile && !haveVideoFile {
		log.Fatalf("Could not find %s or %s", audioFileName, videoFileName)
	}

	// Create a new RTCPeerConnection
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	defer func() {
		if cErr := peerConnection.Close(); cErr != nil {
			fmt.Printf("cannot close peerConnection: %v\n", cErr)
		}
	}()

	iceConnectedCtx, iceConnectedCtxCancel := context.WithCancel(context.Background())

	if haveVideoFile { //nolint:nestif
		file, openErr := os.Open(videoFileName)
		if openErr != nil {
			panic(openErr)
		}

		_, header, openErr := ivfreader.NewWith(file)
		if openErr != nil {
			panic(openErr)
		}

		// Determine video codec
		var trackCodec string
		switch header.FourCC {
		case "AV01":
			trackCodec = webrtc.MimeTypeAV1
		case "VP90":
			trackCodec = webrtc.MimeTypeVP9
		case "VP80":
			trackCodec = webrtc.MimeTypeVP8
		default:
			panic(fmt.Sprintf("Unable to handle FourCC %s", header.FourCC))
		}

		// Create a video track
		videoTrack, videoTrackErr := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: trackCodec}, "video", "pion",
		)
		if videoTrackErr != nil {
			panic(videoTrackErr)
		}

		rtpSender, videoTrackErr := peerConnection.AddTrack(videoTrack)
		if videoTrackErr != nil {
			panic(videoTrackErr)
		}

		// Read incoming RTCP packets
		// Before these packets are returned they are processed by interceptors. For things
		// like NACK this needs to be called.
		go func() {
			rtcpBuf := make([]byte, 1500)
			for {
				if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
					return
				}
			}
		}()

		go func() {
			// Open a IVF file and start reading using our IVFReader
			file, ivfErr := os.Open(videoFileName)
			if ivfErr != nil {
				panic(ivfErr)
			}

			ivf, header, ivfErr := ivfreader.NewWith(file)
			if ivfErr != nil {
				panic(ivfErr)
			}

			// Wait for connection established
			<-iceConnectedCtx.Done()
			fmt.Println("ICE connected - starting video stream")

			// Send our video file frame at a time. Pace our sending so we send it at the same speed it should be played back as.
			// This isn't required since video is timestamped, but we will have much higher loss if we send it all at once.
			//
			// It is important to use a time.Ticker instead of time.Sleep because
			// * avoids accumulating skew, just calling time.Sleep didn't compensate for time spent parsing data
			// * works around latency issues with Sleep (see https://github.com/golang/go/issues/44343)
			ticker := time.NewTicker(
				time.Millisecond * time.Duration((float32(header.TimebaseNumerator)/float32(header.TimebaseDenominator))*1000),
			)
			defer ticker.Stop()
			frameCount := 0
			startTime := time.Now()
			for ; true; <-ticker.C {
				frame, _, ivfErr := ivf.ParseNextFrame()
				if errors.Is(ivfErr, io.EOF) {
					fmt.Printf("\nVideo loop completed (%d frames in %.1fs) - restarting...\n", frameCount, time.Since(startTime).Seconds())
					// Close current file
					file.Close()
					// Reopen file for looping
					file, ivfErr = os.Open(videoFileName)
					if ivfErr != nil {
						panic(ivfErr)
					}
					ivf, header, ivfErr = ivfreader.NewWith(file)
					if ivfErr != nil {
						panic(ivfErr)
					}
					// Reset counters
					frameCount = 0
					startTime = time.Now()
					continue
				}

				if ivfErr != nil {
					panic(ivfErr)
				}

				if ivfErr = videoTrack.WriteSample(media.Sample{Data: frame, Duration: time.Second}); ivfErr != nil {
					panic(ivfErr)
				}

				frameCount++
				if frameCount < 5 || frameCount%200 == 0 {
					fmt.Printf("\rVideo: sent frame %d (%.1fs elapsed)", frameCount, time.Since(startTime).Seconds())
				}
			}
		}()
	}

	if haveAudioFile { //nolint:nestif
		// Create an audio track
		audioTrack, audioTrackErr := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion",
		)
		if audioTrackErr != nil {
			panic(audioTrackErr)
		}

		rtpSender, audioTrackErr := peerConnection.AddTrack(audioTrack)
		if audioTrackErr != nil {
			panic(audioTrackErr)
		}

		// Read incoming RTCP packets
		// Before these packets are returned they are processed by interceptors. For things
		// like NACK this needs to be called.
		go func() {
			rtcpBuf := make([]byte, 1500)
			for {
				if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
					return
				}
			}
		}()

		go func() {
			// Open a OGG file and start reading using our OGGReader
			file, oggErr := os.Open(audioFileName)
			if oggErr != nil {
				panic(oggErr)
			}

			// Open on oggfile in non-checksum mode.
			ogg, _, oggErr := oggreader.NewWith(file)
			if oggErr != nil {
				panic(oggErr)
			}

			// Wait for connection established
			<-iceConnectedCtx.Done()
			fmt.Println("ICE connected - starting audio stream")

			// Keep track of last granule, difference is the amount of samples in the buffer
			var lastGranule uint64

			pageCount := 0
			audioStartTime := time.Now()
			for true {
				pageData, pageHeader, oggErr := ogg.ParseNextPage()
				if errors.Is(oggErr, io.EOF) {
					fmt.Printf("\nAudio loop completed (%d pages in %.1fs) - restarting...\n", pageCount, time.Since(audioStartTime).Seconds())
					// Close current file
					file.Close()
					// Reopen file for looping
					file, oggErr = os.Open(audioFileName)
					if oggErr != nil {
						panic(oggErr)
					}
					ogg, _, oggErr = oggreader.NewWith(file)
					if oggErr != nil {
						panic(oggErr)
					}
					// Reset counters
					pageCount = 0
					audioStartTime = time.Now()
					lastGranule = 0
					continue
				}

				if oggErr != nil {
					panic(oggErr)
				}

				// The amount of samples is the difference between last and current timestamp
				sampleCount := float64(pageHeader.GranulePosition - lastGranule)
				lastGranule = pageHeader.GranulePosition
				sampleDuration := time.Duration((sampleCount/48000)*1000) * time.Millisecond

				if oggErr = audioTrack.WriteSample(media.Sample{Data: pageData, Duration: sampleDuration}); oggErr != nil {
					panic(oggErr)
				}

				// Pace audio by actual page duration to avoid burning through file too fast
				if sampleDuration > 0 {
					time.Sleep(sampleDuration)
				} else {
					fmt.Printf("Skipping audio page %d with zero duration\n", pageCount)
				}

				pageCount++
				if pageCount < 5 || pageCount%100 == 0 {
					fmt.Printf("\rAudio: sent page %d (%.1fs elapsed)", pageCount, time.Since(audioStartTime).Seconds())
				}
			}
		}()
	}

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())
		if connectionState == webrtc.ICEConnectionStateConnected {
			iceConnectedCtxCancel()
		}
	})

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", state.String())

		if state == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure.
			// It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Peer Connection has gone to failed exiting")
			os.Exit(0)
		}

		if state == webrtc.PeerConnectionStateClosed {
			// PeerConnection was explicitly closed. This usually happens from a DTLS CloseNotify
			fmt.Println("Peer Connection has gone to closed exiting")
			os.Exit(0)
		}
	})

	// Wait for offer to be pasted
	offer := webrtc.SessionDescription{}
	decode(readUntilNewline(), &offer)

	// Set remote SessionDescription
	if err = peerConnection.SetRemoteDescription(offer); err != nil {
		panic(err)
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Sets the LocalDescription, and starts our UDP listeners
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	// Output answer in base64 so we can paste it in browser
	fmt.Println(encode(peerConnection.LocalDescription()))

	// Block forever
	select {}
}

// createSharedTracks creates video and audio tracks once for all peer connections
func (fp *FilePeer) createSharedTracks() error {
	var initErr error

	fp.tracksOnce.Do(func() {
		// Create video track (H.264 for H.264 files, VP8 for IVF files)
		if strings.HasSuffix(videoFileName, ".h264") || strings.HasSuffix(videoFileName, ".264") {
			fp.videoTrack, initErr = webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
				"video",
				"file-video",
			)
		} else {
			fp.videoTrack, initErr = webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
				"video",
				"file-video",
			)
		}

		if initErr != nil {
			return
		}

		// Create audio track
		fp.audioTrack, initErr = webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
			"audio",
			"file-audio",
		)
	})

	return initErr
}

// createPeerConnection creates WebRTC peer connection for specific user
func (fp *FilePeer) createPeerConnection(userID string) error {
	iceConfig := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
		// SDPSemantics: webrtc.SDPSemanticsUnifiedPlan,
	}

	pc, err := webrtc.NewPeerConnection(iceConfig)
	if err != nil {
		return err
	}

	// Create shared tracks once
	if err := fp.createSharedTracks(); err != nil {
		return err
	}

	fp.peerConnsMu.Lock()
	fp.peerConns[userID] = pc
	fp.peerConnsMu.Unlock()

	fp.viewersMu.Lock()
	fp.viewers[userID] = true
	fp.viewersMu.Unlock()

	log.Printf("Peer connection made: %s, for userID: %s, %v", fp.peerConns[userID].ID(), userID, fp.viewers[userID])

	// Add shared tracks to this peer connection
	if _, err = os.Stat(videoFileName); !os.IsNotExist(err) {
		if _, err = pc.AddTrack(fp.videoTrack); err != nil {
			return err
		}
	}

	// Add shared audio track if audio file exists
	if _, err = os.Stat(audioFileName); !os.IsNotExist(err) {
		if _, err = pc.AddTrack(fp.audioTrack); err != nil {
			return err
		}
		log.Printf("Audio track added to peer connection")
	} else {
		log.Printf("No audio file found, audio track not created")
	}

	// Handle incoming tracks
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		// TODO: Send audio track to speaker device
		log.Printf("Received track: %s (%s)", track.ID(), track.Kind().String())
	})

	// Handle ICE candidates
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}

		fp.viewersMu.RLock()
		viewers := make([]string, 0, len(fp.viewers))
		for viewerID := range fp.viewers {
			viewers = append(viewers, viewerID)
		}
		fp.viewersMu.RUnlock()

		for _, viewerID := range viewers {
			msg := SignalingMessage{
				Type:         "candidate",
				TargetUserID: viewerID,
				Candidate:    toPtr(candidate.ToJSON()),
			}
			if err := fp.ws.WriteJSON(msg); err != nil {
				log.Printf("Failed to send ICE candidate: %v", err)
			}
		}
	})

	// Handle connection state changes
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Peer connection state: %s", state.String())
		if state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			log.Printf("Peer connection lost.")
		}
	})

	// Handle ICE connection state for streaming timing
	iceConnectedCtx, iceConnectedCtxCancel := context.WithCancel(context.Background())
	iceConnectStart := time.Now()
	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf("ICE Connection State has changed %s", connectionState.String())
		if connectionState == webrtc.ICEConnectionStateConnected {
			iceLatency := time.Since(iceConnectStart)
			totalLatency := time.Since(fp.startTime)
			log.Printf("ICE connected (latency: %v, total: %v)", iceLatency, totalLatency)
			iceConnectedCtxCancel()
		} else if connectionState == webrtc.ICEConnectionStateDisconnected || connectionState == webrtc.ICEConnectionStateFailed {
			log.Printf("ICE connection lost, stopping streaming to prevent black hole")
			iceConnectedCtxCancel()
		}
	})

	// Start streaming after ICE connection (only once)
	go func() {
		<-iceConnectedCtx.Done()
		fp.startStreamingOnce()
	}()

	return nil
}

// startStreamingOnce starts video and audio streaming only once per session
func (fp *FilePeer) startStreamingOnce() {
	fp.streamingOnce.Do(func() {
		log.Printf("Starting media streams (first viewer connected)")

		// Start video streaming
		if _, err := os.Stat(videoFileName); !os.IsNotExist(err) {
			go func() {
				fp.startVideoStreaming()
			}()
		}

		// Start audio streaming
		if _, err := os.Stat(audioFileName); !os.IsNotExist(err) {
			go func() {
				fp.startAudioStreaming()
			}()
		}
	})
}

// startVideoStreaming starts streaming video from file
func (fp *FilePeer) startVideoStreaming() {
	// Check if file is H.264 by extension
	if strings.HasSuffix(videoFileName, ".h264") || strings.HasSuffix(videoFileName, ".264") {
		fp.startH264Streaming()
		return
	}

	// Default IVF streaming
	file, err := os.Open(videoFileName)
	if err != nil {
		log.Printf("Failed to open video file: %v", err)
		return
	}
	defer file.Close()

	ivf, header, err := ivfreader.NewWith(file)
	if err != nil {
		log.Printf("Failed to create IVF reader: %v", err)
		return
	}

	ticker := time.NewTicker(
		time.Millisecond * time.Duration((float32(header.TimebaseNumerator)/float32(header.TimebaseDenominator))*1000),
	)
	defer ticker.Stop()

	frameCount := 0
	startTime := time.Now()

	for ; true; <-ticker.C {
		frame, _, err := ivf.ParseNextFrame()
		if errors.Is(err, io.EOF) {
			log.Printf("Video loop completed (%d frames in %.1fs)", frameCount, time.Since(startTime).Seconds())
			// Reopen file for looping
			file.Close()
			file, err = os.Open(videoFileName)
			if err != nil {
				log.Printf("Failed to reopen video file: %v", err)
				return
			}
			ivf, _, err = ivfreader.NewWith(file)
			if err != nil {
				log.Printf("Failed to recreate IVF reader: %v", err)
				return
			}
			frameCount = 0
			startTime = time.Now()
			continue
		}

		if err != nil {
			log.Printf("Error reading video frame: %v", err)
			return
		}

		if err := fp.videoTrack.WriteSample(media.Sample{Data: frame, Duration: time.Second}); err != nil {
			log.Printf("Error writing video sample: %v", err)
			return
		}

		frameCount++
		if frameCount < 5 || frameCount%200 == 0 {
			log.Printf("Video: sent frame %d (%.1fs elapsed)", frameCount, time.Since(startTime).Seconds())
		}
	}
}

// startH264Streaming starts streaming H.264 video from file
func (fp *FilePeer) startH264Streaming() {
	file, err := os.Open(videoFileName)
	if err != nil {
		log.Printf("Failed to open H.264 file: %v", err)
		return
	}
	defer file.Close()

	h264, err := h264reader.NewReader(file)
	if err != nil {
		log.Printf("Failed to create H.264 reader: %v", err)
		return
	}

	frameCount := 0
	startTime := time.Now()
	nextVideoSampleTime := time.Now()
	timePerFrame := time.Millisecond * 33 // 30fps = 1000ms/30frames = 33.3ms

	for {
		select {
		case <-fp.stopCh:
			return
		default:
		}

		nal, err := h264.NextNAL()
		if err == io.EOF {
			log.Printf("H.264 video loop completed (%d frames in %.1fs) - restarting...", frameCount, time.Since(startTime).Seconds())
			// Close current file
			file.Close()
			// Reopen file for looping
			file, err = os.Open(videoFileName)
			if err != nil {
				log.Printf("Failed to reopen H.264 file: %v", err)
				return
			}
			h264, err = h264reader.NewReader(file)
			if err != nil {
				log.Printf("Failed to recreate H.264 reader: %v", err)
				return
			}
			frameCount = 0
			startTime = time.Now()
			nextVideoSampleTime = time.Now()
			continue
		}
		if err != nil {
			log.Printf("Error reading H.264 NAL unit: %v", err)
			return
		}

		// Golang's time.Sleep() is not precise enough for a consistent audio and video stream
		// (see https://github.com/golang/go/issues/44343). Therefore, don't use an absolute
		// sleep, but instead calculate remaining sleep duration using wall clock time.
		// The packets still will not be perfectly timed, but error will average out to the point
		// where the receiver's jitter buffer can compensate.
		nextVideoSampleTime = nextVideoSampleTime.Add(timePerFrame)
		sleepDuration := nextVideoSampleTime.Sub(time.Now())
		if sleepDuration > 0 {
			time.Sleep(sleepDuration)
		}

		if err := fp.videoTrack.WriteSample(media.Sample{Data: nal.Data, Duration: time.Second}); err != nil {
			log.Printf("Error writing H.264 video sample: %v", err)
			return
		}

		frameCount++
		if frameCount < 5 || frameCount%200 == 0 {
			log.Printf("H.264: sent NAL unit %d (%.1fs elapsed)", frameCount, time.Since(startTime).Seconds())
		}
	}
}

// startAudioStreaming starts streaming audio from file
func (fp *FilePeer) startAudioStreaming() {
	file, err := os.Open(audioFileName)
	if err != nil {
		log.Printf("Failed to open audio file: %v", err)
		return
	}
	defer file.Close()

	ogg, _, err := oggreader.NewWith(file)
	if err != nil {
		log.Printf("Failed to create OGG reader: %v", err)
		return
	}

	var lastGranule uint64
	pageCount := 0
	startTime := time.Now()

	for {
		pageData, pageHeader, err := ogg.ParseNextPage()
		if errors.Is(err, io.EOF) {
			log.Printf("Audio loop completed (%d pages in %.1fs)", pageCount, time.Since(startTime).Seconds())
			// Reopen file for looping
			file.Close()
			file, err = os.Open(audioFileName)
			if err != nil {
				log.Printf("Failed to reopen audio file: %v", err)
				return
			}
			ogg, _, err = oggreader.NewWith(file)
			if err != nil {
				log.Printf("Failed to recreate OGG reader: %v", err)
				return
			}
			pageCount = 0
			startTime = time.Now()
			lastGranule = 0
			continue
		}

		if err != nil {
			log.Printf("Error reading audio page: %v", err)
			return
		}

		sampleCount := float64(pageHeader.GranulePosition - lastGranule)
		lastGranule = pageHeader.GranulePosition
		sampleDuration := time.Duration((sampleCount/48000)*1000) * time.Millisecond

		if err := fp.audioTrack.WriteSample(media.Sample{Data: pageData, Duration: sampleDuration}); err != nil {
			log.Printf("Error writing audio sample: %v", err)
			return
		}

		if sampleDuration > 0 {
			time.Sleep(sampleDuration)
		}

		pageCount++
		if pageCount < 5 || pageCount%100 == 0 {
			log.Printf("Audio: sent page %d (%.1fs elapsed)", pageCount, time.Since(startTime).Seconds())
		}
	}
}

// Helper functions from original main.go
func toPtr(init webrtc.ICECandidateInit) *webrtc.ICECandidateInit {
	return &init
}

// Read from stdin until we get a newline.
func readUntilNewline() (in string) {
	var err error

	r := bufio.NewReader(os.Stdin)
	for {
		in, err = r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			panic(err)
		}

		if in = strings.TrimSpace(in); len(in) > 0 {
			break
		}
	}

	fmt.Println("")

	return
}

// JSON encode + base64 a SessionDescription.
func encode(obj *webrtc.SessionDescription) string {
	b, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return base64.StdEncoding.EncodeToString(b)
}

// Decode a base64 and unmarshal JSON into a SessionDescription.
func decode(in string, obj *webrtc.SessionDescription) {
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		panic(err)
	}

	if err = json.Unmarshal(b, obj); err != nil {
		panic(err)
	}
}

// connectSignaling establishes WebSocket connection to signaling server
func (fp *FilePeer) connectSignaling(signalingServer string) error {
	fp.connectStart = time.Now()
	log.Printf("Connecting to signaling server: %s", signalingServer)

	ws, _, err := websocket.DefaultDialer.Dial(signalingServer, nil)
	if err != nil {
		return err
	}

	fp.ws = ws
	connectLatency := time.Since(fp.connectStart)
	totalLatency := time.Since(fp.startTime)
	log.Printf("Connected to signaling server (WebSocket latency: %v, total: %v)", connectLatency, totalLatency)
	return nil
}

// joinRoom sends join message to signaling server
func (fp *FilePeer) joinRoom() error {
	fp.joinStart = time.Now()
	msg := SignalingMessage{
		Type:   "join",
		RoomID: fp.roomID,
		Name:   "File Player",
		IsCam:  true,
	}

	return fp.ws.WriteJSON(msg)
}

// checkAndStopStreaming stops streaming if no viewers remain
func (fp *FilePeer) checkAndStopStreaming() {
	fp.viewersMu.RLock()
	hasActiveViewers := false
	for _, active := range fp.viewers {
		if active {
			hasActiveViewers = true
			break
		}
	}
	fp.viewersMu.RUnlock()

	if !hasActiveViewers {
		log.Printf("No active viewers remaining, stopping streaming")
		// close(fp.stopCh)
	}
}

// handleSignaling processes WebSocket messages
func (fp *FilePeer) handleSignaling() {
	for {
		select {
		case <-fp.stopCh:
			return
		default:
		}

		var msg SignalingMessage
		if err := fp.ws.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket connection closed, stopping streaming to prevent black hole")
				close(fp.stopCh) // Stop all streaming goroutines
			}
			return
		}

		switch msg.Type {
		case "existing_users":
			fp.handleExistingUsers(msg.Users)
		case "user-joined":
			fp.handleUserJoined(msg.UserID, msg.Name, msg.IsCam)
		case "user-left":
			fp.handleUserLeft(msg.UserID)
		case "answer":
			fp.handleAnswer(msg)
		case "candidate":
			fp.handleCandidate(msg)
		case "room-not-found":
			log.Printf("ERROR: Room '%s' not found. %s", fp.roomID, msg.Message)
			log.Printf("Please check if the room ID is correct or create the room first.")
			close(fp.stopCh)
			return
		default:
			log.Printf("Unknown message type: %s", msg.Type)
		}
	}
}

// handleExistingUsers processes existing users when joining
func (fp *FilePeer) handleExistingUsers(users []UserInfo) {
	// Log room join handshake timing
	joinLatency := time.Since(fp.joinStart)
	totalLatency := time.Since(fp.startTime)
	log.Printf("Room joined successfully (join latency: %v, total: %v)", joinLatency, totalLatency)

	if len(users) == 0 {
		log.Printf("Joined empty room, waiting for viewers...")
	} else {
		log.Printf("Found %d users in room", len(users))
	}

	for _, user := range users {
		if !user.IsCam {
			log.Printf("Found existing viewer: %s (%s)", user.Name, user.ID)

			// Create peer connection with viewers in room
			if err := fp.createPeerConnection(user.ID); err != nil {
				log.Printf("Failed to create peer connection: %v", err)
				close(fp.stopCh)
				return
			}

			go fp.sendOffer(user.ID)
		}
	}
}

// handleUserJoined handles new user joining
func (fp *FilePeer) handleUserJoined(userID, name string, isCam bool) {
	log.Printf("User joined: %s (%s), isCam: %v", name, userID, isCam)

	if !isCam {
		// Create peer connection if not already created
		// fp.peerConnsMu.Lock()
		if _, exists := fp.peerConns[userID]; !exists {
			if err := fp.createPeerConnection(userID); err != nil {
				log.Printf("Failed to create peer connection: %v", err)
				fp.peerConnsMu.Unlock()
				close(fp.stopCh)
				return
			}
		} else {
			log.Printf("User connection already exists.")
		}
		// fp.peerConnsMu.Unlock()

		go fp.sendOffer(userID)
	}
}

// handleUserLeft handles user leaving
func (fp *FilePeer) handleUserLeft(userID string) {
	log.Printf("User left: %s", userID)
	fp.viewersMu.Lock()
	delete(fp.viewers, userID)
	fp.viewersMu.Unlock()

	fp.peerConnsMu.Lock()
	delete(fp.peerConns, userID)
	fp.peerConnsMu.Unlock()

	fp.checkAndStopStreaming()
}

// sendOffer creates and sends WebRTC offer to a viewer
func (fp *FilePeer) sendOffer(viewerID string) {
	fp.offerStart = time.Now()
	log.Printf("Sending offer to viewer: %s", viewerID)

	fp.peerConnsMu.RLock()
	pc, exists := fp.peerConns[viewerID]
	fp.peerConnsMu.RUnlock()

	if !exists {
		log.Printf("On Offer: No peer connection found for viewer: %s", viewerID)
		return
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Printf("Failed to create offer: %v", err)
		return
	}

	offerLatency := time.Since(fp.offerStart)
	totalLatency := time.Since(fp.startTime)
	log.Printf("Offer created (offer latency: %v, total: %v)", offerLatency, totalLatency)

	if err := pc.SetLocalDescription(offer); err != nil {
		log.Printf("Failed to set local description: %v", err)
		return
	}

	msg := SignalingMessage{
		Type:         "offer",
		TargetUserID: viewerID,
		Offer:        &offer,
	}

	if err := fp.ws.WriteJSON(msg); err != nil {
		log.Printf("Failed to send offer: %v", err)
	}
}

// handleAnswer processes answer from viewer
func (fp *FilePeer) handleAnswer(msg SignalingMessage) {
	log.Printf("Received answer from viewer")

	if msg.Answer == nil {
		log.Println("Answer is nil")
		return
	}

	// Get the peer connection for this specific user
	fp.peerConnsMu.RLock()
	pc, exists := fp.peerConns[msg.SenderUserID]
	fp.peerConnsMu.RUnlock()

	if !exists {
		log.Printf("On Answer: No peer connection found for user: %s", msg.SenderUserID)
		return
	}

	// Check if we already have a remote description set to prevent duplicates
	if pc.RemoteDescription() != nil {
		log.Printf("Remote description already set, ignoring duplicate answer")
		return
	}

	if err := pc.SetRemoteDescription(*msg.Answer); err != nil {
		log.Printf("Failed to set remote description: %v", err)
	}
}

// handleCandidate processes ICE candidate from viewer
func (fp *FilePeer) handleCandidate(msg SignalingMessage) {
	if msg.Candidate == nil {
		return
	}

	// Get the peer connection for this specific user
	fp.peerConnsMu.RLock()
	pc, exists := fp.peerConns[msg.SenderUserID]
	fp.peerConnsMu.RUnlock()

	if !exists {
		log.Printf("For candidate: No peer connection found for user: %s", msg.SenderUserID)
		return
	}

	if err := pc.AddICECandidate(*msg.Candidate); err != nil {
		log.Printf("Failed to add ICE candidate: %v", err)
	}
}
