use std::{
    collections::{BTreeMap, BTreeSet},
    error::Error,
    fmt,
};

use async_trait::async_trait;
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

pub type ComponentId = String;

/// A phase-local deadline and cancellation signal, not a business request context.
#[derive(Clone, Debug)]
pub struct Context {
    pub deadline: Instant,
    pub cancellation: CancellationToken,
}

/// Owned, inactive resources only. All methods must be non-blocking and must not panic.
/// Start and wait futures may be dropped on cancellation or deadline expiry.
#[async_trait]
pub trait Lifecycle: Send + 'static {
    async fn start(&mut self, context: &Context) -> Result<(), String>;
    /// Idempotent; must initiate cleanup even after partial or absent startup.
    fn begin_stop(&mut self);
    async fn wait_stopped(&mut self, context: &Context) -> Result<(), String>;
}

pub struct Component {
    pub id: ComponentId,
    pub after: Vec<ComponentId>,
    pub lifecycle: Box<dyn Lifecycle>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Phase {
    Validate,
    Start,
    Stop,
    Drain,
    Host,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Issue {
    pub phase: Phase,
    pub component: Option<ComponentId>,
    pub message: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct KernelError {
    pub issues: Vec<Issue>,
}

impl fmt::Display for KernelError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        for (index, issue) in self.issues.iter().enumerate() {
            if index != 0 {
                write!(f, "; ")?;
            }
            write!(
                f,
                "{:?} {:?}: {}",
                issue.phase, issue.component, issue.message
            )?;
        }
        Ok(())
    }
}

impl Error for KernelError {}

impl Issue {
    pub(crate) fn new(phase: Phase, component: Option<&str>, message: impl Into<String>) -> Self {
        Self {
            phase,
            component: component.map(str::to_owned),
            message: message.into(),
        }
    }
}

impl From<Issue> for KernelError {
    fn from(issue: Issue) -> Self {
        Self {
            issues: vec![issue],
        }
    }
}

struct Entry {
    component: Component,
    stopping: bool,
    cancellation: CancellationToken,
}

impl Entry {
    fn begin_stop(&mut self) {
        if !self.stopping {
            self.stopping = true;
            self.cancellation.cancel();
            self.component.lifecycle.begin_stop();
        }
    }
}

pub(crate) struct Resources {
    entries: Vec<Entry>,
    pub cancellation: CancellationToken,
}

impl Resources {
    pub fn new(components: Vec<Component>) -> Self {
        Self {
            entries: components
                .into_iter()
                .map(|component| Entry {
                    component,
                    stopping: false,
                    // Independent of the generation-use signal: dependencies must remain
                    // available until their dependents have finished stopping.
                    cancellation: CancellationToken::new(),
                })
                .collect(),
            cancellation: CancellationToken::new(),
        }
    }

    pub fn sort(&mut self) -> Result<(), KernelError> {
        let components: Vec<_> = self.entries.iter().map(|entry| &entry.component).collect();
        let indices = order_refs(&components)?;
        let mut ranks = vec![0; indices.len()];
        for (rank, index) in indices.into_iter().enumerate() {
            ranks[index] = rank;
        }
        let mut indexed: Vec<_> = self.entries.drain(..).enumerate().collect();
        indexed.sort_by_key(|(index, _)| ranks[*index]);
        self.entries = indexed.into_iter().map(|(_, entry)| entry).collect();
        Ok(())
    }

    pub async fn start(
        &mut self,
        context: &Context,
        closing: &CancellationToken,
    ) -> Result<(), KernelError> {
        for entry in &mut self.entries {
            let start_context = Context {
                deadline: context.deadline,
                cancellation: entry.cancellation.clone(),
            };
            let component = &mut entry.component;
            let result = tokio::select! {
                biased;
                _ = closing.cancelled() => Err("Host is closing".into()),
                _ = context.cancellation.cancelled() => Err("Activation cancelled".into()),
                _ = tokio::time::sleep_until(context.deadline) => Err("Startup deadline exceeded".into()),
                result = component.lifecycle.start(&start_context) => result,
            };
            if let Err(message) = result {
                return Err(Issue::new(Phase::Start, Some(&component.id), message).into());
            }
        }
        Ok(())
    }

    pub async fn stop(&mut self, deadline: Instant, expired: &CancellationToken) -> Vec<Issue> {
        self.cancellation.cancel();
        let context = Context {
            deadline,
            cancellation: expired.child_token(),
        };
        let mut issues = Vec::new();
        for entry in self.entries.iter_mut().rev() {
            // Always notify every owner, including allocations whose start never ran.
            entry.begin_stop();
            let result = tokio::select! {
                biased;
                _ = context.cancellation.cancelled() => Err("Cleanup deadline exceeded".into()),
                _ = tokio::time::sleep_until(deadline) => Err("Cleanup deadline exceeded".into()),
                result = entry.component.lifecycle.wait_stopped(&context) => result,
            };
            if let Err(message) = result {
                issues.push(Issue::new(Phase::Stop, Some(&entry.component.id), message));
            }
        }
        context.cancellation.cancel();
        issues
    }
}

impl Drop for Resources {
    fn drop(&mut self) {
        self.cancellation.cancel();
        for entry in self.entries.iter_mut().rev() {
            entry.begin_stop();
        }
    }
}

#[cfg(test)]
fn order(components: &[Component]) -> Result<Vec<usize>, KernelError> {
    order_refs(&components.iter().collect::<Vec<_>>())
}

fn order_refs(components: &[&Component]) -> Result<Vec<usize>, KernelError> {
    let invalid = |message: String| KernelError {
        issues: vec![Issue {
            phase: Phase::Validate,
            component: None,
            message,
        }],
    };
    let mut ids = BTreeMap::new();
    for (index, component) in components.iter().enumerate() {
        if component.id.trim().is_empty() {
            return Err(invalid("Component ID must not be empty".into()));
        }
        if ids.insert(component.id.as_str(), index).is_some() {
            return Err(invalid(format!("Duplicate component: {}", component.id)));
        }
    }
    let mut pending = vec![0; components.len()];
    let mut dependents = vec![BTreeSet::new(); components.len()];
    for (index, component) in components.iter().enumerate() {
        for dependency in &component.after {
            let Some(&parent) = ids.get(dependency.as_str()) else {
                return Err(invalid(format!(
                    "Missing dependency {dependency} for {}",
                    component.id
                )));
            };
            if dependents[parent].insert(index) {
                pending[index] += 1;
            }
        }
    }
    let mut ready: BTreeSet<_> = ids
        .iter()
        .filter(|(_, index)| pending[**index] == 0)
        .map(|(&id, &index)| (id, index))
        .collect();
    let mut ordered = Vec::with_capacity(components.len());
    while let Some((_, index)) = ready.pop_first() {
        ordered.push(index);
        for &child in &dependents[index] {
            pending[child] -= 1;
            if pending[child] == 0 {
                ready.insert((components[child].id.as_str(), child));
            }
        }
    }
    if ordered.len() != components.len() {
        return Err(invalid("Component dependency cycle".into()));
    }
    Ok(ordered)
}

#[cfg(test)]
mod tests {
    use super::*;

    struct Inactive;

    #[async_trait]
    impl Lifecycle for Inactive {
        async fn start(&mut self, _: &Context) -> Result<(), String> {
            Ok(())
        }
        fn begin_stop(&mut self) {}
        async fn wait_stopped(&mut self, _: &Context) -> Result<(), String> {
            Ok(())
        }
    }

    fn component(id: &str, after: &[&str]) -> Component {
        Component {
            id: id.into(),
            after: after.iter().map(|id| (*id).into()).collect(),
            lifecycle: Box::new(Inactive),
        }
    }

    #[test]
    fn orders_dependencies_then_ready_ids() {
        let components = vec![
            component("z", &["a"]),
            component("b", &[]),
            component("a", &[]),
        ];
        assert_eq!(order(&components).unwrap(), [2, 1, 0]);
    }

    #[test]
    fn rejects_empty_duplicate_missing_and_cyclic_ids() {
        for components in [
            vec![component("", &[])],
            vec![component("  ", &[])],
            vec![component("a", &[]), component("a", &[])],
            vec![component("a", &["missing"])],
            vec![component("a", &["a"])],
            vec![component("a", &["b"]), component("b", &["a"])],
        ] {
            assert!(order(&components).is_err());
        }
    }
}
