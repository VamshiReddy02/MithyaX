from pathlib import Path

import cv2
import numpy as np


class FaceDetector:

    def __init__(self):
        models_dir = (
            Path(__file__).resolve().parent.parent
            / "models"
        )

        model_path = (
            models_dir
            / "face_detection_yunet_2023mar.onnx"
        )

        if not model_path.exists():
            raise FileNotFoundError(
                f"YuNet model not found: {model_path}"
            )

        self.detector = cv2.FaceDetectorYN.create(
            str(model_path),
            "",
            (320, 320),
            score_threshold=0.6,
            nms_threshold=0.3,
            top_k=5000,
        )

    def detect(
        self,
        frame: np.ndarray,
    ) -> np.ndarray:

        height, width = frame.shape[:2]

        self.detector.setInputSize(
            (width, height)
        )

        _, faces = self.detector.detect(
            frame
        )

        if faces is None:
            return np.empty(
                (0, 15),
                dtype=np.float32,
            )

        return faces