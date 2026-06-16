# Lununda Agent

Multi-user AI Agent runtime. Creates, manages, and runs AI agents with personality, memory, skills, and tools. A single binary serves the gateway HTTP API, web dashboard, IM channel bridges, and the agent runtime.

## Language

**Regex Hook**:
A per-agent rule that intercepts inbound messages matching a regex pattern, executes a CLI command (stdin = message text), and returns the CLI's stdout as the reply — bypassing the LLM entirely.
_Avoid_: regex rule, command rule, CLI route

**Sort Order**:
Integer field on Regex Hook controlling evaluation order. Lower values evaluate first. Evaluated sequentially; a hook with `continue_on_match=false` stops the chain.
_Avoid_: priority, rank, index

**Continue on Match**:
When true, after a hook matches and its CLI executes, evaluation proceeds to the next hook. When false, the chain stops and the result is returned immediately.
_Avoid_: chain, pipeline mode

**Show Error**:
When true and the CLI exits non-zero, an error message is sent to the user. When false, failures are silently logged.
_Avoid_: error display, error visible

## Relationships

- An **Agent** owns zero or more **Regex Hooks**
- **Regex Hooks** are evaluated in **Sort Order** within an Agent's scope
- A **Regex Hook** executes a CLI command and produces a reply text
- Multiple matching **Regex Hooks** produce a combined reply separated by `---hook_name---` headers

## Example dialogue

> **Dev:** "If a user sends '翻译 hello' and there's a Regex Hook with pattern `^翻译(.+)$`, what happens?"
> **Domain expert:** "The hook matches, `hello` (the full message text) is piped to the CLI via stdin, the CLI's stdout is sent back as the reply. The LLM never sees the message."

> **Dev:** "What if two hooks match and both have continue_on_match=true?"
> **Domain expert:** "Both CLIs run. The final reply shows `---HookA---\n\nresultA\n\n---HookB---\n\nresultB` so the user knows which output came from which hook."

## Flagged ambiguities

- "hook" initially referred to both the regex matching mechanism and the generic agent hook system — resolved: this feature uses **Regex Hook** exclusively; the existing agent hook system (`BeforeSystemPrompt`, `PostTurn`, etc.) is a separate concept.
