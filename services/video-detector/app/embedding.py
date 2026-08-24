from __future__ import annotations

import numpy as np
import torch
import torch.nn as nn
from PIL import Image
from torchvision import models, transforms


class FaceEmbeddingModel:
    def __init__(self, device: str = "cpu"):
        self.device = torch.device(device)

        model = models.resnet18(weights=models.ResNet18_Weights.DEFAULT)

        # Remove classification layer.
        self.model = nn.Sequential(*list(model.children())[:-1])

        self.model.to(self.device)
        self.model.eval()

        self.transform = transforms.Compose([
            transforms.Resize((224, 224)),
            transforms.ToTensor(),
            transforms.Normalize(
                mean=[0.485, 0.456, 0.406],
                std=[0.229, 0.224, 0.225],
            ),
        ])

    @torch.no_grad()
    def embed(self, face: np.ndarray) -> np.ndarray:
        """
        face:
            OpenCV BGR numpy array.

        returns:
            512-dimensional normalized embedding.
        """

        if face is None:
            raise ValueError("Face is None")

        if not isinstance(face, np.ndarray):
            raise TypeError(
                f"Expected numpy.ndarray, got {type(face)}"
            )

        if face.size == 0:
            raise ValueError("Empty face image")

        # Grayscale -> RGB
        if face.ndim == 2:
            face = np.stack([face] * 3, axis=-1)

        # BGR -> RGB
        if face.ndim == 3 and face.shape[2] == 3:
            face = face[:, :, ::-1]

        # Make sure PIL receives uint8.
        if face.dtype != np.uint8:
            face = np.clip(face, 0, 255).astype(np.uint8)

        image = Image.fromarray(face)

        tensor = self.transform(image)
        tensor = tensor.unsqueeze(0).to(self.device)

        embedding = self.model(tensor)

        embedding = embedding.flatten(1)

        # L2 normalization
        embedding = torch.nn.functional.normalize(
            embedding,
            p=2,
            dim=1,
        )

        return embedding[0].cpu().numpy()