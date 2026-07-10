# Settings retry-status input design

## Scope

Adjust the Go WebUI Settings data-plane section only. The persisted setting
remains `proxy.retry_on_status` as a comma-separated list, so no Admin API,
config-sync, or schema change is required.

## Retry status-code control

Replace the comma-separated text field with a tag input.

- A user types one status code and presses Enter to add it.
- Pasting a comma- or newline-separated list adds each valid value.
- Each selected code appears as a removable chip.
- Codes must be whole integers in the inclusive range 400–599. Invalid input
  stays out of the selected values and receives an inline error.
- Existing persisted comma-separated values initialise the chips. Saving
  serialises the chips back to the same comma-separated representation.
- Duplicate codes are ignored while preserving the order of first entry.

## Data-plane layout

Use one responsive two-column grid for the four data-plane cards:

1. Forwarding parameters | Logs
2. Metrics | Traces

At small widths the grid remains a single column. The forwarding card is no
longer full width, eliminating the empty area beneath the telemetry cards.

## Testing

- Unit-test the parsing/validation helper for valid values, out-of-range values,
  pasted delimiters, and duplicates.
- Extend the existing Settings source-layout test to assert the shared two-column
  grid and tag-input semantics.
- Run the focused WebUI tests, lint, and production build.
