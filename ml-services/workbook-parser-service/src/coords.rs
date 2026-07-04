pub fn column_label(mut col: u32) -> String {
    assert!(col > 0, "column index is 1-based");
    let mut out = Vec::new();
    while col > 0 {
        col -= 1;
        out.push((b'A' + (col % 26) as u8) as char);
        col /= 26;
    }
    out.iter().rev().collect()
}

pub fn cell_address(row: u32, col: u32) -> String {
    format!("{}{}", column_label(col), row)
}

pub fn range_a1(row_start: u32, col_start: u32, row_end: u32, col_end: u32) -> String {
    format!(
        "{}:{}",
        cell_address(row_start, col_start),
        cell_address(row_end, col_end)
    )
}

pub fn encode_component(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for byte in value.as_bytes() {
        let value = *byte;
        match value {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(value as char)
            }
            _ => out.push_str(&format!("%{value:02X}")),
        }
    }
    out
}

pub fn workbook_uri(
    file_name: &str,
    sheet: &str,
    range: &str,
    row_start: u32,
    row_end: u32,
    headers: &[String],
) -> String {
    let mut uri = format!(
        "{}#sheet={}&range={}&row_start={}&row_end={}",
        encode_component(file_name),
        encode_component(sheet),
        encode_component(range),
        row_start,
        row_end
    );
    for header in headers.iter().filter(|header| !header.is_empty()).take(16) {
        uri.push_str("&columns=");
        uri.push_str(&encode_component(header));
    }
    uri
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn encodes_a1_coordinates() {
        assert_eq!(column_label(1), "A");
        assert_eq!(column_label(28), "AB");
        assert_eq!(cell_address(12, 28), "AB12");
        assert_eq!(range_a1(1, 1, 2, 3), "A1:C2");
    }

    #[test]
    fn uri_has_no_whitespace() {
        let uri = workbook_uri(
            "my file.xlsx",
            "Лист 1",
            "A1:B2",
            1,
            2,
            &[String::from("Ni loss %")],
        );
        assert!(!uri.chars().any(char::is_whitespace));
        assert!(uri.contains("sheet=%D0%9B"));
        assert!(uri.contains("columns=Ni%20loss%20%25"));
    }
}
