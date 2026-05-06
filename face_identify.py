import cv2
import numpy as np
import os
import time
import csv
import sys
import json
import base64
from datetime import datetime
import onnxruntime as ort

# -----------------------------
# Configuration
# -----------------------------
FACE_DET_MODEL = "D:\\Code_Main\\Final_Year_Project\\SBC\\face_recog\\models\\face_detection_yunet_2023mar.onnx"
FACE_REC_MODEL = "D:\\Code_Main\\Final_Year_Project\\SBC\\face_recog\\models\\mobileface_v1.0_infer.onnx"
IMAGE_SIZE = (320, 320)
COSINE_THRESHOLD = 0.6   # higher = stricter
KNOWN_FACES_DIR = "./known_faces"

# -----------------------------
# Mobile Face Recognizer Class
# -----------------------------
class MobileFaceRecognizer:
    def __init__(self, model_path, use_gpu=True):
        self.model_path = model_path

        # Setup ONNX Runtime session
        providers = ['CUDAExecutionProvider', 'CPUExecutionProvider'] if use_gpu else ['CPUExecutionProvider']
        self.session = ort.InferenceSession(model_path, providers=providers)

        # Get input/output names
        self.input_name = self.session.get_inputs()[0].name
        self.output_names = [output.name for output in self.session.get_outputs()]

        print(f"MobileFaceRecognizer initialized with model: {model_path}")

    def alignCrop(self, image, face):
        """Simple alignment and crop based on face bounding box"""
        x, y, w, h = face[:4].astype(int)

        # Add some padding
        padding = int(max(w, h) * 0.2)
        x1 = max(0, x - padding)
        y1 = max(0, y - padding)
        x2 = min(image.shape[1], x + w + padding)
        y2 = min(image.shape[0], y + h + padding)

        # Crop face
        face_img = image[y1:y2, x1:x2]

        # Resize to 112x112 (MobileFaceNet input size)
        face_img = cv2.resize(face_img, (112, 112))

        return face_img

    def feature(self, aligned_face):
        """Extract face features using MobileFaceNet"""
        # Preprocess: convert to RGB and normalize
        if len(aligned_face.shape) == 3:
            face_rgb = cv2.cvtColor(aligned_face, cv2.COLOR_BGR2RGB)
        else:
            face_rgb = aligned_face

        # Normalize to [0, 1]
        face_normalized = face_rgb.astype(np.float32) / 255.0

        # Add batch dimension and transpose to NCHW
        face_tensor = np.transpose(face_normalized, (2, 0, 1))
        face_tensor = np.expand_dims(face_tensor, axis=0)

        # Run inference
        outputs = self.session.run(self.output_names, {self.input_name: face_tensor})

        # Return the embedding (128-dimensional)
        return outputs[0]

# -----------------------------
# Initialize models
# -----------------------------
detector = cv2.FaceDetectorYN.create(
    FACE_DET_MODEL, "", IMAGE_SIZE
)

recognizer = MobileFaceRecognizer(FACE_REC_MODEL, True)

# -----------------------------
# Helper functions
# -----------------------------
def cosine_similarity(a, b):
    return np.dot(a, b) / (np.linalg.norm(a) * np.linalg.norm(b))

def extract_embedding_from_frame(frame):
    h, w = frame.shape[:2]
    detector.setInputSize((w, h))

    # Time face detection
    start_time = time.time()
    _, faces = detector.detect(frame)
    det_time = (time.time() - start_time) * 1000  # Convert to milliseconds

    if faces is None:
        return None, None, 0, 0

    face = faces[0]

    # Time face recognition (alignment + feature extraction)
    start_time = time.time()
    aligned = recognizer.alignCrop(frame, face)
    embedding = recognizer.feature(aligned)
    rec_time = (time.time() - start_time) * 1000  # Convert to milliseconds

    return embedding.flatten(), face, det_time, rec_time

# -----------------------------
# Build face database
# -----------------------------
def load_face_database():
    """Load known faces from database"""
    face_db = {}
    if not os.path.exists(KNOWN_FACES_DIR):
        return face_db

    for dir in os.listdir(KNOWN_FACES_DIR):
        name = dir
        face_db[name] = []
        person_path = os.path.join(KNOWN_FACES_DIR, dir)
        if not os.path.isdir(person_path):
            continue
        for file in os.listdir(person_path):
            if file.endswith(".npy"):
                emb = np.load(os.path.join(person_path, file))
                face_db[name].append(emb)
    return face_db

face_db = load_face_database()

# ----------------------------------------------------
# OPTION 2: Manual cosine similarity (RECOMMENDED)
# ----------------------------------------------------
def match_face(query_embedding, face_db=None):
    if face_db is None:
        face_db = globals().get('face_db', {})
    best_name = "Unknown"
    best_score = 0.0

    for name, db_emb in face_db.items():
        # Skip if this person has no valid embeddings
        if len(db_emb) == 0:
            continue

        # Compute centroid of embeddings for this person
        centroid = np.mean(db_emb, axis=0)
        score = cosine_similarity(query_embedding, centroid)

        # Handle case where score might be NaN
        if np.isnan(score):
            continue

        if score > best_score:
            best_score = score
            best_name = name

    if best_score >= COSINE_THRESHOLD:
        return f"{best_name} ({best_score:.3f})"
    else:
        return f"Unknown {best_name} ({best_score:.3f})"

def process_frame(frame_data, detector, recognizer, face_db):
    """Process a single frame and return identified persons with timing info"""
    # Decode base64 image
    try:
        img_bytes = base64.b64decode(frame_data)
        nparr = np.frombuffer(img_bytes, np.uint8)
        frame = cv2.imdecode(nparr, cv2.IMREAD_COLOR)
        if frame is None:
            return [], 0.0, 0.0
    except Exception as e:
        print(f"Error decoding frame: {e}", file=sys.stderr)
        return [], 0.0, 0.0

    h, w = frame.shape[:2]
    detector.setInputSize((w, h))

    start_det = time.time()
    _, faces = detector.detect(frame)
    det_time = (time.time() - start_det) * 1000.0

    if faces is None:
        return [], det_time, 0.0

    persons = []
    start_rec = time.time()
    for face in faces:
        aligned = recognizer.alignCrop(frame, face)
        embedding = recognizer.feature(aligned)

        if embedding is not None:
            person = match_face(embedding.flatten(), face_db)
            # Add bounding box
            x, y, w_box, h_box = face[:4].astype(int)
            person["bbox"] = {"x": int(x), "y": int(y), "w": int(w_box), "h": int(h_box)}
            persons.append(person)
    rec_time = (time.time() - start_rec) * 1000.0

    return persons, det_time, rec_time


class PerformanceLogger:
    """Logs face recognition performance metrics to CSV"""
    def __init__(self, log_dir=None):
        self.csv_writer = None
        self.csv_file = None
        if log_dir:
            os.makedirs(log_dir, exist_ok=True)
            csv_path = os.path.join(log_dir, "face_recognition_performance.csv")
            self.csv_file = open(csv_path, 'w', newline='')
            self.csv_writer = csv.writer(self.csv_file)
            self.csv_writer.writerow(['Frame', 'Time', 'Detection_ms', 'Recognition_ms', 'Persons_Detected'])

    def log(self, frame_count, det_time, rec_time, num_persons):
        if self.csv_writer:
            self.csv_writer.writerow([frame_count, time.time(), f"{det_time:.2f}", f"{rec_time:.2f}", num_persons])
            self.csv_file.flush()

    def close(self):
        if self.csv_file:
            self.csv_file.close()


def main():
    """Main loop - read JSON from stdin, process, write JSON to stdout"""
    # Initialize models
    if not os.path.exists(FACE_DET_MODEL) or not os.path.exists(FACE_REC_MODEL):
        print(json.dumps({"error": "Face models not found", "face_det_model": os.path.abspath(FACE_DET_MODEL), "face_rec_model": os.path.abspath(FACE_REC_MODEL)}))
        sys.exit(1)

    detector = cv2.FaceDetectorYN.create(FACE_DET_MODEL, "", IMAGE_SIZE)
    recognizer = MobileFaceRecognizer(FACE_REC_MODEL, use_gpu=False)  # Use CPU for stability
    face_db = load_face_database()

    logger = PerformanceLogger(log_dir=os.path.join(SCRIPT_DIR, "logs"))

    print(json.dumps({"status": "ready", "persons_in_db": len(face_db)}), flush=True)

    frame_count = 0
    # Process frames from stdin
    for line in sys.stdin:
        try:
            msg = json.loads(line.strip())
            frame_data = msg.get("frame")
            timestamp = msg.get("timestamp", time.time())

            if not frame_data:
                continue

            persons, det_time, rec_time = process_frame(frame_data, detector, recognizer, face_db)
            frame_count += 1

            logger.log(frame_count, det_time, rec_time, len(persons))

            result = {
                "persons": persons,
                "timestamp": timestamp,
                "det_time": det_time,
                "rec_time": rec_time
            }
            print(json.dumps(result), flush=True)

        except json.JSONDecodeError:
            print(json.dumps({"error": "Invalid JSON"}), flush=True)
        except Exception as e:
            print(json.dumps({"error": str(e)}), flush=True)

    logger.close()


if __name__ == "__main__":
    # -----------------------------
    # Query face
    # -----------------------------
    current_result = "Initializing..."

    # Initialize timing variables for averaging
    det_times = []
    rec_times = []
    avg_window_seconds = 1.0  # 5-second time window for SMA

    # Initialize logging variables
    frame_count = 0
    log_update_interval = 5 # Log summary every 30 frames
    det_sma_history = []
    rec_sma_history = []
    time_stamps = []

    # Initialize time-based tracking
    inference_times = []  # Store (timestamp, det_time, rec_time)
    inference_count = 0
    inferences_per_second = 0
    last_inference_reset = time.time()
    last_log_time = time.time()
    not_detected_timestamp = 0
    current_timestamp = 0

    # Initialize FPS tracking using OpenCV's TickMeter
    fps_meter = cv2.TickMeter()
    fps_history = []

    # Setup CSV logging
    csv_filename = f"./logs/face_recognition_performance.csv"
    csv_file = open(csv_filename, 'w', newline='')
    csv_writer = csv.writer(csv_file)
    csv_writer.writerow(['Frame','Time', 'Detection_SMA_ms', 'Recognition_SMA_ms', 'FPS', 'FPS_SMA', 'Inferences_Per_Second'])

    print(f"Performance data will be logged to: {csv_filename}")
    try:
        # Initialize camera
        cap = cv2.VideoCapture(0)
        if not cap.isOpened():
            print("Error: Could not open camera")
            exit()
        cap.set(cv2.CAP_PROP_BRIGHTNESS, 1.0)

        print("Face recognition started. Press 'q' to quit.")

        while True:
            hasFrame, frame = cap.read()
            if not hasFrame:
                print("Error: Could not read frame")
                break

            frame_count += 1

            # Keep only last 30 FPS measurements for averaging
            if len(fps_history) > 30:
                fps_history.pop(0)

            # Calculate average FPS (SMA)
            avg_fps = sum(fps_history) / len(fps_history) if fps_history else 0

            # Start FPS measurement
            fps_meter.start()
            query_embedding, face_coords, det_time, rec_time = extract_embedding_from_frame(frame)
            # Stop FPS measurement and get current FPS
            fps_meter.stop()
            fps = fps_meter.getFPS()
            fps_history.append(fps)

            if query_embedding is not None:
                current_result = match_face(query_embedding)
                print(f"Recognition result: {current_result}")

                # Track inference timing with timestamps
                current_timestamp = time.time() - not_detected_timestamp
                inference_times.append((current_timestamp, det_time, rec_time))
                inference_count += 1

                # Remove old data outside the time window (5 seconds)
                cutoff_time = current_timestamp - avg_window_seconds
                inference_times = [(t, d, r) for t, d, r in inference_times if t >= cutoff_time]

                # Log to CSV and update console summary every 5 seconds
                if time.time() - last_log_time >= avg_window_seconds:
                    last_log_time = time.time()
                    # Calculate time-based SMA
                    inferences_per_second = len(inference_times) / avg_window_seconds
                    if inference_times:
                        recent_det_times = [d for _, d, _ in inference_times]
                        recent_rec_times = [r for _, _, r in inference_times]
                        avg_det = sum(recent_det_times) / len(recent_det_times)
                        avg_rec = sum(recent_rec_times) / len(recent_rec_times)
                    else:
                        avg_det = avg_rec = 0

                    # Write to CSV
                    csv_writer.writerow([frame_count, current_timestamp, avg_det, avg_rec, fps, avg_fps, inferences_per_second])
                    csv_file.flush()  # Ensure data is written immediately

                    # Console summary
                    # print("\n" + "="*90)
                    # print(f"PERFORMANCE SUMMARY - Frame {frame_count}")
                    # print("="*90)
                    # print(f"Current: Detection {det_time:.2f}ms | Recognition {rec_time:.2f}ms | FPS: {fps:.1f} (SMA: {avg_fps:.1f}) | Inf/sec: {inferences_per_second}")
                    # print(f"SMA ({avg_window_seconds}s window): Detection {avg_det:.2f}ms | Recognition {avg_rec:.2f}ms | FPS SMA: {avg_fps:.1f}")

                    # # Calculate statistics
                    # print(f"Inferences in last {avg_window_seconds}s: {len(inference_times)}")
                    # print("="*90 + "\n")

                print(f"Face detection: {det_time:.2f}ms | Face recognition: {rec_time:.2f}ms")
                # Draw bounding box around detected face
                if face_coords is not None:
                    x, y, w, h = int(face_coords[0]), int(face_coords[1]), int(face_coords[2]), int(face_coords[3])
                    cv2.rectangle(frame, (x, y), (x + w, y + h), (0, 255, 0), 2)
            else:
                if(current_result != "No face detected ..."):
                    current_result = "No face detected ..."
                    print(current_result)
                frame_count -= 1
                not_detected_timestamp = time.time() - current_timestamp


            # Add text overlay with current result and FPS
            cv2.putText(frame, current_result, (10, 30),
                        cv2.FONT_HERSHEY_SIMPLEX, 0.8, (0, 255, 0), 2)

            # Add FPS and inference display with SMA
            perf_text = f"FPS: {fps:.1f} (SMA: {avg_fps:.1f}) | Inf/sec: {inferences_per_second}"
            cv2.putText(frame, perf_text, (10, 60),
                        cv2.FONT_HERSHEY_SIMPLEX, 0.6, (0, 255, 255), 2)

            # Display frame
            cv2.imshow("Face Recognition", frame)

            # Check for quit key
            if cv2.waitKey(1) & 0xFF == ord('q'):
                break

    except KeyboardInterrupt:
        cap.release()
        cv2.destroyAllWindows()
        csv_file.close()
        print(f"\nPerformance data saved to: {csv_filename}")
        print(f"Total frames processed: {frame_count}")
        if det_sma_history and rec_sma_history:
            print(f"Final SMA: Detection {np.mean(det_sma_history[-10:]):.2f}ms | Recognition {np.mean(rec_sma_history[-10:]):.2f}ms")
        if fps_history:
            print(f"Final FPS: Current {fps:.1f} | SMA {avg_fps:.1f} | Min {min(fps_history):.1f} | Max {max(fps_history):.1f}")
