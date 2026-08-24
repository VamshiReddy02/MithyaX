from pathlib import Path

import cv2
import timm
import torch

from PIL import Image
from torchvision import transforms
from huggingface_hub import hf_hub_download


MODEL_REPO = "huzaifanasirrr/faceforge-detector"
MODEL_FILE = "detector_best.pth"

# If app.train has produced a fine-tuned checkpoint, prefer it over
# the base pretrained weights.
FINETUNED_CHECKPOINT = (
    Path(__file__).resolve().parent.parent
    / "models"
    / "finetuned_classifier.pth"
)


class FaceForgeModel(torch.nn.Module):
    def __init__(self):
        super().__init__()

        self.xception = timm.create_model(
            "xception",
            pretrained=False,
            num_classes=0,
        )

        self.classifier = torch.nn.Sequential(
            torch.nn.Dropout(0.5),
            torch.nn.Linear(2048, 512),
            torch.nn.ReLU(),
            torch.nn.Dropout(0.3),
            torch.nn.Linear(512, 2),
        )

    def forward(self, x):
        features = self.xception(x)
        return self.classifier(features)


class DeepfakeDetector:

    def __init__(self):
        self.device = self._get_device()

        print(
            f"Loading Xception detector on "
            f"{self.device}..."
        )

        if FINETUNED_CHECKPOINT.exists():
            model_path = FINETUNED_CHECKPOINT

            print(
                f"Using fine-tuned checkpoint: {model_path}"
            )

        else:
            model_path = hf_hub_download(
                repo_id=MODEL_REPO,
                filename=MODEL_FILE,
            )

        checkpoint = torch.load(
            model_path,
            map_location="cpu",
        )

        self.model = FaceForgeModel()

        self.model.load_state_dict(
            checkpoint["model_state_dict"]
        )

        self.model.to(self.device)
        self.model.eval()

        self.transform = transforms.Compose(
            [
                transforms.Resize((224, 224)),
                transforms.ToTensor(),
                transforms.Normalize(
                    mean=[0.5, 0.5, 0.5],
                    std=[0.5, 0.5, 0.5],
                ),
            ]
        )

        print("Model loaded.")

    def _get_device(self):
        if torch.backends.mps.is_available():
            return torch.device("mps")

        if torch.cuda.is_available():
            return torch.device("cuda")

        return torch.device("cpu")

    def predict_image(self, image):
        """
        Predict a single PIL image.

        Returns:
            {
                "label": "REAL" | "FAKE",
                "real_probability": float,
                "fake_probability": float,
            }
        """

        if not isinstance(image, Image.Image):
            raise TypeError(
                "image must be a PIL.Image.Image"
            )

        image = image.convert("RGB")

        tensor = self.transform(image)
        tensor = tensor.unsqueeze(0).to(self.device)

        with torch.inference_mode():
            logits = self.model(tensor)

            probabilities = torch.softmax(
                logits,
                dim=1,
            )

        real_probability = (
            probabilities[0, 0].item()
        )

        fake_probability = (
            probabilities[0, 1].item()
        )

        if fake_probability >= real_probability:
            label = "FAKE"
        else:
            label = "REAL"

        return {
            "label": label,
            "real_probability": real_probability,
            "fake_probability": fake_probability,
        }

    def _prepare_frame(self, frame):
        """
        Convert an OpenCV frame into RGB PIL image.
        """

        if frame is None:
            return None

        if not isinstance(frame, torch.Tensor):
            pass
        else:
            raise TypeError(
                "Expected OpenCV numpy frame, not torch.Tensor"
            )

        if frame.ndim == 2:
            frame = cv2.cvtColor(
                frame,
                cv2.COLOR_GRAY2BGR,
            )

        elif (
            frame.ndim == 3
            and frame.shape[2] == 1
        ):
            frame = cv2.cvtColor(
                frame,
                cv2.COLOR_GRAY2BGR,
            )

        elif (
            frame.ndim == 3
            and frame.shape[2] == 4
        ):
            frame = cv2.cvtColor(
                frame,
                cv2.COLOR_BGRA2BGR,
            )

        if (
            frame.ndim != 3
            or frame.shape[2] != 3
        ):
            return None

        rgb = cv2.cvtColor(
            frame,
            cv2.COLOR_BGR2RGB,
        )

        return Image.fromarray(rgb)

    def predict_face(self, face):
        """
        Predict a single face crop.

        OpenCV face crops are BGR numpy arrays.

        Returns:
            {
                "real_probability": float,
                "fake_probability": float,
            }
        """

        image = self._prepare_frame(face)

        if image is None:
            return {
                "real_probability": 0.0,
                "fake_probability": 0.0,
            }

        result = self.predict_image(image)

        return {
            "real_probability": float(
                result["real_probability"]
            ),
            "fake_probability": float(
                result["fake_probability"]
            ),
        }

    def predict_all_frames(self, frames):
        """
        Predict every frame and return aggregate
        video-level statistics.

        Input:
            List of OpenCV BGR numpy arrays.

        Output:
            Dictionary containing:

            - mean_fake_probability
            - median_fake_probability
            - p75_fake_probability
            - p90_fake_probability
            - max_fake_probability
            - suspicious_frame_ratio
            - suspicious_frames
            - frames_analyzed
        """

        if frames is None or len(frames) == 0:
            raise ValueError(
                "No frames provided"
            )

        fake_probabilities = []

        for frame in frames:

            if frame is None:
                continue

            image = self._prepare_frame(frame)

            if image is None:
                continue

            tensor = self.transform(
                image
            )

            tensor = (
                tensor
                .unsqueeze(0)
                .to(self.device)
            )

            with torch.inference_mode():
                logits = self.model(tensor)

                probabilities = torch.softmax(
                    logits,
                    dim=1,
                )

            fake_probability = (
                probabilities[0, 1].item()
            )

            fake_probabilities.append(
                fake_probability
            )

        if not fake_probabilities:
            raise RuntimeError(
                "No valid frames could be analyzed"
            )

        values = torch.tensor(
            fake_probabilities,
            dtype=torch.float32,
        )

        mean_fake_probability = (
            values.mean().item()
        )

        median_fake_probability = (
            values.median().item()
        )

        p75_fake_probability = (
            torch.quantile(
                values,
                0.75,
            ).item()
        )

        p90_fake_probability = (
            torch.quantile(
                values,
                0.90,
            ).item()
        )

        max_fake_probability = (
            values.max().item()
        )

        # A frame is considered suspicious
        # when fake probability >= 0.70.
        suspicious_frames = sum(
            probability >= 0.70
            for probability
            in fake_probabilities
        )

        frames_analyzed = len(
            fake_probabilities
        )

        suspicious_frame_ratio = (
            suspicious_frames
            / frames_analyzed
        )

        return {
            "mean_fake_probability":
                mean_fake_probability,

            "median_fake_probability":
                median_fake_probability,

            "p75_fake_probability":
                p75_fake_probability,

            "p90_fake_probability":
                p90_fake_probability,

            "max_fake_probability":
                max_fake_probability,

            "suspicious_frames":
                suspicious_frames,

            "suspicious_frame_ratio":
                suspicious_frame_ratio,

            "frames_analyzed":
                frames_analyzed,
        }

    def predict(self, frames):
        """
        Backwards-compatible video prediction.

        Returns:
            {
                "label": "REAL" | "FAKE" | "UNCERTAIN",
                "real_probability": float,
                "fake_probability": float,
                "frames_analyzed": int,
            }
        """

        stats = self.predict_all_frames(
            frames
        )

        fake_probability = (
            stats["mean_fake_probability"]
        )

        real_probability = (
            1.0 - fake_probability
        )

        if fake_probability >= 0.70:
            label = "FAKE"

        elif fake_probability <= 0.30:
            label = "REAL"

        else:
            label = "UNCERTAIN"

        return {
            "label": label,
            "real_probability":
                real_probability,
            "fake_probability":
                fake_probability,
            "frames_analyzed":
                stats["frames_analyzed"],
        }