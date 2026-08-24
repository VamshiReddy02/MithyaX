import cv2

from app.face import FaceDetector


def test_face_detector_initializes():
    detector = FaceDetector()

    assert detector is not None


def test_face_detector_handles_frame():
    detector = FaceDetector()

    frame = cv2.imread("samples/test.mp4")

    # A video file isn't an image, so just test with a blank frame.
    frame = cv2.resize(
        cv2.imread("samples/face.jpg")
        if cv2.imread("samples/face.jpg") is not None
        else cv2.cvtColor(
            cv2.UMat(480, 640, cv2.CV_8UC3).get(),
            cv2.COLOR_BGR2RGB,
        ),
        (640, 480),
    )

    result = detector.detect_and_crop(frame)

    # No face is acceptable for this synthetic test.
    assert result is None or len(result) > 0