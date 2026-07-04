use std::env;
use std::net::SocketAddr;

use axum::extract::{DefaultBodyLimit, Multipart};
use axum::http::StatusCode;
use axum::routing::{get, post};
use axum::{Json, Router};

use crate::parser::parse_workbook;
use crate::render::render_response;

pub fn app() -> Router {
    Router::new()
        .route("/health", get(health))
        .route("/v1/parse", post(parse))
        .layer(DefaultBodyLimit::max(max_body_bytes()))
}

pub async fn serve() -> anyhow::Result<()> {
    let addr: SocketAddr = env::var("HTTP_ADDR")
        .unwrap_or_else(|_| "0.0.0.0:8095".to_string())
        .parse()?;
    let listener = tokio::net::TcpListener::bind(addr).await?;
    tracing::info!(%addr, "workbook parser listening");
    axum::serve(listener, app()).await?;
    Ok(())
}

async fn health() -> &'static str {
    "ok"
}

async fn parse(
    mut multipart: Multipart,
) -> Result<Json<crate::model::ParseResponse>, (StatusCode, String)> {
    let mut file_name = String::from("workbook.xlsx");
    let mut data = None;

    while let Some(field) = multipart.next_field().await.map_err(bad_request)? {
        if field.name() != Some("file") {
            continue;
        }
        if let Some(name) = field.file_name() {
            file_name = name.to_string();
        }
        data = Some(field.bytes().await.map_err(bad_request)?.to_vec());
        break;
    }

    let data = data.ok_or_else(|| {
        (
            StatusCode::BAD_REQUEST,
            "missing multipart field 'file'".to_string(),
        )
    })?;
    let workbook = parse_workbook(&data, &file_name).map_err(invalid_workbook)?;
    let response = render_response(workbook).map_err(server_error)?;
    Ok(Json(response))
}

fn max_body_bytes() -> usize {
    env::var("WORKBOOK_MAX_UPLOAD_MB")
        .ok()
        .and_then(|value| value.parse::<usize>().ok())
        .unwrap_or(128)
        * 1024
        * 1024
}

fn bad_request(err: axum::extract::multipart::MultipartError) -> (StatusCode, String) {
    (StatusCode::BAD_REQUEST, err.to_string())
}

fn invalid_workbook(err: anyhow::Error) -> (StatusCode, String) {
    (StatusCode::UNPROCESSABLE_ENTITY, err.to_string())
}

fn server_error(err: anyhow::Error) -> (StatusCode, String) {
    (StatusCode::INTERNAL_SERVER_ERROR, err.to_string())
}
