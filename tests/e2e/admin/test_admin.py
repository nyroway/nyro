from __future__ import annotations

import time
from typing import Any

import pytest

from tests.common.helpers import http_request


def _create_provider(env: dict[str, str], name: str) -> str:
    status, resp = http_request(
        "POST",
        f"{env['admin']}/api/v1/providers",
        payload={
            "name": name,
            "protocol": "openai",
            "base_url": env["mock"],
            "api_key": "dummy-key",
        },
        headers=env["auth"],
    )
    assert status == 200, f"create provider failed: {status} {resp}"
    return resp["data"]["id"]


def _create_model(env: dict[str, str], provider_id: str, name: str, vmodel: str) -> str:
    status, resp = http_request(
        "POST",
        f"{env['admin']}/api/v1/models",
        payload={
            "name": name,
            "virtual_model": vmodel,
            "target_provider": provider_id,
            "target_model": "gpt-4o-mini",
            "access_control": True,
        },
        headers=env["auth"],
    )
    assert status == 200, f"create model failed: {status} {resp}"
    return resp["data"]["id"]


def _create_api_key(env: dict[str, str], model_id: str, name: str) -> dict[str, Any]:
    status, resp = http_request(
        "POST",
        f"{env['admin']}/api/v1/api-keys",
        payload={"name": name, "model_ids": [model_id]},
        headers=env["auth"],
    )
    assert status == 200, f"create api-key failed: {status} {resp}"
    return resp["data"]


@pytest.mark.e2e
@pytest.mark.admin
def test_admin_anon_returns_401(admin_env: dict[str, str]) -> None:
    status, _ = http_request("GET", f"{admin_env['admin']}/api/v1/status")
    assert status == 401


@pytest.mark.e2e
@pytest.mark.admin
def test_provider_crud(admin_env: dict[str, str]) -> None:
    provider_id = _create_provider(admin_env, "test-provider")

    status, resp = http_request("GET", f"{admin_env['admin']}/api/v1/providers", headers=admin_env["auth"])
    assert status == 200
    ids = [item["id"] for item in resp["data"]]
    assert provider_id in ids

    status, resp = http_request(
        "GET",
        f"{admin_env['admin']}/api/v1/providers/{provider_id}",
        headers=admin_env["auth"],
    )
    assert status == 200
    assert resp["data"]["id"] == provider_id


@pytest.mark.e2e
@pytest.mark.admin
def test_model_crud(admin_env: dict[str, str]) -> None:
    provider_id = _create_provider(admin_env, "test-provider-model")
    model_id = _create_model(admin_env, provider_id, "test-model", "test-vmodel")

    status, resp = http_request("GET", f"{admin_env['admin']}/api/v1/models", headers=admin_env["auth"])
    assert status == 200
    ids = [item["id"] for item in resp.get("data", [])]
    assert model_id in ids


@pytest.mark.e2e
@pytest.mark.admin
def test_api_key_crud(admin_env: dict[str, str]) -> None:
    provider_id = _create_provider(admin_env, "test-provider-key")
    model_id = _create_model(admin_env, provider_id, "test-model-key", "test-vmodel-key")
    api_key = _create_api_key(admin_env, model_id, "test-key")
    assert api_key.get("key"), f"missing api key material: {api_key}"


@pytest.mark.e2e
@pytest.mark.admin
def test_access_control_rejects_anonymous(admin_env: dict[str, str]) -> None:
    provider_id = _create_provider(admin_env, "test-provider-access")
    _create_model(admin_env, provider_id, "test-model-access", "test-model-access")

    status, _ = http_request(
        "POST",
        f"{admin_env['proxy']}/v1/chat/completions",
        payload={"model": "test-model-access", "messages": [{"role": "user", "content": "hi"}]},
    )
    assert status == 401


@pytest.mark.e2e
@pytest.mark.admin
def test_export_config_counts(admin_env: dict[str, str]) -> None:
    provider_id = _create_provider(admin_env, "test-provider-export")
    _create_model(admin_env, provider_id, "test-model-export", "test-vmodel-export")

    status, resp = http_request(
        "GET",
        f"{admin_env['admin']}/api/v1/config/export",
        headers=admin_env["auth"],
    )
    assert status == 200
    data = resp.get("data", {})
    assert len(data.get("providers", [])) >= 1
    assert len(data.get("models", [])) >= 1


@pytest.mark.e2e
@pytest.mark.admin
def test_proxy_request_creates_log(admin_env: dict[str, str]) -> None:
    provider_id = _create_provider(admin_env, "test-provider-log")
    model_id = _create_model(admin_env, provider_id, "test-model-log", "test-model-log")
    api_key = _create_api_key(admin_env, model_id, "test-key-log")

    status, resp = http_request(
        "POST",
        f"{admin_env['proxy']}/v1/chat/completions",
        payload={
            "model": "test-model-log",
            "messages": [{"role": "user", "content": "log-trigger"}],
        },
        headers={"authorization": f"Bearer {api_key['key']}"},
    )
    assert status == 200, f"proxy request failed: {status} {resp}"

    deadline = time.time() + 10.0
    total = 0
    while time.time() < deadline:
        status, logs_resp = http_request(
            "GET",
            f"{admin_env['admin']}/api/v1/logs?limit=20&offset=0",
            headers=admin_env["auth"],
        )
        if status == 200:
            total = int(logs_resp.get("data", {}).get("total", 0))
            if total >= 1:
                break
        time.sleep(0.3)
    assert total >= 1


@pytest.mark.e2e
@pytest.mark.admin
def test_stats_overview_incremented(admin_env: dict[str, str]) -> None:
    provider_id = _create_provider(admin_env, "test-provider-stats")
    model_id = _create_model(admin_env, provider_id, "test-model-stats", "test-model-stats")
    api_key = _create_api_key(admin_env, model_id, "test-key-stats")

    status, _ = http_request(
        "POST",
        f"{admin_env['proxy']}/v1/chat/completions",
        payload={
            "model": "test-model-stats",
            "messages": [{"role": "user", "content": "stats-trigger"}],
        },
        headers={"authorization": f"Bearer {api_key['key']}"},
    )
    assert status == 200

    status, resp = http_request(
        "GET",
        f"{admin_env['admin']}/api/v1/stats/overview",
        headers=admin_env["auth"],
    )
    assert status == 200
    data = resp.get("data", {})
    assert data.get("total_requests", 0) >= 1
