from __future__ import annotations

import random
from pathlib import Path

import torch
from huggingface_hub import hf_hub_download
from PIL import Image
from torch.utils.data import DataLoader, Dataset
from torchvision import transforms

from app.evaluate import collect_videos, extract_faces
from app.face import FaceDetector
from app.model import MODEL_FILE, MODEL_REPO, FaceForgeModel

DATASET_DIR = Path("samples/dataset")
CACHE_DIR = Path("training_cache")
CHECKPOINT_OUT = Path("models/finetuned_classifier.pth")

# Fake videos held out entirely from training, used only to measure
# generalization to videos the model has never seen in any form.
HOLDOUT_FAKE_VIDEOS = {"fake_11.mp4", "fake_12.mp4", "fake_13.mp4"}

# Every Nth frame is cached, to cut down on near-duplicate adjacent
# frames and keep extraction/training time manageable.
FRAME_STRIDE = 3

VAL_FRACTION = 0.2


def build_cache() -> None:
    """
    Extract face crops from every video into training_cache/,
    organized as:

        training_cache/<label>/<video_stem>/<frame_idx>.jpg

    Skips videos that are already cached.
    """

    face_detector = FaceDetector()

    for label, subdir in (("real", "real"), ("fake", "fake")):
        for video_path in collect_videos(DATASET_DIR / subdir):
            out_dir = CACHE_DIR / label / video_path.stem

            if out_dir.exists() and any(out_dir.iterdir()):
                continue

            out_dir.mkdir(parents=True, exist_ok=True)

            print(f"Extracting faces: {video_path.name}")

            total_frames, _, faces = extract_faces(
                video_path,
                face_detector,
            )

            for idx, face in enumerate(faces):
                if idx % FRAME_STRIDE != 0:
                    continue

                out_path = out_dir / f"{idx:05d}.jpg"

                Image.fromarray(face[:, :, ::-1]).save(out_path, quality=95)

            print(f"  cached {len(list(out_dir.iterdir()))} crops")


def list_samples():
    """
    Build (path, label, split) tuples for every cached crop.

    split is "train", "val_same_video", or "val_holdout".
    """

    samples = []

    for label_dir in (CACHE_DIR / "real", CACHE_DIR / "fake"):
        label = label_dir.name

        for video_dir in sorted(label_dir.iterdir()):
            crops = sorted(video_dir.iterdir())

            if not crops:
                continue

            video_name = video_dir.name + ".mp4"

            if video_name in HOLDOUT_FAKE_VIDEOS:
                for crop in crops:
                    samples.append((crop, label, "val_holdout"))
                continue

            split_point = int(len(crops) * (1 - VAL_FRACTION))

            for crop in crops[:split_point]:
                samples.append((crop, label, "train"))

            for crop in crops[split_point:]:
                samples.append((crop, label, "val_same_video"))

    return samples


class CropDataset(Dataset):
    def __init__(self, samples, transform):
        self.samples = samples
        self.transform = transform

    def __len__(self):
        return len(self.samples)

    def __getitem__(self, index):
        path, label, _ = self.samples[index]

        image = Image.open(path).convert("RGB")
        tensor = self.transform(image)

        # Matches DeepfakeDetector: class 0 = real, class 1 = fake.
        target = 0 if label == "real" else 1

        return tensor, target


def evaluate(model, loader, device):
    model.eval()

    correct = 0
    total = 0

    with torch.inference_mode():
        for images, targets in loader:
            images = images.to(device)
            targets = targets.to(device)

            logits = model(images)
            predicted = logits.argmax(dim=1)

            correct += int((predicted == targets).sum())
            total += targets.size(0)

    return correct / total if total else 0.0


def main() -> None:
    build_cache()

    samples = list_samples()

    train_samples = [s for s in samples if s[2] == "train"]
    val_same_video = [s for s in samples if s[2] == "val_same_video"]
    val_holdout = [s for s in samples if s[2] == "val_holdout"]

    n_real_train = sum(1 for s in train_samples if s[1] == "real")
    n_fake_train = sum(1 for s in train_samples if s[1] == "fake")

    print()
    print(f"Train crops:          {len(train_samples)} (real={n_real_train}, fake={n_fake_train})")
    print(f"Val (same video):     {len(val_same_video)}")
    print(f"Val (holdout videos): {len(val_holdout)}")
    print()
    print(
        "CAUTION: only one REAL video exists in this dataset, so every "
        "'real' crop comes from the same person/background/lighting. The "
        "model may learn to recognize that specific video rather than "
        "general real-vs-fake cues. Val (holdout videos) is the only "
        "number here that reflects real generalization, and even that is "
        "only evaluated on FAKE videos since there is no second REAL video "
        "to hold out."
    )
    print()

    transform = transforms.Compose(
        [
            transforms.Resize((224, 224)),
            transforms.ToTensor(),
            transforms.Normalize(mean=[0.5, 0.5, 0.5], std=[0.5, 0.5, 0.5]),
        ]
    )

    train_dataset = CropDataset(train_samples, transform)
    val_same_dataset = CropDataset(val_same_video, transform)
    val_holdout_dataset = CropDataset(val_holdout, transform)

    # Weighted sampling to counter the real/fake crop imbalance.
    class_counts = {0: n_real_train, 1: n_fake_train}
    weights = [
        1.0 / class_counts[0 if label == "real" else 1]
        for _, label, _ in train_samples
    ]

    sampler = torch.utils.data.WeightedRandomSampler(
        weights,
        num_samples=len(weights),
        replacement=True,
    )

    train_loader = DataLoader(train_dataset, batch_size=32, sampler=sampler, num_workers=0)
    val_same_loader = DataLoader(val_same_dataset, batch_size=32, num_workers=0)
    val_holdout_loader = DataLoader(val_holdout_dataset, batch_size=32, num_workers=0) if val_holdout else None

    device = torch.device("mps" if torch.backends.mps.is_available() else ("cuda" if torch.cuda.is_available() else "cpu"))
    print(f"Training on {device}")

    model = FaceForgeModel()

    checkpoint_path = hf_hub_download(repo_id=MODEL_REPO, filename=MODEL_FILE)
    checkpoint = torch.load(checkpoint_path, map_location="cpu")
    model.load_state_dict(checkpoint["model_state_dict"])
    model.to(device)

    # Freeze the Xception backbone; only fine-tune the small
    # classification head. The dataset is far too small (a few
    # thousand crops from 14 videos) to safely fine-tune a
    # 20M-parameter feature extractor without collapsing to
    # memorizing this specific dataset.
    for param in model.xception.parameters():
        param.requires_grad = False

    optimizer = torch.optim.AdamW(model.classifier.parameters(), lr=1e-4, weight_decay=1e-4)
    criterion = torch.nn.CrossEntropyLoss()

    best_holdout_acc = -1.0
    epochs = 8

    for epoch in range(1, epochs + 1):
        model.train()

        running_loss = 0.0

        for images, targets in train_loader:
            images = images.to(device)
            targets = targets.to(device)

            optimizer.zero_grad()
            logits = model(images)
            loss = criterion(logits, targets)
            loss.backward()
            optimizer.step()

            running_loss += loss.item() * images.size(0)

        train_loss = running_loss / len(train_dataset)
        same_video_acc = evaluate(model, val_same_loader, device)
        holdout_acc = evaluate(model, val_holdout_loader, device) if val_holdout_loader else float("nan")

        print(
            f"Epoch {epoch}/{epochs}  "
            f"loss={train_loss:.4f}  "
            f"val_same_video_acc={same_video_acc:.4f}  "
            f"val_holdout_acc={holdout_acc:.4f}"
        )

        if val_holdout_loader and holdout_acc >= best_holdout_acc:
            best_holdout_acc = holdout_acc

            CHECKPOINT_OUT.parent.mkdir(exist_ok=True)

            torch.save(
                {"model_state_dict": model.state_dict()},
                CHECKPOINT_OUT,
            )

    print()
    print(f"Best holdout accuracy: {best_holdout_acc:.4f}")
    print(f"Saved checkpoint: {CHECKPOINT_OUT}")


if __name__ == "__main__":
    main()
