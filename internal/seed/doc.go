// Package seed builds the shared test fleet: a state folder with a fixed
// identity, one connected laptop and one enrolled server (docs/specs/02k-v1-surface-and-cli.md
// "The test seed").
//
// Contract tests, golden CLI outputs and the Playwright smoke all start
// from [Seed] so they share one fleet. [Version] follows the agent wire
// (internal/protoversion): a wire change must rebuild the seed.
package seed
