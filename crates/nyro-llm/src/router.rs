//! One weighted choice; request preparation decides which backends are eligible.
use rand::{
    Rng,
    distributions::{Distribution, WeightedIndex},
};

pub(crate) fn choose(weights: impl IntoIterator<Item = u32>) -> Option<usize> {
    choose_with(weights, &mut rand::thread_rng())
}

fn choose_with(weights: impl IntoIterator<Item = u32>, rng: &mut impl Rng) -> Option<usize> {
    WeightedIndex::new(weights.into_iter().map(u64::from))
        .ok()
        .map(|distribution| distribution.sample(rng))
}

#[cfg(test)]
mod tests {
    use super::*;
    use rand::{SeedableRng, rngs::StdRng};

    #[test]
    fn excludes_disabled_backends_and_handles_empty_candidates() {
        let mut rng = StdRng::seed_from_u64(42);
        assert_eq!(choose_with([], &mut rng), None);
        assert_eq!(choose_with([0, 0], &mut rng), None);
        for _ in 0..100 {
            assert_eq!(choose_with([0, 100, 0], &mut rng), Some(1));
        }
    }

    #[test]
    fn weights_change_selection_probability_without_u32_sum_overflow() {
        let mut rng = StdRng::seed_from_u64(42);
        let mut selected = [0; 2];
        for _ in 0..10_000 {
            selected[choose_with([1, 9], &mut rng).unwrap()] += 1;
        }
        assert!((8500..9500).contains(&selected[1]), "{selected:?}");
        let mut seen = [false; 2];
        for _ in 0..100 {
            seen[choose_with([u32::MAX, u32::MAX], &mut rng).unwrap()] = true;
        }
        assert_eq!(seen, [true, true]);
    }
}
