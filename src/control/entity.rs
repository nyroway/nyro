//! HTTP adapters for typed draft edits; the control crate owns their semantics.
use super::*;
use axum::extract::{Path, rejection::PathRejection};
use nyro_control::entity::{EntityChange, EntityKind, EntityValue};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Create {
    expected_revision: u64,
    id: String,
    value: Value,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Replace {
    expected_revision: u64,
    value: Value,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Delete {
    expected_revision: u64,
}

pub(super) fn routes() -> Router<Arc<Control>> {
    Router::new()
        .route("/admin/:collection", get(list).post(create))
        .route(
            "/admin/:collection/:id",
            get(read).put(replace).delete(delete),
        )
}

fn kind(collection: &str) -> Option<EntityKind> {
    match collection {
        "providers" => Some(EntityKind::Provider),
        "models" => Some(EntityKind::Model),
        "api-keys" => Some(EntityKind::ApiKey),
        _ => None,
    }
}

async fn list(
    State(control): State<Arc<Control>>,
    path: Result<Path<String>, PathRejection>,
) -> Response {
    let Ok(Path(collection)) = path else {
        return failure(StatusCode::BAD_REQUEST, "invalid_path");
    };
    let Some(kind) = kind(&collection) else {
        return failure(StatusCode::NOT_FOUND, "not_found");
    };
    control
        .execute(Command::ReadEntities { kind, id: None })
        .await
}
async fn read(
    State(control): State<Arc<Control>>,
    path: Result<Path<(String, String)>, PathRejection>,
) -> Response {
    let Ok(Path((collection, id))) = path else {
        return failure(StatusCode::BAD_REQUEST, "invalid_path");
    };
    let Some(kind) = kind(&collection) else {
        return failure(StatusCode::NOT_FOUND, "not_found");
    };
    control
        .execute(Command::ReadEntities { kind, id: Some(id) })
        .await
}
async fn create(
    State(control): State<Arc<Control>>,
    path: Result<Path<String>, PathRejection>,
    body: Result<Json<Create>, JsonRejection>,
) -> Response {
    let Ok(Path(collection)) = path else {
        return failure(StatusCode::BAD_REQUEST, "invalid_path");
    };
    let Some(kind) = kind(&collection) else {
        return failure(StatusCode::NOT_FOUND, "not_found");
    };
    let input = match body {
        Ok(Json(input)) => input,
        Err(error) => return failure(error.status(), "invalid_request"),
    };
    let value = match EntityValue::from_json(kind, input.value) {
        Ok(value) => value,
        Err(error) => return storage_error(error),
    };
    control
        .execute(Command::EditEntity {
            expected_revision: input.expected_revision,
            change: EntityChange::Create {
                id: input.id,
                value,
            },
        })
        .await
}
async fn replace(
    State(control): State<Arc<Control>>,
    path: Result<Path<(String, String)>, PathRejection>,
    body: Result<Json<Replace>, JsonRejection>,
) -> Response {
    let Ok(Path((collection, id))) = path else {
        return failure(StatusCode::BAD_REQUEST, "invalid_path");
    };
    let Some(kind) = kind(&collection) else {
        return failure(StatusCode::NOT_FOUND, "not_found");
    };
    let input = match body {
        Ok(Json(input)) => input,
        Err(error) => return failure(error.status(), "invalid_request"),
    };
    let value = match EntityValue::from_json(kind, input.value) {
        Ok(value) => value,
        Err(error) => return storage_error(error),
    };
    control
        .execute(Command::EditEntity {
            expected_revision: input.expected_revision,
            change: EntityChange::Replace { id, value },
        })
        .await
}
async fn delete(
    State(control): State<Arc<Control>>,
    path: Result<Path<(String, String)>, PathRejection>,
    body: Result<Json<Delete>, JsonRejection>,
) -> Response {
    let Ok(Path((collection, id))) = path else {
        return failure(StatusCode::BAD_REQUEST, "invalid_path");
    };
    let Some(kind) = kind(&collection) else {
        return failure(StatusCode::NOT_FOUND, "not_found");
    };
    let input = match body {
        Ok(Json(input)) => input,
        Err(error) => return failure(error.status(), "invalid_request"),
    };
    control
        .execute(Command::EditEntity {
            expected_revision: input.expected_revision,
            change: EntityChange::Delete { kind, id },
        })
        .await
}
