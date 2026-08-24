from __future__ import annotations

import json
import sys
from pathlib import Path

from app.diagnostic import (
    analyze_video,
)
from app.embedding import (
    FaceEmbeddingModel,
)
from app.face import (
    FaceDetector,
)
from app.model import (
    DeepfakeDetector,
)


def print_report(report):
    print()
    print("=" * 60)
    print(
        f"Video: {report['video']}"
    )
    print("=" * 60)

    print(
        f"Frames:          {report['frames']}"
    )

    print(
        f"Faces detected:  "
        f"{report['faces_detected']}"
    )

    print(
        f"Embedding frames: "
        f"{report['embedding_frames']}"
    )

    print()

    print("Xception")
    print("--------")

    print(
        f"Mean fake:       "
        f"{report['fake_mean']:.4f}"
    )

    print(
        f"Median fake:     "
        f"{report['fake_median']:.4f}"
    )

    print(
        f"P90 fake:        "
        f"{report['fake_p90']:.4f}"
    )

    print(
        f"Max fake:        "
        f"{report['fake_max']:.4f}"
    )

    consistency = report[
        "embedding_consistency"
    ]

    temporal = report[
        "temporal_changes"
    ]

    if consistency:
        print()
        print("Embedding Consistency")
        print("---------------------")

        print(
            f"Mean similarity:   "
            f"{consistency['mean_similarity']:.4f}"
        )

        print(
            f"Median similarity: "
            f"{consistency['median_similarity']:.4f}"
        )

        print(
            f"Min similarity:    "
            f"{consistency['min_similarity']:.4f}"
        )

    if temporal:
        print()
        print("Temporal Changes")
        print("----------------")

        print(
            f"Mean change:        "
            f"{temporal['mean_change']:.4f}"
        )

        print(
            f"Median change:      "
            f"{temporal['median_change']:.4f}"
        )

        print(
            f"Max change:         "
            f"{temporal['max_change']:.4f}"
        )

        print(
            f"Large change ratio: "
            f"{temporal['large_change_ratio']:.2%}"
        )


def main():

    if len(sys.argv) != 2:
        print(
            "Usage:"
        )
        print(
            "python -m app.analyze "
            "samples/dataset/fake/fake_02.mp4"
        )
        sys.exit(1)

    video_path = Path(
        sys.argv[1]
    )

    if not video_path.exists():
        raise FileNotFoundError(
            video_path
        )

    print(
        "MithyaX Diagnostic Analyzer"
    )
    print(
        "==========================="
    )

    print(
        "Loading Xception..."
    )

    detector = DeepfakeDetector()

    print(
        "Loading face detector..."
    )

    face_detector = FaceDetector()

    print(
        "Loading embedding model..."
    )

    embedding_model = (
        FaceEmbeddingModel()
    )

    print(
        "Analyzing every frame..."
    )

    report = analyze_video(
        video_path,
        detector,
        face_detector,
        embedding_model,
    )

    print_report(report)

    output_dir = Path(
        "reports"
    )

    output_dir.mkdir(
        exist_ok=True
    )

    output_file = (
        output_dir
        / f"{video_path.stem}.json"
    )

    with output_file.open(
        "w",
        encoding="utf-8",
    ) as file:
        json.dump(
            report,
            file,
            indent=2,
        )

    print()
    print(
        f"Report saved: {output_file}"
    )


if __name__ == "__main__":
    main()