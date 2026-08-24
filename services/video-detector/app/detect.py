import sys

from app.video import extract_all_frames
from app.model import DeepfakeDetector


def print_frame_analysis(result):
    print()
    print("Frame-Level Analysis")
    print("---------------------")

    print(
        f"Frames analyzed: "
        f"{result['frames_analyzed']}"
    )

    print(
        f"Mean fake probability: "
        f"{result['mean_fake_probability']:.4f}"
    )

    print(
        f"Median fake probability: "
        f"{result['median_fake_probability']:.4f}"
    )

    print(
        f"Maximum fake probability: "
        f"{result['max_fake_probability']:.4f}"
    )

    print(
        f"90th percentile: "
        f"{result['p90_fake_probability']:.4f}"
    )

    print(
        f"Suspicious frames: "
        f"{result['suspicious_frames']}/"
        f"{result['frames_analyzed']}"
    )

    print(
        f"Suspicious frame ratio: "
        f"{result['suspicious_frame_ratio']:.2%}"
    )

    print()
    print("Per-frame scores")
    print("----------------")

    for frame in result["frame_results"]:
        fake_probability = frame["fake_probability"]

        if fake_probability >= 0.70:
            label = "FAKE"
        elif fake_probability <= 0.30:
            label = "REAL"
        else:
            label = "UNCERTAIN"

        print(
            f"Frame {frame['frame']:04d}  "
            f"Real: {frame['real_probability']:.4f}  "
            f"Fake: {frame['fake_probability']:.4f}  "
            f"{label}"
        )


def main():
    if len(sys.argv) != 2:
        print(
            "Usage: "
            "python -m app.detect <video>"
        )
        sys.exit(1)

    video_path = sys.argv[1]

    print()
    print("MithyaX Video Detector")
    print("======================")
    print(f"Video: {video_path}")
    print("Analyzing every frame...")
    print()

    frames = extract_all_frames(video_path)

    print(
        f"Frames extracted: {len(frames)}"
    )

    detector = DeepfakeDetector()

    result = detector.predict_all_frames(frames)

    print_frame_analysis(result)


if __name__ == "__main__":
    main()