"""Local OpenCV diagram graph parser."""

from diagram_parser_service.cv_parser.graph import parse_diagram
from diagram_parser_service.cv_parser.models import DetectedBlock, DetectedConnector, DiagramParseResult

__all__ = ["DetectedBlock", "DetectedConnector", "DiagramParseResult", "parse_diagram"]
