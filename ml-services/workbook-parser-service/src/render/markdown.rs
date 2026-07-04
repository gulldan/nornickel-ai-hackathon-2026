use crate::coords::{range_a1, workbook_uri};
use crate::model::ParsedRow;

pub(super) struct MarkdownBlockContext<'a> {
    pub file_name: &'a str,
    pub sheet_name: &'a str,
    pub source_uri: &'a str,
    pub range: &'a str,
    pub col_start: u32,
    pub col_end: u32,
    pub headers: &'a [String],
    pub rows: &'a [ParsedRow],
}

pub(super) fn push_markdown_block(text: &mut String, context: MarkdownBlockContext<'_>) {
    if !text.is_empty() {
        text.push('\n');
    }
    text.push_str(&format!(
        "### Sheet: {}, range {}\n",
        context.sheet_name, context.range
    ));
    text.push_str(&format!("source_uri={}\n", context.source_uri));
    text.push_str("block_type=table\n\n");
    text.push_str("| source_uri | row | ");
    text.push_str(
        &context
            .headers
            .iter()
            .map(|h| markdown_cell(h))
            .collect::<Vec<_>>()
            .join(" | "),
    );
    text.push_str(" |\n|---|---:|");
    text.push_str(&"---|".repeat(context.headers.len()));
    text.push('\n');
    for row in context.rows {
        let row_range = row_range(&context, row);
        let row_uri = row_uri(&context, row, &row_range);
        text.push_str("| ");
        text.push_str(&row_uri);
        text.push_str(" | ");
        text.push_str(&row.row.to_string());
        text.push_str(" | ");
        text.push_str(
            &context
                .headers
                .iter()
                .enumerate()
                .map(|(idx, _)| {
                    let col = context.col_start + idx as u32;
                    markdown_cell(&value_with_formula(row, col))
                })
                .collect::<Vec<_>>()
                .join(" | "),
        );
        text.push_str(" |\n");
    }
    text.push('\n');
    for row in context.rows {
        push_row_evidence(text, &context, row);
    }
}

fn markdown_cell(value: &str) -> String {
    value
        .replace('|', "\\|")
        .replace('\n', " ")
        .trim()
        .to_string()
}

fn value_with_formula(row: &ParsedRow, col: u32) -> String {
    let Some(cell) = row.cells.iter().find(|cell| cell.col == col) else {
        return String::new();
    };
    if cell.formula.is_empty() {
        cell.value.clone()
    } else if cell.value.is_empty() {
        format!("formula: {}", cell.formula)
    } else {
        format!("{} (formula: {})", cell.value, cell.formula)
    }
}

fn push_row_evidence(text: &mut String, context: &MarkdownBlockContext<'_>, row: &ParsedRow) {
    let row_range = row_range(context, row);
    let row_uri = row_uri(context, row, &row_range);
    text.push_str("source_uri=");
    text.push_str(&row_uri);
    text.push('\n');
    text.push_str("block_type=table_row\n");
    text.push_str("row=");
    text.push_str(&row.row.to_string());
    text.push_str(" | ");
    text.push_str(
        &context
            .headers
            .iter()
            .enumerate()
            .filter_map(|(idx, header)| {
                let col = context.col_start + idx as u32;
                let value = evidence_value(row, col);
                if header.trim().is_empty() && value.trim().is_empty() {
                    None
                } else {
                    Some(format!(
                        "{}={}",
                        evidence_cell(header),
                        evidence_cell(&value)
                    ))
                }
            })
            .collect::<Vec<_>>()
            .join(" | "),
    );
    text.push_str("\n\n");
}

fn row_range(context: &MarkdownBlockContext<'_>, row: &ParsedRow) -> String {
    range_a1(row.row, context.col_start, row.row, context.col_end)
}

fn row_uri(context: &MarkdownBlockContext<'_>, row: &ParsedRow, row_range: &str) -> String {
    workbook_uri(
        context.file_name,
        context.sheet_name,
        row_range,
        row.row,
        row.row,
        context.headers,
    )
}

fn evidence_cell(value: &str) -> String {
    value
        .replace(['\n', '\r'], " ")
        .replace('|', "\\|")
        .trim()
        .to_string()
}

fn evidence_value(row: &ParsedRow, col: u32) -> String {
    value_with_formula(row, col)
}
