import pathlib
import tempfile
import unittest
from unittest import mock

import cv2
import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]

from diagram_parser_service.cv_parser import parse_diagram  # noqa: E402
from diagram_parser_service.cv_parser.labels import clean_diagram_label  # noqa: E402
from diagram_parser_service.cv_parser.models import (  # noqa: E402
    DetectedBlock,
    DiagramParseResult,
    OCRBlockText,
    ParserConfig,
)
from diagram_parser_service.cv_parser.ocr import confidence_from_payload, recognize_block_crops  # noqa: E402


class CVParserTest(unittest.TestCase):
    def test_detects_blocks_and_connector_on_synthetic_flow(self):
        with tempfile.TemporaryDirectory() as tmp:
            image_path = pathlib.Path(tmp) / "flow.png"
            image = np.full((140, 360, 3), 255, dtype=np.uint8)
            cv2.rectangle(image, (25, 45), (115, 100), (0, 0, 0), 2)
            cv2.rectangle(image, (225, 45), (315, 100), (0, 0, 0), 2)
            cv2.arrowedLine(image, (118, 72), (222, 72), (0, 0, 0), 2, tipLength=0.18)
            cv2.imwrite(str(image_path), image)

            result = parse_diagram(
                "synthetic-flow",
                image_path,
                ParserConfig(min_block_area=800, block_merge_gap=8, max_connection_distance=60),
            )

        self.assertGreaterEqual(len(result.blocks), 2)
        self.assertGreaterEqual(len(result.connectors), 1)
        prediction = result.to_prediction()
        self.assertEqual(prediction["diagram_id"], "synthetic-flow")
        self.assertGreaterEqual(len(prediction["nodes"]), 2)
        self.assertGreaterEqual(len(prediction["edges"]), 1)
        self.assertEqual(prediction["visual_review"], "bad")
        self.assertEqual(len(prediction["evidence"]["text_blocks"]), len(prediction["nodes"]))
        self.assertEqual(prediction["evidence"]["reading_order"], [node["id"] for node in prediction["nodes"]])

    def test_prediction_preserves_top_left_text_block_sequence(self):
        result = DiagramParseResult(
            diagram_id="layout-order",
            image_path=pathlib.Path("layout.png"),
            width=300,
            height=220,
            blocks=[
                DetectedBlock("bottom-left", (20, 150, 90, 190), 2800, 0.8),
                DetectedBlock("top-right", (180, 20, 260, 60), 3200, 0.8),
                DetectedBlock("top-left", (20, 20, 100, 60), 3200, 0.8),
            ],
            connectors=[],
        )

        prediction = result.to_prediction()
        evidence = prediction["evidence"]

        self.assertEqual(prediction["edges"], [])
        self.assertEqual(evidence["reading_order"], ["top-left", "top-right", "bottom-left"])
        self.assertEqual([node["id"] for node in prediction["nodes"]], evidence["reading_order"])
        self.assertEqual([block["text"] for block in evidence["text_blocks"]], ["block 1", "block 2", "block 3"])
        self.assertEqual(
            evidence["layout_sequence_edges"],
            [
                {
                    "source": "top-left",
                    "target": "top-right",
                    "label": "layout_next",
                    "policy": "top_left_to_bottom_right",
                },
                {
                    "source": "top-right",
                    "target": "bottom-left",
                    "label": "layout_next",
                    "policy": "top_left_to_bottom_right",
                },
            ],
        )

    def test_prediction_uses_attached_ocr_text(self):
        result = DiagramParseResult(
            diagram_id="ocr-labels",
            image_path=pathlib.Path("layout.png"),
            width=300,
            height=120,
            blocks=[
                DetectedBlock("left", (20, 20, 100, 60), 3200, 0.8),
                DetectedBlock("right", (180, 20, 260, 60), 3200, 0.8),
            ],
            connectors=[],
            ocr_texts={
                "left": OCRBlockText("Питание флотации", 0.9),
                "right": OCRBlockText("1 основная флотация", 0.9),
            },
        )

        prediction = result.to_prediction()

        self.assertEqual([node["label"] for node in prediction["nodes"]], ["Питание флотации", "1 основная флотация"])
        self.assertEqual([node["text_status"] for node in prediction["nodes"]], ["ocr", "ocr"])
        self.assertEqual(prediction["evidence"]["ocr_block_count"], 2)

    def test_prediction_skips_table_only_ocr_nodes(self):
        result = DiagramParseResult(
            diagram_id="ocr-table-noise",
            image_path=pathlib.Path("layout.png"),
            width=300,
            height=120,
            blocks=[
                DetectedBlock("stage", (20, 20, 150, 45), 3250, 0.8),
                DetectedBlock("table", (20, 70, 150, 100), 3900, 0.8),
            ],
            connectors=[],
            ocr_texts={
                "stage": OCRBlockText("Дробление 2 ст. КСД 2200 Гр (1 шт.)", 0.9),
                "table": OCRBlockText("<table><tr><td>95,9</td><td>1 510 235</td></tr></table>", 0.9),
            },
        )

        prediction = result.to_prediction()

        self.assertEqual([node["label"] for node in prediction["nodes"]], ["Дробление 2 ст."])
        self.assertEqual(prediction["evidence"]["node_count"], 1)
        self.assertEqual(prediction["evidence"]["rejected_ocr_block_count"], 1)
        self.assertEqual(len(prediction["evidence"]["text_blocks"]), 2)
        self.assertEqual(
            [block["label_status"] for block in prediction["evidence"]["text_blocks"]],
            ["canonical", "empty"],
        )

    def test_cleans_paddle_html_and_equipment_noise(self):
        self.assertEqual(
            clean_diagram_label("Грохочение Двухситный грохот Sibra 2DR (4 шт.)"),
            "Грохочение Двухситный грохот Sibra 2DR",
        )
        self.assertEqual(clean_diagram_label("В бункеры ИФЦ <table><tr><td>95,81</td></tr></table>"), "В бункеры ИФЦ")
        self.assertEqual(clean_diagram_label("<div><img src='diagram.jpg' /></div>"), "")
        self.assertEqual(
            clean_diagram_label("<table><tr><td colspan='4'>0твальные хвосты</td></tr></table>"),
            "Отвальные хвосты",
        )
        self.assertEqual(clean_diagram_label("1-10-22 ↘ 1 основнал флотация"), "1 основная флотация")
        self.assertEqual(clean_diagram_label("↑1-27 3 основная фпотация"), "3 основная флотация")
        self.assertEqual(
            clean_diagram_label("___ ↓ ◯ ► t = 13-11 Перечистка первял меднал"),
            "Перечистка первая медная",
        )
        self.assertEqual(clean_diagram_label("## Вода в операцию:"), "")
        self.assertEqual(clean_diagram_label("Пески <table><tr><td>5,32</td></tr></table>"), "")

    def test_ocr_confidence_uses_payload_score_only(self):
        self.assertEqual(confidence_from_payload({"text": "Флотация"}), 0.0)
        self.assertEqual(confidence_from_payload({"confidence": 1.5}), 1.0)
        self.assertEqual(confidence_from_payload({"score": -0.1}), 0.0)
        self.assertEqual(confidence_from_payload({"confidence": True}), 0.0)

    def test_ocr_crop_failure_keeps_block_with_warning(self):
        with tempfile.TemporaryDirectory() as tmp:
            image_path = pathlib.Path(tmp) / "crop.png"
            image = np.full((80, 160, 3), 255, dtype=np.uint8)
            cv2.rectangle(image, (20, 20), (120, 55), (0, 0, 0), 2)
            cv2.imwrite(str(image_path), image)

            block = DetectedBlock("stage", (20, 20, 120, 55), 3500, 0.8)
            with mock.patch(
                "diagram_parser_service.cv_parser.ocr.post_ocr",
                side_effect=RuntimeError("paddle failed"),
            ):
                texts = recognize_block_crops(image_path, [block], "http://ocr", "PaddleOCR-VL")

        self.assertIn("stage", texts)
        self.assertEqual(texts["stage"].text, "")
        self.assertEqual(texts["stage"].confidence, 0.0)
        self.assertEqual(texts["stage"].warnings, ("ocr_failed:RuntimeError",))


if __name__ == "__main__":
    unittest.main()
