import base64
import os
import pathlib
import sys
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from diagram_parser_service import server  # noqa: E402


class FakeParseResult:
    blocks = []

    def to_prediction(self):
        return object()


class DiagramParserServerTest(unittest.TestCase):
    def test_invalid_ocr_mode_falls_back_to_full_image(self):
        with mock.patch.dict(os.environ, {"DIAGRAM_OCR_MODE": "bad-mode"}):
            self.assertEqual(server.diagram_ocr_mode(), "full-image")

    def test_tiles4_ocr_mode_is_supported(self):
        with mock.patch.dict(os.environ, {"DIAGRAM_OCR_MODE": "tiles4"}):
            self.assertEqual(server.diagram_ocr_mode(), "tiles4")

    def test_tiles9_ocr_mode_is_supported(self):
        with mock.patch.dict(os.environ, {"DIAGRAM_OCR_MODE": "tiles9"}):
            self.assertEqual(server.diagram_ocr_mode(), "tiles9")

    def test_paddle5_ocr_mode_is_supported(self):
        with mock.patch.dict(os.environ, {"DIAGRAM_OCR_MODE": "paddle5"}):
            self.assertEqual(server.diagram_ocr_mode(), "paddle5")

    def test_adaptive_ocr_uses_block_ocr_only_for_small_diagrams(self):
        with mock.patch.dict(os.environ, {"DIAGRAM_BLOCK_OCR_MAX_BLOCKS": "12"}):
            self.assertTrue(server.should_use_block_ocr("adaptive", 10))
            self.assertFalse(server.should_use_block_ocr("adaptive", 36))
            self.assertTrue(server.should_use_block_ocr("blocks", 36))
            self.assertFalse(server.should_use_block_ocr("full-image", 10))

    def test_full_image_ocr_renders_text(self):
        with mock.patch(
            "diagram_parser_service.server.post_ocr",
            return_value={"text": "  Дробление 1 ст.\\nДробление 2 ст.  "},
        ):
            rendered = server.render_full_image_ocr(pathlib.Path("diagram.png"), "http://ocr", "PaddleOCR-VL", 10)

        self.assertIn("full_image_ocr_status: ok", rendered)
        self.assertIn("Дробление 1 ст.", rendered)
        self.assertIn("Дробление 2 ст.", rendered)

    def test_describe_appends_paddle5_ocr_when_enabled(self):
        image_b64 = base64.b64encode(b"fake image").decode()
        request = server.DescribeRequest(image_b64=image_b64, mime="image/png")

        with (
            mock.patch.dict(os.environ, {"DIAGRAM_OCR_MODE": "paddle5"}),
            mock.patch("diagram_parser_service.server.parse_diagram", return_value=FakeParseResult()),
            mock.patch("diagram_parser_service.server.render_prediction", return_value="diagram_graph\n"),
            mock.patch(
                "diagram_parser_service.server.render_paddle5_ocr",
                return_value="\npaddle5_ocr_status: ok\npaddle5_ocr_text:\n- text=\"Грохочение\"\n",
            ) as paddle5_ocr,
            mock.patch("diagram_parser_service.server.render_full_image_ocr") as full_image_ocr,
        ):
            response = server.describe(request)

        paddle5_ocr.assert_called_once()
        full_image_ocr.assert_not_called()
        self.assertIn("diagram_graph", response.text)
        self.assertIn("paddle5_ocr_status: ok", response.text)
        self.assertIn("Грохочение", response.text)


if __name__ == "__main__":
    unittest.main()
