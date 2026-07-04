use std::collections::BTreeSet;

use crate::coords::{range_a1, workbook_uri};
use crate::model::{BlockSidecar, CellSidecar, ParsedRow, ParsedSheet};

use super::headers::{data_rows, headers_for};
use super::markdown::{push_markdown_block, MarkdownBlockContext};
use super::sidecar::sidecar_cells;

const ROW_WINDOW: usize = 25;

pub(super) struct RenderedSheet {
    pub text: String,
    pub blocks: Vec<BlockSidecar>,
    pub cells: Vec<CellSidecar>,
}

impl RenderedSheet {
    pub(super) fn formula_count(&self) -> usize {
        self.cells
            .iter()
            .filter(|cell| !cell.formula.trim().is_empty())
            .count()
    }
}

pub(super) fn render_sheet(file_name: &str, sheet: &ParsedSheet) -> RenderedSheet {
    let mut text = String::new();
    let mut blocks = Vec::new();
    let mut cells = Vec::new();
    for (block_index, block_rows) in contiguous_blocks(&sheet.rows).iter().enumerate() {
        let Some((col_start, col_end)) = column_bounds(block_rows) else {
            continue;
        };
        let headers = headers_for(block_rows, col_start, col_end);
        for window in data_rows(block_rows).chunks(ROW_WINDOW) {
            if window.is_empty() {
                continue;
            }
            let row_start = window.first().map_or(block_rows[0].row, |row| row.row);
            let row_end = window.last().map_or(row_start, |row| row.row);
            let range = range_a1(row_start, col_start, row_end, col_end);
            let source_uri =
                workbook_uri(file_name, &sheet.name, &range, row_start, row_end, &headers);
            push_markdown_block(
                &mut text,
                MarkdownBlockContext {
                    file_name,
                    sheet_name: &sheet.name,
                    source_uri: &source_uri,
                    range: &range,
                    col_start,
                    col_end,
                    headers: &headers,
                    rows: window,
                },
            );
            blocks.push(BlockSidecar {
                block_id: format!("{}:{}", sheet.name, block_index + 1),
                source_uri,
                range_a1: range,
                row_start,
                row_end,
                col_start,
                col_end,
                headers: headers.clone(),
            });
        }
        cells.extend(sidecar_cells(block_rows, col_start, &headers));
    }
    RenderedSheet {
        text,
        blocks,
        cells,
    }
}

fn contiguous_blocks(rows: &[ParsedRow]) -> Vec<Vec<ParsedRow>> {
    let mut blocks = Vec::new();
    let mut current = Vec::new();
    let mut previous_row = None;
    for row in rows {
        if previous_row.is_some_and(|prev| row.row > prev + 1) && !current.is_empty() {
            blocks.push(current);
            current = Vec::new();
        }
        current.push(row.clone());
        previous_row = Some(row.row);
    }
    if !current.is_empty() {
        blocks.push(current);
    }
    blocks
}

fn column_bounds(rows: &[ParsedRow]) -> Option<(u32, u32)> {
    let cols: BTreeSet<u32> = rows
        .iter()
        .flat_map(|row| row.cells.iter())
        .filter(|cell| !cell.is_empty())
        .map(|cell| cell.col)
        .collect();
    Some((*cols.first()?, *cols.last()?))
}
