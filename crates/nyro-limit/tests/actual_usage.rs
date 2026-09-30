use nyro_limit::token::{Receipt, Registry, Rule};
use std::time::Duration;
fn rule(limit: u64) -> Rule {
    Rule {
        limit,
        window: Duration::from_secs(60),
    }
}
#[test]
fn actual_only_settlement_and_multiple_scopes_are_atomic() {
    let r = Registry::default();
    let model = r.bind("model/uid", vec![rule(2)], vec![rule(10)]).unwrap();
    let caller = r
        .bind("consumer/uid/llm", vec![rule(1)], vec![rule(5)])
        .unwrap();
    r.admit(&[&model, &caller]).unwrap();
    // No reservation: unknown usage is zero, repeated cumulative reports count once.
    drop(Receipt::new(vec![model.clone(), caller.clone()]));
    let mut receipt = Receipt::new(vec![model.clone(), caller.clone()]);
    receipt.observe(3).unwrap();
    receipt.observe(3).unwrap();
    assert!(receipt.observe(2).is_err());
    drop(receipt);
    assert!(r.admit(&[&model, &caller]).is_err());
    // Rejection on caller did not consume the second model request.
    r.admit(&[&model]).unwrap();
    assert!(r.admit(&[&model]).is_err());
    let token_only = r.bind("consumer/uid/llm", vec![], vec![rule(3)]).unwrap();
    assert!(r.admit(&[&token_only]).is_err());
}
#[test]
fn threshold_changes_and_remove_readd_preserve_history_but_failed_bind_does_not_change_policy() {
    let r = Registry::default();
    let first = r.bind("uid", vec![rule(1)], vec![]).unwrap();
    r.admit(&[&first]).unwrap();
    let candidate = r.bind("uid", vec![rule(2)], vec![]).unwrap();
    drop(candidate);
    assert!(r.admit(&[&first]).is_err());
    drop(first);
    let next = r.bind("uid", vec![rule(2)], vec![]).unwrap();
    r.admit(&[&next]).unwrap();
    assert!(r.admit(&[&next]).is_err());
}

#[test]
fn newly_added_window_starts_empty_and_inflight_receipt_keeps_its_original_windows() {
    let registry = Registry::default();
    let minute = registry.bind("uid", vec![], vec![rule(5)]).unwrap();
    let mut receipt = Receipt::new(vec![minute.clone()]);
    receipt.observe(5).unwrap();
    let hour = registry
        .bind(
            "uid",
            vec![],
            vec![Rule {
                limit: 1,
                window: Duration::from_secs(3600),
            }],
        )
        .unwrap();
    drop(receipt);
    assert!(registry.admit(&[&minute]).is_err());
    registry.admit(&[&hour]).unwrap();
}
