from app.evaluate import (
    calculate_metrics,
    collect_videos,
)


def test_collect_videos(tmp_path):
    real_dir = tmp_path / "real"
    real_dir.mkdir()

    (real_dir / "video1.mp4").touch()
    (real_dir / "video2.mov").touch()
    (real_dir / "notes.txt").touch()

    videos = collect_videos(real_dir)

    assert len(videos) == 2
    assert videos[0].suffix in {".mp4", ".mov"}
    assert videos[1].suffix in {".mp4", ".mov"}


def test_calculate_metrics():
    results = [
        {
            "expected": "REAL",
            "predicted": "REAL",
            "correct": True,
        },
        {
            "expected": "REAL",
            "predicted": "FAKE",
            "correct": False,
        },
        {
            "expected": "FAKE",
            "predicted": "FAKE",
            "correct": True,
        },
        {
            "expected": "FAKE",
            "predicted": "REAL",
            "correct": False,
        },
    ]

    metrics = calculate_metrics(results)

    assert metrics["accuracy"] == 0.5
    assert metrics["precision"] == 0.5
    assert metrics["recall"] == 0.5
    assert metrics["f1"] == 0.5