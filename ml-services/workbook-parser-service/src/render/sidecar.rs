use crate::model::{CellSidecar, ParsedRow};

pub(super) fn sidecar_cells(
    rows: &[ParsedRow],
    col_start: u32,
    headers: &[String],
) -> Vec<CellSidecar> {
    rows.iter()
        .flat_map(|row| {
            row.cells
                .iter()
                .filter(|cell| !cell.is_empty())
                .map(|cell| {
                    let header = headers
                        .get((cell.col - col_start) as usize)
                        .cloned()
                        .unwrap_or_default();
                    CellSidecar {
                        address: cell.address.clone(),
                        row: cell.row,
                        col: cell.col,
                        raw_value: cell.value.clone(),
                        display_value: cell.value.clone(),
                        value_type: cell.value_type.clone(),
                        formula: cell.formula.clone(),
                        unit: unit_from_header(&header),
                        header,
                    }
                })
        })
        .collect()
}

fn unit_from_header(header: &str) -> String {
    if header.contains('%') {
        return "%".to_string();
    }
    for (open, close) in [('(', ')'), ('[', ']')] {
        if let (Some(start), Some(end)) = (header.rfind(open), header.rfind(close)) {
            if start < end {
                return header[start + 1..end].trim().to_string();
            }
        }
    }
    String::new()
}
