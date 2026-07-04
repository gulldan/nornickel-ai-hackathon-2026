use std::collections::BTreeMap;

use crate::model::ParsedRow;

pub(super) fn headers_for(rows: &[ParsedRow], col_start: u32, col_end: u32) -> Vec<String> {
    let use_first_row = rows.len() > 1;
    let raw = (col_start..=col_end).map(|col| {
        if use_first_row {
            value_at(&rows[0], col)
        } else {
            String::new()
        }
    });
    unique_headers(raw, col_start)
}

pub(super) fn data_rows(rows: &[ParsedRow]) -> &[ParsedRow] {
    if rows.len() > 1 {
        &rows[1..]
    } else {
        rows
    }
}

fn unique_headers(values: impl Iterator<Item = String>, col_start: u32) -> Vec<String> {
    let mut seen: BTreeMap<String, usize> = BTreeMap::new();
    values
        .enumerate()
        .map(|(idx, value)| {
            let mut header = normalize_header(&value);
            if header.is_empty() {
                header = format!(
                    "column_{}",
                    crate::coords::column_label(col_start + idx as u32)
                );
            }
            let count = seen.entry(header.clone()).or_insert(0);
            *count += 1;
            if *count == 1 {
                header
            } else {
                format!("{header}_{}", *count)
            }
        })
        .collect()
}

fn normalize_header(value: &str) -> String {
    value.split_whitespace().collect::<Vec<_>>().join(" ")
}

fn value_at(row: &ParsedRow, col: u32) -> String {
    row.cells
        .iter()
        .find(|cell| cell.col == col)
        .map_or_else(String::new, |cell| cell.value.clone())
}
