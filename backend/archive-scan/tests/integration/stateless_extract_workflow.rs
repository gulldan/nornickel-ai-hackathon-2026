#![cfg(feature = "service")]

use crate::support::{
    assert_postgres_extract_metadata, postgres_fixture, read_json, sample_zip_archive,
    seaweedfs_fixture, ServiceProcess,
};
use reqwest::StatusCode;

#[test]
fn service_e2e_extracts_upload_to_seaweedfs_s3_and_postgres_metadata() {
    let Some(postgres) = postgres_fixture() else {
        return;
    };
    let Some(seaweed) = seaweedfs_fixture("extract-bucket") else {
        return;
    };

    let archive = sample_zip_archive();
    let extraction_id = format!("it-extract-{}", std::process::id());
    let env = vec![
        ("ARCHIVE_SCAN_EXTRACT_STORE_BACKEND".to_owned(), "s3".to_owned()),
        ("ARCHIVE_SCAN_EXTRACT_STORE_S3_ENDPOINT".to_owned(), seaweed.endpoint.clone()),
        ("ARCHIVE_SCAN_EXTRACT_STORE_S3_REGION".to_owned(), "us-east-1".to_owned()),
        ("ARCHIVE_SCAN_EXTRACT_STORE_S3_BUCKET".to_owned(), seaweed.bucket.clone()),
        ("ARCHIVE_SCAN_EXTRACT_STORE_S3_KEY_PREFIX".to_owned(), "integration/extracted".to_owned()),
        ("ARCHIVE_SCAN_EXTRACT_STORE_S3_ACCESS_KEY_ID".to_owned(), "admin".to_owned()),
        ("ARCHIVE_SCAN_EXTRACT_STORE_S3_SECRET_ACCESS_KEY".to_owned(), "secret".to_owned()),
        ("ARCHIVE_SCAN_EXTRACT_STORE_S3_PATH_STYLE".to_owned(), "1".to_owned()),
        ("ARCHIVE_SCAN_EXTRACT_METADATA_BACKEND".to_owned(), "postgres".to_owned()),
        ("ARCHIVE_SCAN_EXTRACT_METADATA_POSTGRES_URL".to_owned(), postgres.url.clone()),
        (
            "ARCHIVE_SCAN_EXTRACT_METADATA_POSTGRES_TABLE_PREFIX".to_owned(),
            postgres.table_prefix.clone(),
        ),
        ("ARCHIVE_SCAN_EXTRACT_METADATA_BATCH_SIZE".to_owned(), "1".to_owned()),
    ];

    let service = ServiceProcess::spawn_with_env(&env);
    let readyz = service
        .client()
        .get(service.url("/readyz"))
        .send()
        .expect("service should expose readiness");
    assert_eq!(readyz.status(), StatusCode::OK);

    let form = reqwest::blocking::multipart::Form::new()
        .file("archive", archive.path())
        .expect("multipart archive field should be created")
        .text("extraction_id", extraction_id.clone())
        .text("fast_only", "true")
        .text("include_entries", "true")
        .text("full_hash", "true");
    let response = service
        .client()
        .post(service.url("/v1/extract/upload"))
        .multipart(form)
        .send()
        .expect("service should extract uploaded archive");
    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response);
    assert_eq!(value["extraction_id"], extraction_id);
    assert_eq!(value["destination"]["backend"], "s3");
    assert_eq!(value["total_entries"], 2);
    assert_eq!(value["stored_files"], 2);
    assert_eq!(value["entries"].as_array().map(Vec::len), Some(2));
    assert!(value["entries"][0]["stored_object"]["uri"]
        .as_str()
        .is_some_and(|uri| uri.starts_with("s3://extract-bucket/integration/extracted/")));
    assert!(value["entries"][0]["stored_object"]["b3"]
        .as_str()
        .is_some_and(|hash| hash.len() == 64));

    let keys = seaweed
        .list_keys(&format!("integration/extracted/{extraction_id}/"))
        .expect("seaweedfs should list extracted objects");
    assert!(
        keys.iter().any(|key| key.ends_with("/alpha.zip") && key.contains(&extraction_id)),
        "alpha.zip should be present in SeaweedFS S3, got {keys:?}"
    );
    assert!(
        keys.iter().any(|key| key.ends_with("/nested/bravo.pdf") && key.contains(&extraction_id)),
        "nested/bravo.pdf should be present in SeaweedFS S3, got {keys:?}"
    );

    assert_postgres_extract_metadata(&postgres, &extraction_id, 2);
}
