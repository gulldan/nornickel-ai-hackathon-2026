use std::io::Cursor;
use std::path::Path;

use anyhow::{bail, Context, Result};
use calamine::{open_workbook_auto_from_rs, Data, Range, Reader};

use crate::coords::cell_address;
use crate::model::{ParsedCell, ParsedRow, ParsedSheet, ParsedWorkbook};

pub fn parse_workbook(data: &[u8], file_name: &str) -> Result<ParsedWorkbook> {
    let mut workbook =
        open_workbook_auto_from_rs(Cursor::new(data.to_vec())).context("open workbook")?;
    let sheet_names = workbook.sheet_names();
    if sheet_names.is_empty() {
        bail!("workbook has no sheets");
    }

    let mut sheets = Vec::new();
    let mut warnings = Vec::new();
    for sheet_name in sheet_names {
        let range = workbook
            .worksheet_range(&sheet_name)
            .with_context(|| format!("read sheet {sheet_name:?}"))?;
        let formulas = workbook
            .worksheet_formula(&sheet_name)
            .unwrap_or_else(|err| {
                warnings.push(format!("formula range unavailable for {sheet_name}: {err}"));
                Range::empty()
            });
        let rows = parsed_rows(&range, &formulas);
        if !rows.is_empty() {
            sheets.push(ParsedSheet {
                name: sheet_name,
                rows,
            });
        }
    }

    if sheets.is_empty() {
        bail!("workbook has no non-empty cells");
    }

    Ok(ParsedWorkbook {
        file_name: file_name.to_string(),
        original_format: extension(file_name),
        sheets,
        warnings,
    })
}

fn parsed_rows(range: &Range<Data>, formulas: &Range<String>) -> Vec<ParsedRow> {
    let Some((start_row, start_col)) = range.start() else {
        return Vec::new();
    };
    let mut rows = Vec::new();
    for (row_offset, row) in range.rows().enumerate() {
        let row_number = start_row + row_offset as u32 + 1;
        let mut cells = Vec::with_capacity(row.len());
        for (col_offset, value) in row.iter().enumerate() {
            let col_number = start_col + col_offset as u32 + 1;
            let formula = formulas
                .get_value((row_number - 1, col_number - 1))
                .map_or("", String::as_str)
                .trim()
                .to_string();
            cells.push(ParsedCell {
                row: row_number,
                col: col_number,
                address: cell_address(row_number, col_number),
                value: display_value(value),
                value_type: value_type(value).to_string(),
                formula,
            });
        }
        if cells.iter().any(|cell| !cell.is_empty()) {
            rows.push(ParsedRow {
                row: row_number,
                cells,
            });
        }
    }
    rows
}

fn display_value(value: &Data) -> String {
    match value {
        Data::Empty => String::new(),
        Data::Float(number) if number.is_finite() && number.fract() == 0.0 => {
            format!("{number:.0}")
        }
        Data::DateTime(datetime) => datetime
            .as_datetime()
            .map_or_else(|| datetime.to_string(), |value| value.to_string()),
        _ => value.to_string(),
    }
}

fn value_type(value: &Data) -> &'static str {
    match value {
        Data::Int(_) => "int",
        Data::Float(_) => "float",
        Data::String(_) => "string",
        Data::Bool(_) => "bool",
        Data::DateTime(_) | Data::DateTimeIso(_) => "datetime",
        Data::DurationIso(_) => "duration",
        Data::Error(_) => "error",
        Data::Empty => "empty",
    }
}

fn extension(file_name: &str) -> String {
    Path::new(file_name)
        .extension()
        .and_then(|ext| ext.to_str())
        .unwrap_or("unknown")
        .to_ascii_lowercase()
}
