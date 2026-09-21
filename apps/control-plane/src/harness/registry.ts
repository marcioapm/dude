/**
 * Harness registry — plan §45 capability negotiation.
 *
 * A workflow or agent definition should be able to require *capabilities*
 * rather than naming a concrete harness, so policy can pick a compatible one
 * and the factory can later route on observed cost and quality.
 */

import type { AgentHarness, CapabilityRequirement } from "@dude/domain";
import { satisfiesCapabilities } from "@dude/domain";

export class HarnessRegistry {
  readonly #harnesses = new Map<string, AgentHarness>();

  register(harness: AgentHarness): this {
    this.#harnesses.set(harness.name, harness);
    return this;
  }

  get(name: string): AgentHarness | undefined {
    return this.#harnesses.get(name);
  }

  /** Harness by name, raising a clear error rather than failing later. */
  require(name: string): AgentHarness {
    const harness = this.#harnesses.get(name);
    if (!harness) {
      const known = [...this.#harnesses.keys()].join(", ") || "none registered";
      throw new Error(`unknown harness "${name}" (known: ${known})`);
    }
    return harness;
  }

  /** Every harness satisfying the required capabilities. */
  select(required: readonly CapabilityRequirement[]): AgentHarness[] {
    return [...this.#harnesses.values()].filter((h) => satisfiesCapabilities(h, required));
  }

  names(): string[] {
    return [...this.#harnesses.keys()];
  }
}

export const harnessRegistry = new HarnessRegistry();
