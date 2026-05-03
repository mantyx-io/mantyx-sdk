---
title: Streaming
description: Get assistant tokens as they arrive over SSE.
sidebar:
  order: 5
---

There are two complementary APIs:

1. **Callback-style** — pass `onAssistantDelta` (or `OnAssistantDelta` in Go) to a regular `runAgent` / `Send` call. The promise/result still resolves with the final text.
2. **Iterator-style** — call `streamAgent` / `Session.Stream` to get an async iterator (or channel in Go) of every event.

## TypeScript

```ts
// Callback
await client.runAgent({
  systemPrompt: "...",
  prompt: "Tell me a story.",
  onAssistantDelta: (delta) => process.stdout.write(delta),
});

// Iterator
for await (const event of client.streamAgent({
  systemPrompt: "...",
  prompt: "Tell me a story.",
})) {
  if (event.type === "assistant_delta") process.stdout.write(event.text);
  if (event.type === "result") process.stdout.write("\n");
}
```

## Python

```python
# Callback
client.run_agent(
    system_prompt="...",
    prompt="Tell me a story.",
    on_assistant_delta=lambda d: print(d, end="", flush=True),
)

# Iterator
for event in client.stream_agent(system_prompt="...", prompt="Tell me a story."):
    if event.type == "assistant_delta":
        print(event.text, end="", flush=True)

# Async iterator
async for event in async_client.stream_agent(system_prompt="...", prompt="..."):
    ...
```

## Go

```go
// Callback
_, _ = client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt:     "...",
    Prompt:           "Tell me a story.",
    OnAssistantDelta: func(s string) { fmt.Print(s) },
})

// Channel
ch, err := client.StreamAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "...",
    Prompt:       "Tell me a story.",
})
if err != nil { log.Fatal(err) }
for ev := range ch {
    if ev.Type == "assistant_delta" {
        if t, ok := ev.Data["text"].(string); ok {
            fmt.Print(t)
        }
    }
}
```

## What events to expect

| Event | When | Payload |
| --- | --- | --- |
| `assistant_delta` | Streaming assistant tokens | `{ text }` |
| `thinking_delta` | Some providers expose chain-of-thought tokens | `{ text }` |
| `assistant_message` | Full assistant turn (text + tool calls) | `{ text, toolCalls }` |
| `tool_call` / `tool_result` | Server-side tool execution | `{ toolUseId, name, ... }` |
| `local_tool_call` | The SDK should run a handler | `{ toolUseId, name, input }` |
| `local_tool_result_in` | Echo of the SDK's result | `{ toolUseId, output }` |
| `result` | Terminal | `{ subtype, text?, error? }` |
| `cancelled` | Terminal (after `cancelRun`) | `{}` |

The full list lives in [Wire protocol](/docs/protocol/) §7. The SDKs all reconnect automatically via `Last-Event-ID` + `?lastSeq=` if the SSE stream drops.
