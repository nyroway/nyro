use super::*;

pub(super) fn ensure_virtual_model(model: &str) -> anyhow::Result<()> {
    if model.trim().is_empty() {
        anyhow::bail!("virtual_model cannot be empty");
    }
    Ok(())
}

pub(super) fn normalize_model_balance(balance: Option<&str>) -> anyhow::Result<String> {
    let normalized = balance.unwrap_or("weighted").trim().to_ascii_lowercase();
    match normalized.as_str() {
        "weighted" | "priority" => Ok(normalized),
        _ => anyhow::bail!("unsupported model balance: {normalized}"),
    }
}

pub(super) fn normalize_create_model_backends(
    input: &CreateModel,
) -> anyhow::Result<Vec<CreateModelBackend>> {
    if !input.targets.is_empty() {
        return Ok(input.targets.clone());
    }
    if !input.target_provider.trim().is_empty() && !input.target_model.trim().is_empty() {
        return Ok(vec![CreateModelBackend {
            provider_id: input.target_provider.clone(),
            model: input.target_model.clone(),
            weight: Some(100),
            priority: Some(1),
        }]);
    }
    anyhow::bail!("at least one model backend is required")
}

pub(super) fn normalize_update_model_backends(
    current: &Model,
    input: &UpdateModel,
) -> anyhow::Result<Vec<CreateModelBackend>> {
    if let Some(targets) = &input.targets {
        let mapped = targets
            .iter()
            .map(|target| CreateModelBackend {
                provider_id: target.provider_id.clone(),
                model: target.model.clone(),
                weight: target.weight,
                priority: target.priority,
            })
            .collect();
        return Ok(mapped);
    }

    let provider = input
        .target_provider
        .clone()
        .unwrap_or_else(|| current.target_provider.clone());
    let model = input
        .target_model
        .clone()
        .unwrap_or_else(|| current.target_model.clone());
    if provider.trim().is_empty() || model.trim().is_empty() {
        anyhow::bail!("model backend cannot be empty");
    }
    Ok(vec![CreateModelBackend {
        provider_id: provider,
        model,
        weight: Some(100),
        priority: Some(1),
    }])
}

pub(super) fn ensure_model_backends_valid(backends: &[CreateModelBackend]) -> anyhow::Result<()> {
    if backends.is_empty() {
        anyhow::bail!("at least one model backend is required");
    }
    for backend in backends {
        if backend.provider_id.trim().is_empty() {
            anyhow::bail!("backend provider_id cannot be empty");
        }
        if backend.model.trim().is_empty() {
            anyhow::bail!("backend model cannot be empty");
        }
        let weight = backend.weight.unwrap_or(100);
        if weight < 0 {
            anyhow::bail!("backend weight must be >= 0");
        }
        let priority = backend.priority.unwrap_or(1);
        if !(1..=2).contains(&priority) {
            anyhow::bail!("backend priority must be 1 or 2");
        }
    }
    Ok(())
}
