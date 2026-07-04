use super::*;

#[tokio::test]
async fn scan_local_path_returns_summary() {
    let archive = sample_zip_archive();
    let response = router()
        .oneshot(post_json(
            "/v1/scan/local-path",
            serde_json::json!({
                "path": archive.path().display().to_string(),
                "include_entries": true,
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    assert!(value["archive"]["name"]
        .as_str()
        .is_some_and(|name| name.starts_with("sample-") && name.ends_with(".zip")));
    assert_eq!(value["total_entries"], 2);
    assert_eq!(value["total_files"], 2);
    assert_eq!(value["total_directories"], 0);
    assert_eq!(value["entry_kinds"][0]["kind"], "file");
    assert_eq!(value["entry_kinds"][0]["count"], 2);

    let labels: Vec<_> = value["types"]
        .as_array()
        .expect("types should be an array")
        .iter()
        .map(|item| item["label"].as_str().unwrap_or_default())
        .collect();
    assert!(labels.contains(&"zip"));
    assert!(labels.contains(&"pdf"));

    let mimes = mime_counts(&value);
    assert_eq!(mimes.get("application/zip"), Some(&1));
    assert_eq!(mimes.get("application/pdf"), Some(&1));
    assert_eq!(value["entries"].as_array().map(Vec::len), Some(2));
}
#[tokio::test]
async fn scan_source_route_accepts_shared_filesystem_source() {
    let archive = sample_zip_archive();
    let response = router()
        .oneshot(post_json(
            "/v1/scan/source",
            serde_json::json!({
                "source": {
                    "kind": "shared_filesystem_path",
                    "path": archive.path().display().to_string()
                },
                "include_entries": true,
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    assert_eq!(value["archive"]["path"], archive.path().display().to_string());
    assert_eq!(value["total_entries"], 2);
    assert_eq!(value["entries"].as_array().map(Vec::len), Some(2));
}
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn scan_source_route_accepts_object_storage_url_source() {
    let object_store = object_storage_fixture_server().await;
    let response = router_with_private_object_sources()
        .oneshot(post_json(
            "/v1/scan/source",
            serde_json::json!({
                "source": {
                    "kind": "object_storage_url",
                    "url": object_store.url.clone()
                },
                "include_entries": true,
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    assert_eq!(value["archive"]["path"], object_store.url);
    assert_eq!(value["archive"]["name"], "remote-sample.zip");
    assert_eq!(value["total_entries"], 2);
    assert_eq!(value["entries"].as_array().map(Vec::len), Some(2));
}
#[tokio::test]
async fn scan_local_path_returns_expected_counts_for_real_archive_fixture() {
    let Some(archive_path) = real_archive_fixture() else {
        eprintln!("skipping real archive service test because data/asr-master.zip is not present");
        return;
    };

    let response = router()
        .oneshot(post_json(
            "/v1/scan/local-path",
            serde_json::json!({
                "path": archive_path.display().to_string(),
                "include_entries": true,
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    assert_real_fixture_summary(&value, &archive_path);
}
#[tokio::test]
async fn scan_source_reports_truncated_large_entry_without_full_hash() {
    let fixture = large_zip_archive();
    let response = router()
        .oneshot(post_json(
            "/v1/scan/source",
            serde_json::json!({
                "source": {
                    "kind": "shared_filesystem_path",
                    "path": fixture.archive.path().display().to_string()
                },
                "include_entries": true,
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    assert_eq!(value["total_entries"], 1);
    assert_eq!(value["total_files"], 1);
    assert_eq!(value["total_directories"], 0);
    assert_eq!(type_counts(&value).get("txt"), Some(&1));
    assert_eq!(mime_counts(&value).get("text/plain"), Some(&1));

    let entry = find_entry(&value, LARGE_ENTRY_NAME);
    assert_large_text_entry(entry, fixture.archive.path(), &fixture.payload, false);
}
