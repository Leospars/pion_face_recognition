#!/usr/bin/env python3
"""
Face Recognition Wrapper for WebRTC Camera
Communicates with Go via stdin/stdout JSON protocol

Protocol:
- Input: {"frame": "base64_encoded_image", "timestamp": 1234567890}
- Output: {"persons": [{"name": "John", "confidence": 0.85}], "timestamp": 1234567890}
"""

import sys
import json
import base64
import cv2
import numpy as np
import os
import time
from datetime import datetime
import onnxruntime as ort

# Configuration
FACE_DET_MODEL = "D:\\Code_Main\\Final_Year_Project\\SBC\\face_recog\\models\\face_detection_yunet_2023mar.onnx"
FACE_REC_MODEL = "D:\\Code_Main\\Final_Year_Project\\SBC\\face_recog\\models\\mobileface_v1.0_infer.onnx"
IMAGE_SIZE = (320, 320)
COSINE_THRESHOLD = 0.6

class MobileFaceRecognizer:
    def __init__(self, model_path, use_gpu=True):
        self.model_path = model_path
        providers = ['CUDAExecutionProvider', 'CPUExecutionProvider'] if use_gpu else ['CPUExecutionProvider']
        self.session = ort.InferenceSession(model_path, providers=providers)
        self.input_name = self.session.get_inputs()[0].name
        self.output_names = [output.name for output in self.session.get_outputs()]

    def alignCrop(self, image, face):
        x, y, w, h = face[:4].astype(int)
        padding = int(max(w, h) * 0.2)
        x1 = max(0, x - padding)
        y1 = max(0, y - padding)
        x2 = min(image.shape[1], x + w + padding)
        y2 = min(image.shape[0], y + h + padding)
        face_img = image[y1:y2, x1:x2]
        face_img = cv2.resize(face_img, (112, 112))
        return face_img

    def feature(self, aligned_face):
        if len(aligned_face.shape) == 3:
            face_rgb = cv2.cvtColor(aligned_face, cv2.COLOR_BGR2RGB)
        else:
            face_rgb = aligned_face
        face_normalized = face_rgb.astype(np.float32) / 255.0
        face_tensor = np.transpose(face_normalized, (2, 0, 1))
        face_tensor = np.expand_dims(face_tensor, axis=0)
        outputs = self.session.run(self.output_names, {self.input_name: face_tensor})
        return outputs[0]

def cosine_similarity(a, b):
    return np.dot(a, b) / (np.linalg.norm(a) * np.linalg.norm(b))

def load_face_database():
    """Load known faces from database"""
    face_db = {}
    if not os.path.exists("known_faces"):
        return face_db

    for dir in os.listdir("known_faces"):
        name = dir
        face_db[name] = []
        person_path = os.path.join("known_faces", dir)
        if not os.path.isdir(person_path):
            continue
        for file in os.listdir(person_path):
            if file.endswith(".npy"):
                emb = np.load(os.path.join(person_path, file))
                face_db[name].append(emb)
    return face_db

def match_face(query_embedding, face_db):
    """Match face against database"""
    best_name = "Unknown"
    best_score = 0.0

    for name, db_emb in face_db.items():
        if len(db_emb) == 0:
            continue
        centroid = np.mean(db_emb, axis=0)
        score = cosine_similarity(query_embedding, centroid)
        if np.isnan(score):
            continue
        if score > best_score:
            best_score = score
            best_name = name

    if best_score >= COSINE_THRESHOLD:
        return {"name": best_name, "confidence": float(best_score)}
    else:
        return {"name": f"Unknown ({best_name})", "confidence": float(best_score)}

def process_frame(frame_data, detector, recognizer, face_db):
    """Process a single frame and return identified persons"""
    # Decode base64 image
    try:
        img_bytes = base64.b64decode(frame_data)
        nparr = np.frombuffer(img_bytes, np.uint8)
        frame = cv2.imdecode(nparr, cv2.IMREAD_COLOR)
        if frame is None:
            return []
    except Exception as e:
        print(f"Error decoding frame: {e}", file=sys.stderr)
        return []

    h, w = frame.shape[:2]
    detector.setInputSize((w, h))

    _, faces = detector.detect(frame)

    if faces is None:
        return []

    persons = []
    for face in faces:
        aligned = recognizer.alignCrop(frame, face)
        embedding = recognizer.feature(aligned)

        if embedding is not None:
            person = match_face(embedding.flatten(), face_db)
            # Add bounding box
            x, y, w_box, h_box = face[:4].astype(int)
            person["bbox"] = {"x": int(x), "y": int(y), "w": int(w_box), "h": int(h_box)}
            persons.append(person)

    return persons

def main():
    """Main loop - read JSON from stdin, process, write JSON to stdout"""
    # Initialize models
    if not os.path.exists(FACE_DET_MODEL) or not os.path.exists(FACE_REC_MODEL):
        print(json.dumps({"error": "Face models not found", "face_det_model": os.path.abspath(FACE_DET_MODEL), "face_rec_model": os.path.abspath(FACE_REC_MODEL)}))
        sys.exit(1)

    detector = cv2.FaceDetectorYN.create(FACE_DET_MODEL, "", IMAGE_SIZE)
    recognizer = MobileFaceRecognizer(FACE_REC_MODEL, use_gpu=False)  # Use CPU for stability
    face_db = load_face_database()

    print(json.dumps({"status": "ready", "persons_in_db": len(face_db)}), flush=True)

    # Process frames from stdin
    for line in sys.stdin:
        try:
            msg = json.loads(line.strip())
            frame_data = msg.get("frame")
            timestamp = msg.get("timestamp", time.time())

            if not frame_data:
                continue

            persons = process_frame(frame_data, detector, recognizer, face_db)

            result = {
                "persons": persons,
                "timestamp": timestamp
            }
            print(json.dumps(result), flush=True)

        except json.JSONDecodeError:
            print(json.dumps({"error": "Invalid JSON"}), flush=True)
        except Exception as e:
            print(json.dumps({"error": str(e)}), flush=True)

if __name__ == "__main__":
    main()
