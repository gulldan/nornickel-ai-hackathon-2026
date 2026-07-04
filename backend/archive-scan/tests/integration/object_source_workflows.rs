#![cfg(feature = "service")]

use crate::support::{
    object_storage_fixture_server, read_json, wait_for_job_result, ServiceProcess,
};
use reqwest::StatusCode;

#[test]
fn service_e2e_scans_object_storage_source_via_real_server() {
    let object_store = object_storage_fixture_server();
    let service = ServiceProcess::spawn_with_env(&[(
        "ARCHIVE_SCAN_OBJECT_SOURCE_ALLOW_PRIVATE_NETWORKS".to_owned(),
        "1".to_owned(),
    )]);
    let object_store_url = object_store.url.clone();
    let response = service
        .client()
        .post(service.url("/v1/scan/source"))
        .header("content-type", "application/json")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "source": {
                    "kind": "object_storage_url",
                    "url": object_store_url
                },
                "fast_only": true,
                "include_entries": true
            }))
            .expect("request JSON should serialize"),
        )
        .send()
        .expect("service should accept sync object-storage scan");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response);
    assert_eq!(value["archive"]["path"], object_store.url);
    assert_eq!(value["archive"]["name"], "remote-sample.zip");
    assert_eq!(value["total_entries"], 2);
    assert_eq!(value["total_files"], 2);
    assert_eq!(value["entries"].as_array().map(Vec::len), Some(2));
}
#[test]
fn service_e2e_runs_async_object_storage_job_via_real_server() {
    let object_store = object_storage_fixture_server();
    let service = ServiceProcess::spawn_with_env(&[(
        "ARCHIVE_SCAN_OBJECT_SOURCE_ALLOW_PRIVATE_NETWORKS".to_owned(),
        "1".to_owned(),
    )]);
    let object_store_url = object_store.url.clone();
    let create = service
        .client()
        .post(service.url("/v1/jobs"))
        .header("content-type", "application/json")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "source": {
                    "kind": "object_storage_url",
                    "url": object_store_url
                },
                "fast_only": true
            }))
            .expect("request JSON should serialize"),
        )
        .send()
        .expect("service should create async object-storage job");

    assert_eq!(create.status(), StatusCode::CREATED);

    let create_value = read_json(create);
    let job_id = create_value["job_id"].as_str().expect("job_id should be present");

    let status = service
        .client()
        .get(service.url(&format!("/v1/jobs/{job_id}")))
        .send()
        .expect("service should expose job status");
    assert_eq!(status.status(), StatusCode::OK);

    let status_value = read_json(status);
    assert_eq!(status_value["request"]["source"]["kind"], "object_storage_url");
    assert_eq!(status_value["request"]["source"]["url"], object_store.url);

    let result = wait_for_job_result(&service, job_id);
    assert_eq!(result["archive"]["path"], object_store.url);
    assert_eq!(result["archive"]["name"], "remote-sample.zip");
    assert_eq!(result["total_entries"], 2);
    assert_eq!(result["total_files"], 2);
}
