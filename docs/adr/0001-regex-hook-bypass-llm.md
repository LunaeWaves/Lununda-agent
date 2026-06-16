# Regex Hook: Agent-level message interception bypassing LLM

Agent-level Regex Hooks intercept inbound messages matching a regex pattern, execute a CLI command, and return the output directly to the user — bypassing the LLM entirely. Chosen over gateway-level interception so each agent can maintain its own independent rule set, consistent with Lununda Agent's per-agent scoping model. CLI output follows the same markdown format as LLM replies (including `![](image)` refs) so the downstream media pipeline works uniformly. Rules are stored in a dedicated DB table `agent_regex_hooks` with sort_order, continue_on_match, and error handling controls. Evaluated in `HandleMessage` and `HandleMessageStream` entry points before the ReAct loop begins.

## Considered Options

- Gateway-level interception (global rules before agent routing) — rejected because agents are the per-user boundary in Lununda Agent; mixing global regex rules with agent scoping would complicate multi-tenant setups.
- Agent config file (REGEX.md in agent_files) — rejected because regex rules are structured data (pattern + CLI command + flags), not prose; a DB table enables CRUD API + Dashboard UI naturally.
- Scope system (configs table namespace) — rejected for the same structured-data reason; key-value scope entries are awkward for multi-field ordered rules.

## Consequences

- Each agent independently controls its interception rules — no cross-agent conflicts.
- The handler in `taskqueue` handler and `HandleMessageStream` entry both call the same `matchRegexHooks` method, keeping interception consistent across all channels.
- CLI stdout must conform to LLM reply conventions (markdown with optional `![alt](src)` image refs) for downstream media pipeline compatibility.
