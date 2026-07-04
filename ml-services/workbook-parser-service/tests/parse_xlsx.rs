use std::io::{Cursor, Write};

use workbook_parser_service::parser::parse_workbook;
use workbook_parser_service::render::render_response;
use zip::write::SimpleFileOptions;
use zip::ZipWriter;

#[test]
fn parses_xlsx_into_anchored_markdown_and_sidecar() {
    let workbook = parse_workbook(&xlsx_fixture(), "flotation.xlsx").expect("parse workbook");
    let response = render_response(workbook).expect("render response");

    assert_eq!(response.metadata["workbook_mode"], "anchored_markdown");
    assert_eq!(response.metadata["workbook_parser_engine"], "calamine");
    assert!(response
        .text
        .contains("source_uri=flotation.xlsx#sheet=Flotation"));
    assert!(response.text.contains("columns=Ni%20loss%20%28%25%29"));
    assert!(response.text.contains("| T-001 | 12.4 | 8.1 |"));
    assert!(response.text.contains("block_type=table_row"));
    assert!(response
        .text
        .contains("sample=T-001 | Ni loss (%)=12.4 | Cu loss (%)=8.1"));
    assert!(response.text.contains("SUM(B2:B3)"));
    assert!(response.sidecars[0].text.contains("\"address\": \"B2\""));
}

fn xlsx_fixture() -> Vec<u8> {
    let mut cursor = Cursor::new(Vec::new());
    {
        let mut zip = ZipWriter::new(&mut cursor);
        write_part(&mut zip, "[Content_Types].xml", CONTENT_TYPES);
        write_part(&mut zip, "_rels/.rels", ROOT_RELS);
        write_part(&mut zip, "xl/workbook.xml", WORKBOOK);
        write_part(&mut zip, "xl/_rels/workbook.xml.rels", WORKBOOK_RELS);
        write_part(&mut zip, "xl/worksheets/sheet1.xml", SHEET);
        zip.finish().expect("finish zip");
    }
    cursor.into_inner()
}

fn write_part(zip: &mut ZipWriter<&mut Cursor<Vec<u8>>>, name: &str, content: &str) {
    zip.start_file(name, SimpleFileOptions::default())
        .expect("start file");
    zip.write_all(content.as_bytes()).expect("write part");
}

const CONTENT_TYPES: &str = r#"<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
</Types>"#;

const ROOT_RELS: &str = r#"<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>"#;

const WORKBOOK: &str = r#"<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Flotation" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>"#;

const WORKBOOK_RELS: &str = r#"<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
</Relationships>"#;

const SHEET: &str = r#"<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1">
      <c r="A1" t="inlineStr"><is><t>sample</t></is></c>
      <c r="B1" t="inlineStr"><is><t>Ni loss (%)</t></is></c>
      <c r="C1" t="inlineStr"><is><t>Cu loss (%)</t></is></c>
    </row>
    <row r="2">
      <c r="A2" t="inlineStr"><is><t>T-001</t></is></c>
      <c r="B2"><v>12.4</v></c>
      <c r="C2"><v>8.1</v></c>
    </row>
    <row r="3">
      <c r="A3" t="inlineStr"><is><t>T-002</t></is></c>
      <c r="B3"><v>10.9</v></c>
      <c r="C3"><f>SUM(B2:B3)</f><v>23.3</v></c>
    </row>
  </sheetData>
</worksheet>"#;
