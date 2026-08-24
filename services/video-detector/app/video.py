import cv2


def extract_all_frames(video_path: str):
    """
    Extract every frame from a video.

    Returns:
        list[np.ndarray]
    """

    cap = cv2.VideoCapture(video_path)

    if not cap.isOpened():
        raise RuntimeError(
            f"Could not open video: {video_path}"
        )

    frames = []

    try:
        while True:
            success, frame = cap.read()

            if not success:
                break

            if frame is None:
                continue

            if frame.size == 0:
                continue

            frames.append(frame)

    finally:
        cap.release()

    if not frames:
        raise RuntimeError(
            f"No frames could be extracted "
            f"from video: {video_path}"
        )

    return frames


def sample_frames(
    video_path: str,
    num_frames: int = 16,
):
    """
    Sample approximately num_frames from a video.
    """

    cap = cv2.VideoCapture(video_path)

    if not cap.isOpened():
        raise RuntimeError(
            f"Could not open video: {video_path}"
        )

    total_frames = int(
        cap.get(cv2.CAP_PROP_FRAME_COUNT)
    )

    if total_frames <= 0:
        cap.release()
        raise RuntimeError(
            f"Could not determine frame count: "
            f"{video_path}"
        )

    indices = [
        int(i * total_frames / num_frames)
        for i in range(num_frames)
    ]

    indices = [
        min(index, total_frames - 1)
        for index in indices
    ]

    frames = []

    for index in indices:
        cap.set(
            cv2.CAP_PROP_POS_FRAMES,
            index,
        )

        success, frame = cap.read()

        if success and frame is not None:
            frames.append(frame)

    cap.release()

    if not frames:
        raise RuntimeError(
            f"Could not extract frames from: "
            f"{video_path}"
        )

    return frames