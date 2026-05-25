use crate::db::models::Model;
use crate::storage::ModelSnapshotStore;

pub struct ModelCache {
    pub models: Vec<Model>,
}

impl ModelCache {
    pub async fn load(store: &dyn ModelSnapshotStore) -> anyhow::Result<Self> {
        let models = store.load_active_snapshot().await?;
        Ok(Self { models })
    }

    pub async fn reload(&mut self, store: &dyn ModelSnapshotStore) -> anyhow::Result<()> {
        *self = Self::load(store).await?;
        Ok(())
    }
}

pub fn match_model<'a>(models: &'a [Model], model: &str) -> Option<&'a Model> {
    models.iter().find(|m| m.virtual_model == model)
}
