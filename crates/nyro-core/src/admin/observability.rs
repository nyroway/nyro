use super::*;

impl AdminService {
    // ── Logs ──

    pub async fn query_logs(&self, q: LogQuery) -> anyhow::Result<LogPage> {
        let mut q = q;
        q.limit = Some(q.limit.unwrap_or(50).min(500));
        q.offset = Some(q.offset.unwrap_or(0));
        self.gw.storage.logs().query(q).await
    }

    pub async fn get_log(&self, id: &str) -> anyhow::Result<Option<RequestLog>> {
        self.gw.storage.logs().find_by_id(id).await
    }

    pub async fn clear_logs(&self) -> anyhow::Result<u64> {
        self.gw.storage.logs().clear_all().await
    }
    // ── Stats ──

    fn normalize_hours(hours: Option<i32>) -> Option<i32> {
        hours.and_then(|value| (value > 0).then_some(value))
    }

    pub async fn get_stats_overview(&self, hours: Option<i32>) -> anyhow::Result<StatsOverview> {
        self.gw
            .storage
            .logs()
            .stats_overview(Self::normalize_hours(hours).map(i64::from))
            .await
    }

    pub async fn get_stats_hourly(&self, hours: i32) -> anyhow::Result<Vec<StatsHourly>> {
        self.gw
            .storage
            .logs()
            .stats_hourly(i64::from(hours.max(1)))
            .await
    }

    pub async fn get_stats_by_model(&self, hours: Option<i32>) -> anyhow::Result<Vec<ModelStats>> {
        self.gw
            .storage
            .logs()
            .stats_by_model(Self::normalize_hours(hours).map(i64::from))
            .await
    }

    pub async fn get_stats_by_provider(
        &self,
        hours: Option<i32>,
    ) -> anyhow::Result<Vec<ProviderStats>> {
        self.gw
            .storage
            .logs()
            .stats_by_provider(Self::normalize_hours(hours).map(i64::from))
            .await
    }

    pub async fn get_stats_by_api_key(
        &self,
        hours: Option<i32>,
    ) -> anyhow::Result<Vec<ApiKeyStats>> {
        self.gw
            .storage
            .logs()
            .stats_by_api_key(Self::normalize_hours(hours).map(i64::from))
            .await
    }
}
