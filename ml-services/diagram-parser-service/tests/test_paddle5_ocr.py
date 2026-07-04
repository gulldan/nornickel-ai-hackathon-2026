import pathlib
import sys
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from diagram_parser_service.paddle5_ocr import OCRLabel, labels_from_page, render_paddle5_ocr  # noqa: E402


class FakePage:
    def __init__(self, payload):
        self._payload = payload

    def json(self):
        return self._payload


class Paddle5OcrTest(unittest.TestCase):
    def test_labels_from_page_reads_nested_paddle_result(self):
        page = FakePage(
            {
                "res": {
                    "rec_texts": [" Грохочение ", "", "Дробление 1 ст."],
                    "rec_scores": [0.91, 0.7, "0.87"],
                    "dt_polys": [
                        [[10, 20], [90, 20], [90, 40], [10, 40]],
                        [[0, 0], [1, 0], [1, 1], [0, 1]],
                        [[15.4, 50.2], [120, 50], [120, 70], [15, 70]],
                    ],
                }
            }
        )

        labels = labels_from_page(page)

        self.assertEqual(labels, [
            OCRLabel(text="Грохочение", score=0.91, bbox=(10, 20, 90, 40)),
            OCRLabel(text="Дробление 1 ст.", score=0.87, bbox=(15, 50, 120, 70)),
        ])

    def test_render_paddle5_ocr_renders_indexable_text(self):
        with mock.patch(
            "diagram_parser_service.paddle5_ocr.recognize_labels",
            return_value=[
                OCRLabel(text="Грохочение", score=0.91, bbox=(10, 20, 90, 40)),
                OCRLabel(text="Дробление 1 ст.", score=0.87, bbox=(15, 50, 120, 70)),
            ],
        ):
            rendered = render_paddle5_ocr(pathlib.Path("scheme.png"))

        self.assertIn("paddle5_ocr_status: ok", rendered)
        self.assertIn("paddle5_ocr_line_count: 2", rendered)
        self.assertIn('bbox=[10,20,90,40] score=0.910 text="Грохочение"', rendered)
        self.assertIn('text="Дробление 1 ст."', rendered)


if __name__ == "__main__":
    unittest.main()
