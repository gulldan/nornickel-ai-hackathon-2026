use std::collections::BTreeMap;

use anyhow::{Context, Result};

use crate::model::{ParseResponse, ParsedWorkbook, SheetSidecar, SidecarArtifact, WorkbookSidecar};

mod blocks;
mod headers;
mod markdown;
mod sidecar;

pub fn render_response(workbook: ParsedWorkbook) -> Result<ParseResponse> {
    let mut text = String::new();
    let mut sheets = Vec::new();
    let mut block_count = 0usize;
    let mut formula_count = 0usize;
    let mut cell_count = 0usize;

    for sheet in &workbook.sheets {
        let rendered = blocks::render_sheet(&workbook.file_name, sheet);
        block_count += rendered.blocks.len();
        formula_count += rendered.formula_count();
        cell_count += rendered.cells.len();
        if !text.is_empty() {
            text.push_str("\n\n");
        }
        text.push_str(&rendered.text);
        sheets.push(SheetSidecar {
            name: sheet.name.clone(),
            blocks: rendered.blocks,
            cells: rendered.cells,
        });
    }

    let sidecar = WorkbookSidecar {
        schema_version: 1,
        file_name: workbook.file_name.clone(),
        original_format: workbook.original_format.clone(),
        parser_engine: "calamine".to_string(),
        sheets,
        warnings: workbook.warnings,
    };
    let sidecar_text =
        serde_json::to_string_pretty(&sidecar).context("serialize workbook sidecar")?;

    Ok(ParseResponse {
        text,
        metadata: metadata(
            &workbook.original_format,
            block_count,
            cell_count,
            formula_count,
            sidecar.warnings.len(),
        ),
        sidecars: vec![SidecarArtifact {
            name: "workbook.sidecar.json".to_string(),
            content_type: "application/json".to_string(),
            text: sidecar_text,
        }],
    })
}

fn metadata(
    workbook_format: &str,
    block_count: usize,
    cell_count: usize,
    formula_count: usize,
    warning_count: usize,
) -> BTreeMap<String, String> {
    BTreeMap::from([
        ("workbook_mode".to_string(), "anchored_markdown".to_string()),
        ("workbook_format".to_string(), workbook_format.to_string()),
        ("workbook_parser_engine".to_string(), "calamine".to_string()),
        ("workbook_sidecar_version".to_string(), "1".to_string()),
        ("workbook_block_count".to_string(), block_count.to_string()),
        ("workbook_cell_count".to_string(), cell_count.to_string()),
        (
            "workbook_formula_count".to_string(),
            formula_count.to_string(),
        ),
        (
            "workbook_warning_count".to_string(),
            warning_count.to_string(),
        ),
    ])
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{ParsedCell, ParsedRow, ParsedSheet};

    #[test]
    fn renders_row_anchors_and_sidecar() {
        let workbook = ParsedWorkbook {
            file_name: "book.xlsx".to_string(),
            original_format: "xlsx".to_string(),
            warnings: Vec::new(),
            sheets: vec![ParsedSheet {
                name: "Sheet 1".to_string(),
                rows: vec![row(1, &["sample", "Ni loss (%)"]), row(2, &["T-1", "12.4"])],
            }],
        };
        let response = render_response(workbook).expect("render");
        assert!(response
            .text
            .contains("source_uri=book.xlsx#sheet=Sheet%201"));
        assert!(response.text.contains("columns=Ni%20loss%20%28%25%29"));
        assert!(response.text.contains("| T-1 | 12.4 |"));
        assert!(response.text.contains("block_type=table_row"));
        assert!(response
            .text
            .contains("source_uri=book.xlsx#sheet=Sheet%201&range=A2%3AB2&row_start=2&row_end=2"));
        assert!(response.text.contains("sample=T-1 | Ni loss (%)=12.4"));
        assert_eq!(response.metadata["workbook_block_count"], "1");
        assert_eq!(response.metadata["workbook_cell_count"], "4");
    }

    fn row(row: u32, values: &[&str]) -> ParsedRow {
        ParsedRow {
            row,
            cells: values
                .iter()
                .enumerate()
                .map(|(idx, value)| ParsedCell {
                    row,
                    col: idx as u32 + 1,
                    address: crate::coords::cell_address(row, idx as u32 + 1),
                    value: (*value).to_string(),
                    value_type: "string".to_string(),
                    formula: String::new(),
                })
                .collect(),
        }
    }
}
