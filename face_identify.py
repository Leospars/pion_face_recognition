import cv2
import numpy as np
import os
import time
import csv
import sys
import onnxruntime as ort
from my_logger import log
from frame_pipe import read_frames as read_frames_from_pipe

# -----------------------------
# Configuration
# -----------------------------
FACE_DET_MODEL = "D:/Code_Main/Final_Year_Project/SBC/face_recog/models/face_detection_yunet_2023mar.onnx"
FACE_REC_MODEL = "D:/Code_Main/Final_Year_Project/SBC/face_recog/models/mobileface_v1.0_infer.onnx"
IMAGE_SIZE = (320, 320)
COSINE_THRESHOLD = 0.5   # higher = stricter
KNOWN_FACES_DIR = "D:/Code_Main/Final_Year_Project/SBC/webrtc_video/known_faces"

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

        log.info(f"MobileFaceRecognizer initialized with model: {model_path}")

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
def load_face_database(update=False, face_db_dir = KNOWN_FACES_DIR):
    """Load known faces from database
        If update=True, recompute all images and save to centroid.npy

    Directory layout expected:
        face_db/
          <person_id>/
            gallery/          ← source images (only used when update=True)
              *.png
            centroid.npy      ← pre-computed mean embedding
    """

    face_db = {}
    if not os.path.exists(face_db_dir):
        log.error(f"Faces database directory '{os.path.abspath(face_db_dir)}' not found")
        return face_db

    for dir in os.listdir(face_db_dir):
        name = dir
        face_db[name] = []
        person_path = os.path.join(face_db_dir, dir)

        if update:
            gallery_path = os.path.join(person_path, "gallery")
            if os.path.isdir(gallery_path):
                for file in os.listdir(gallery_path):
                    if file.split(".")[-1] in ["jpg", "jpeg", "png", "webp"]:
                        log.debug(f"Creating embedding for {name} from {file}")
                        emb = extract_embedding_from_frame(cv2.imread(os.path.join(gallery_path, file)))[0]
                        if emb is not None:
                            face_db[name].append(emb)

            if len(face_db[name]) > 0:
                np_arr = np.array(face_db[name])
                centroid = np.mean(np_arr, axis=0)
                # store embedding in directory
                np.save(f"{face_db_dir}/{dir}/centroid.npy", centroid)

        for file in os.listdir(person_path):
            if file.endswith("centroid.npy"):
                log.debug(f"Using stored centroid embedding {file} for {name}")
                emb = np.load(os.path.join(person_path, file))
                face_db[name] = [emb]
                break
    return face_db

# ----------------------------------------------------
# OPTION 2: Manual cosine similarity (RECOMMENDED)
# ----------------------------------------------------
def match_face(query_embedding: np.ndarray, face_db=None):
    if face_db is None:
        face_db = globals().get('face_db', {})
    if len(face_db) == 0:
        return "No Face DB"

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
        return best_name, best_score
    else:
        return "Unknown", best_score

face_detected = False
def process_frame(frame_data, detector: cv2.FaceDetectorYN, recognizer: MobileFaceRecognizer, face_db: dict):
    """Process a single frame and return identified persons with timing info"""

    # Ensure it is a valid numpy array
    if isinstance(frame_data, np.ndarray):
        frame = frame_data
    else:
        try:
            frame = np.asarray(frame_data, dtype=np.uint8)
        except Exception as e:
            log.error(f"Error converting frame data to numpy array: {e}")
            return [], 0.0, 0.0

    if frame is None:
        return [], 0.0, 0.0
    try:
        h, w = frame.shape[:2]
        detector.setInputSize((w, h))

        start_det = time.time()
        _, faces = detector.detect(frame)
        det_time = (time.time() - start_det) * 1000.0

        global face_detected
        if faces is None:
            if not face_detected:
                log.debug("No faces detected")
                face_detected = True
            return [], det_time, 0.0

        face_detected = False
        persons = []
        start_rec = time.time()
        for face in faces:
            aligned = recognizer.alignCrop(frame, face)
            embedding = recognizer.feature(aligned)

            if embedding is not None:
                name, score = match_face(embedding.flatten(), face_db)
                # Add bounding box
                x, y, w_box, h_box = face[:4].astype(int)
                person = {"name": name, "confidence": float(score), "bbox": {"x": int(x), "y": int(y), "w": int(w_box), "h": int(h_box)}}
                persons.append(person)
        rec_time = (time.time() - start_rec) * 1000.0

        return persons, det_time, rec_time
    except Exception as e:
        log.exception(f"Error processing frame: {e}")
        return [], 0.0, 0.0


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
    import argparse
    import os
    from frame_pipe import read_frames as read_frames_from_pipe
    import json

    SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
    DEFAULT_LOG_DIR = os.path.join(SCRIPT_DIR, "logs")
    global KNOWN_FACES_DIR

    arg_parser = argparse.ArgumentParser()
    arg_parser.add_argument("-i", "--input", default="-")
    arg_parser.add_argument("--format", default="rgb24")
    arg_parser.add_argument("--width", type=int, default=None)
    arg_parser.add_argument("--height", type=int, default=None)
    arg_parser.add_argument("--log-dir", default=DEFAULT_LOG_DIR)
    arg_parser.add_argument("--face-det-model", default=FACE_DET_MODEL)
    arg_parser.add_argument("--face-rec-model", default=FACE_REC_MODEL)
    arg_parser.add_argument("--update-face-db", action="store_true")
    arg_parser.add_argument("--db", default=KNOWN_FACES_DIR)
    arg_parser.add_argument("--display", action="store_true")
    args = arg_parser.parse_args()

    # Example terminal call
    # ffmpeg -hide_banner -f dshow -pixel_format yuyv422 -video_size 1920x1080 -rtbufsize 10M -i video="Arducam USB Camera" -f `
    # rawvideo -pix_fmt yuyv422 -r 5 pipe:1 | & "D:\Code_Main\Final_Year_Project\SBC\face_recog\.venv\Scripts\python.exe" -u `
    # "d:\Code_Main\Final_Year_Project\SBC\webrtc_video\face_identify.py" --format "rgb24" --width 1920 --height 1080

    """Main loop - process frames from ffmpeg stdin, write JSON to stdout"""
    # Initialize models
    if not os.path.exists(args.face_det_model) or not os.path.exists(args.face_rec_model):
        log.error("Face models not found", extra={"face_det_model": os.path.abspath(args.face_det_model), "face_rec_model": os.path.abspath(args.face_rec_model)})
        sys.exit(1)

    if args.log_dir == DEFAULT_LOG_DIR:
        if not os.path.exists(DEFAULT_LOG_DIR):
            log.info("Creating default log directory", extra={"log_dir": os.path.abspath(DEFAULT_LOG_DIR)})
            os.makedirs(DEFAULT_LOG_DIR, exist_ok=True)

    if not os.path.exists(args.log_dir):
        log.error("Log directory not found", extra={"log_dir": os.path.abspath(args.log_dir)})
        sys.exit(1)

    format_type = args.format
    if format_type == "grayscale":
        channels = 1
    elif format_type == "yuyv422":
        channels = 2
    else:
        channels = 3

    detector = cv2.FaceDetectorYN.create(args.face_det_model, "", IMAGE_SIZE)
    recognizer = MobileFaceRecognizer(args.face_rec_model, use_gpu=False)  # Use CPU for stability
    KNOWN_FACES_DIR = args.db
    face_db = load_face_database(args.update_face_db)
    if(len(face_db) == 0):
        log.error("No persons in database")
        sys.exit(1)
    perfLog = PerformanceLogger(args.log_dir)
    def process_frame_util(frame, detector: cv2.FaceDetectorYN, recognizer: MobileFaceRecognizer, face_db: dict, perfLog: PerformanceLogger):
        frame_count = 0
        # Process frames from stdin
        try:
            persons, det_time, rec_time = process_frame(frame, detector, recognizer, face_db)
            frame_count += 1

            perfLog.log(frame_count, det_time, rec_time, len(persons))

            result = {
                "persons": persons,
                "timestamp": float(time.time()),
                "det_time": float(det_time),
                "rec_time": float(rec_time)
            }
            print(json.dumps(result), flush=True)

        except Exception as e:
            log.exception("Error processing frame", extra={"error": str(e)})

        return persons

    read_frames_from_pipe(args.input, args.width, args.height, channels, args.display, lambda frame: process_frame_util(frame, detector, recognizer, face_db, perfLog))
    perfLog.close()

if __name__ == "__main__":
    main()
