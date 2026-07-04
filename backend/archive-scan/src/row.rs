use serde::{
    de::{Deserializer, Error as DeError},
    ser::SerializeStruct,
    Deserialize, Serialize, Serializer,
};
use std::{borrow::Cow, sync::Arc};

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ArchiveMeta {
    #[cfg_attr(feature = "service", schema(value_type = u32))]
    pub archive_index: u32,
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub archive_name: Box<str>,
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub archive_path: Box<str>,
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub archive_ext: Box<str>,
    pub archive_size: u64,
    pub archive_mtime_unix: i64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum DetectionSource {
    Magic,
    Magika,
    Heuristic,
    Unknown,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Hash, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum EntryKind {
    File,
    Directory,
    Other,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug)]
pub struct EntryRow {
    #[cfg_attr(feature = "service", schema(value_type = ArchiveMeta))]
    pub archive: Arc<ArchiveMeta>,
    pub entry_index: u64,
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub entry_name: Box<str>,
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub entry_ext: Box<str>,
    pub entry_kind: EntryKind,
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub label: Cow<'static, str>,
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub mime: Cow<'static, str>,
    pub detected_by: DetectionSource,
    pub confidence: f32,
    pub is_nested_archive: bool,
    pub header_len: u32,
    pub bytes_scanned: u64,
    pub truncated_scan: bool,
    #[cfg_attr(feature = "service", schema(value_type = Option<String>))]
    pub head_b3: Option<Box<str>>,
    #[cfg_attr(feature = "service", schema(value_type = Option<String>))]
    pub full_b3: Option<Box<str>>,
}

pub type SharedEntryRow = Arc<EntryRow>;

impl Serialize for EntryRow {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        let field_count =
            18 + usize::from(self.head_b3.is_some()) + usize::from(self.full_b3.is_some());
        let mut state = serializer.serialize_struct("EntryRow", field_count)?;

        state.serialize_field("archive_index", &self.archive.archive_index)?;
        state.serialize_field("archive_name", &self.archive.archive_name)?;
        state.serialize_field("archive_path", &self.archive.archive_path)?;
        state.serialize_field("archive_ext", &self.archive.archive_ext)?;
        state.serialize_field("archive_size", &self.archive.archive_size)?;
        state.serialize_field("archive_mtime_unix", &self.archive.archive_mtime_unix)?;
        state.serialize_field("entry_index", &self.entry_index)?;
        state.serialize_field("entry_name", &self.entry_name)?;
        state.serialize_field("entry_ext", &self.entry_ext)?;
        state.serialize_field("entry_kind", &self.entry_kind)?;
        state.serialize_field("label", &self.label)?;
        state.serialize_field("mime", &self.mime)?;
        state.serialize_field("detected_by", &self.detected_by)?;
        state.serialize_field("confidence", &self.confidence)?;
        state.serialize_field("is_nested_archive", &self.is_nested_archive)?;
        state.serialize_field("header_len", &self.header_len)?;
        state.serialize_field("bytes_scanned", &self.bytes_scanned)?;
        state.serialize_field("truncated_scan", &self.truncated_scan)?;
        if let Some(head_b3) = &self.head_b3 {
            state.serialize_field("head_b3", head_b3)?;
        }
        if let Some(full_b3) = &self.full_b3 {
            state.serialize_field("full_b3", full_b3)?;
        }
        state.end()
    }
}

#[derive(Deserialize)]
struct EntryRowSerde {
    archive_index: u32,
    archive_name: Box<str>,
    archive_path: Box<str>,
    archive_ext: Box<str>,
    archive_size: u64,
    archive_mtime_unix: i64,
    entry_index: u64,
    entry_name: Box<str>,
    entry_ext: Box<str>,
    entry_kind: EntryKind,
    label: Box<str>,
    mime: Box<str>,
    detected_by: DetectionSource,
    confidence: f32,
    is_nested_archive: bool,
    header_len: u32,
    bytes_scanned: u64,
    truncated_scan: bool,
    #[serde(default)]
    head_b3: Option<Box<str>>,
    #[serde(default)]
    full_b3: Option<Box<str>>,
}

impl<'de> Deserialize<'de> for EntryRow {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        let raw = EntryRowSerde::deserialize(deserializer)?;
        if raw.mime.is_empty() && raw.label.is_empty() {
            return Err(D::Error::custom("entry row label must not be blank"));
        }

        Ok(Self {
            archive: Arc::new(ArchiveMeta {
                archive_index: raw.archive_index,
                archive_name: raw.archive_name,
                archive_path: raw.archive_path,
                archive_ext: raw.archive_ext,
                archive_size: raw.archive_size,
                archive_mtime_unix: raw.archive_mtime_unix,
            }),
            entry_index: raw.entry_index,
            entry_name: raw.entry_name,
            entry_ext: raw.entry_ext,
            entry_kind: raw.entry_kind,
            label: Cow::Owned(raw.label.into_string()),
            mime: Cow::Owned(raw.mime.into_string()),
            detected_by: raw.detected_by,
            confidence: raw.confidence,
            is_nested_archive: raw.is_nested_archive,
            header_len: raw.header_len,
            bytes_scanned: raw.bytes_scanned,
            truncated_scan: raw.truncated_scan,
            head_b3: raw.head_b3,
            full_b3: raw.full_b3,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample_row() -> EntryRow {
        EntryRow {
            archive: Arc::new(ArchiveMeta {
                archive_index: 1,
                archive_name: "sample.zip".into(),
                archive_path: "/tmp/sample.zip".into(),
                archive_ext: "zip".into(),
                archive_size: 1_024,
                archive_mtime_unix: 1_700_000_000,
            }),
            entry_index: 7,
            entry_name: "nested/file.txt".into(),
            entry_ext: "txt".into(),
            entry_kind: EntryKind::File,
            label: Cow::Borrowed("text"),
            mime: Cow::Borrowed("text/plain"),
            detected_by: DetectionSource::Magic,
            confidence: 1.0,
            is_nested_archive: false,
            header_len: 64,
            bytes_scanned: 64,
            truncated_scan: true,
            head_b3: Some("head".into()),
            full_b3: None,
        }
    }

    #[test]
    fn entry_row_serialization_includes_optional_hashes_when_present() {
        let mut row = sample_row();
        row.full_b3 = Some("full".into());
        let value = serde_json::to_value(row).expect("entry row should serialize to JSON");

        assert_eq!(value["archive_name"], "sample.zip");
        assert_eq!(value["entry_name"], "nested/file.txt");
        assert_eq!(value["detected_by"], "magic");
        assert_eq!(value["entry_kind"], "file");
        assert_eq!(value["head_b3"], "head");
        assert_eq!(value["full_b3"], "full");
    }

    #[test]
    fn detection_source_uses_lowercase_serialization() {
        let value = serde_json::to_value(DetectionSource::Magika)
            .expect("detection source should serialize");
        assert_eq!(value, "magika");
    }

    #[test]
    fn entry_kind_uses_snake_case_serialization() {
        let value =
            serde_json::to_value(EntryKind::Directory).expect("entry kind should serialize");
        assert_eq!(value, "directory");
    }
}
