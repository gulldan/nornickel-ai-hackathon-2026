import pathlib
import sys
import tempfile
import unittest
from unittest import mock

import cv2
import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from diagram_parser_service.tiles import render_tiles4_ocr, split_grid, split_tiles4  # noqa: E402


class TilesTest(unittest.TestCase):
    def test_split_tiles4_adds_overlap(self):
        image = np.zeros((100, 200, 3), dtype=np.uint8)

        tiles = split_tiles4(image, overlap_px=10)

        self.assertEqual([tile.bbox for tile in tiles], [
            (0, 0, 110, 60),
            (90, 0, 200, 60),
            (0, 40, 110, 100),
            (90, 40, 200, 100),
        ])

    def test_split_grid_supports_three_by_three(self):
        image = np.zeros((90, 90, 3), dtype=np.uint8)

        tiles = split_grid(image, grid_size=3, overlap_px=5)

        self.assertEqual(len(tiles), 9)
        self.assertEqual(tiles[0].bbox, (0, 0, 35, 35))
        self.assertEqual(tiles[4].bbox, (25, 25, 65, 65))
        self.assertEqual(tiles[8].bbox, (55, 55, 90, 90))
        self.assertEqual((tiles[8].row, tiles[8].col), (2, 2))

    def test_render_tiles4_ocr_renders_available_tile_text(self):
        with tempfile.NamedTemporaryFile(suffix=".png") as image_file:
            image = np.zeros((40, 80, 3), dtype=np.uint8)
            self.assertTrue(cv2.imwrite(image_file.name, image))

            with mock.patch(
                "diagram_parser_service.tiles.post_ocr",
                side_effect=[
                    {"text": "Грохочение"},
                    RuntimeError("timeout"),
                    {"text": "Дробление 1 ст."},
                    {"text": ""},
                ],
            ):
                rendered = render_tiles4_ocr(pathlib.Path(image_file.name), "http://ocr", "PaddleOCR-VL", 10)

        self.assertIn("tile_ocr_status: partial:tile_2:ocr_failed:RuntimeError", rendered)
        self.assertIn("tile 1 row=0 col=0", rendered)
        self.assertIn("Грохочение", rendered)
        self.assertIn("Дробление 1 ст.", rendered)


if __name__ == "__main__":
    unittest.main()
