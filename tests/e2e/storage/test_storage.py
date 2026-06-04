from __future__ import annotations

from typing import Callable

import pytest


@pytest.mark.e2e
@pytest.mark.storage
@pytest.mark.parametrize("backend", ["sqlite", "postgres", "mysql"], ids=["sqlite", "postgres", "mysql"])
def test_storage_backend_equivalence(storage_runtime: dict[str, object], backend: str) -> None:
    pg_url = storage_runtime["pg_url"]
    mysql_url = storage_runtime["mysql_url"]

    if backend == "postgres" and not pg_url:
        pytest.skip("postgres backend requires DB_URL or DATABASE_URL")
    if backend == "mysql" and not mysql_url:
        pytest.skip("mysql backend requires MYSQL_URL")

    run_harness: Callable[..., str] = storage_runtime["run_harness"]  # type: ignore[assignment]
    output = run_harness(
        backend,
        upstream_port=storage_runtime["upstream_port"],
        work_dir=storage_runtime["work_dir"],
        pg_url=pg_url,
        mysql_url=mysql_url,
    )

    assert f"backend={backend}" in output
    assert "logs_total=" in output
    assert "stats_total_requests=" in output
    assert "proxy_status_ok=200" in output
    assert "proxy_status_no_key=401" in output
