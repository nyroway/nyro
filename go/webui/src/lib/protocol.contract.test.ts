import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { PROTOCOL_TABLE, resolveProtocol } from "./protocol";

// protocols.json is the single source of protocol identity, shared with the Go
// backend (go/internal/llm/protocol/protocols.json). Neither side reads it
// at runtime: this test and the Go contract_test.go each assert their own table matches it,
// so a change made on one side and forgotten on the other fails a test rather
// than drifting silently — which is exactly how the displayName suffixes and
// the alias set diverged before.
//
// The TS table covers only the selectable protocols (what the UI offers); the
// contract carries the full set including non-selectable ones (e.g.
// openai-embeddings, which has a codec but is not surfaced yet).
interface ContractProtocol {
  id: string;
  displayName: string;
  alias: string;
  defaultBaseUrl: string;
  selectable: boolean;
}
interface Contract {
  protocols: ContractProtocol[];
  rejected: string[];
}

const contractPath = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "..",
  "internal",
  "llm",
  "protocol",
  "protocols.json",
);
const contract = JSON.parse(readFileSync(contractPath, "utf8")) as Contract;
const selectable = contract.protocols.filter((p) => p.selectable);

describe("protocol identity contract (internal/llm/protocol/protocols.json)", () => {
  it("PROTOCOL_TABLE is exactly the selectable subset of the contract", () => {
    expect(PROTOCOL_TABLE).toHaveLength(selectable.length);
    for (const p of selectable) {
      const row = PROTOCOL_TABLE.find((r) => r.id === p.id);
      expect(row, `selectable protocol ${p.id} missing from PROTOCOL_TABLE`).toBeDefined();
      expect(row).toMatchObject({
        id: p.id,
        displayName: p.displayName,
        defaultBaseUrl: p.defaultBaseUrl,
      });
    }
  });

  it("every selectable protocol resolves to itself, via id and its alias", () => {
    for (const p of selectable) {
      expect(resolveProtocol(p.id), `resolveProtocol("${p.id}")`).toBe(p.id);
      if (p.alias) {
        expect(resolveProtocol(p.alias), `resolveProtocol("${p.alias}")`).toBe(p.id);
      }
    }
  });

  it("non-selectable protocols are invisible to the UI", () => {
    for (const p of contract.protocols.filter((row) => !row.selectable)) {
      expect(PROTOCOL_TABLE.find((r) => r.id === p.id)).toBeUndefined();
      expect(resolveProtocol(p.id), `non-selectable ${p.id} should not resolve`).toBeNull();
    }
  });

  it("rejected identifiers do not resolve", () => {
    expect(contract.rejected.length, "contract lists no rejected identifiers").toBeGreaterThan(0);
    for (const id of contract.rejected) {
      expect(resolveProtocol(id), `resolveProtocol("${id}") should be null`).toBeNull();
    }
  });
});
