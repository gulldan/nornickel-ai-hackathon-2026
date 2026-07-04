use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

#[derive(Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct ParseResponse {
    pub text: String,
    pub metadata: BTreeMap<String, String>,
    pub sidecars: Vec<SidecarArtifact>,
}

#[derive(Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct SidecarArtifact {
    pub name: String,
    pub content_type: String,
    pub text: String,
}

#[derive(Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct WorkbookSidecar {
    pub schema_version: u32,
    pub file_name: String,
    pub original_format: String,
    pub parser_engine: String,
    pub sheets: Vec<SheetSidecar>,
    pub warnings: Vec<String>,
}

#[derive(Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct SheetSidecar {
    pub name: String,
    pub blocks: Vec<BlockSidecar>,
    pub cells: Vec<CellSidecar>,
}

#[derive(Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct BlockSidecar {
    pub block_id: String,
    pub source_uri: String,
    pub range_a1: String,
    pub row_start: u32,
    pub row_end: u32,
    pub col_start: u32,
    pub col_end: u32,
    pub headers: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct CellSidecar {
    pub address: String,
    pub row: u32,
    pub col: u32,
    pub raw_value: String,
    pub display_value: String,
    pub value_type: String,
    pub formula: String,
    pub header: String,
    pub unit: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParsedCell {
    pub row: u32,
    pub col: u32,
    pub address: String,
    pub value: String,
    pub value_type: String,
    pub formula: String,
}

impl ParsedCell {
    pub fn is_empty(&self) -> bool {
        self.value.trim().is_empty() && self.formula.trim().is_empty()
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParsedRow {
    pub row: u32,
    pub cells: Vec<ParsedCell>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParsedSheet {
    pub name: String,
    pub rows: Vec<ParsedRow>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParsedWorkbook {
    pub file_name: String,
    pub original_format: String,
    pub sheets: Vec<ParsedSheet>,
    pub warnings: Vec<String>,
}
