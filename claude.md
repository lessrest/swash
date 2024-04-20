Here is a markdown reference sheet extracted from the old implementation:

# Chat Completion API Reference

## OpenAI API

### Request
- API path: `/openai/v1/chat/completions`
- Method: POST
- Headers:
  - `Content-Type: application/json`
- Request body:
  ```json
  {
    "model": "model_name",
    "temperature": 0.6,
    "stream": true,
    "max_tokens": 512,
    "messages": [
      {"role": "system", "content": "..."},
      {"role": "user", "content": "..."},
      {"role": "assistant", "content": "..."}
    ]
  }
  ```

### Response
- SSE (Server-Sent Events) stream
- Each event is a JSON object followed by a newline
- Event structure:
  ```json
  {
    "id": "chatcmpl-123", 
    "object": "chat.completion.chunk",
    "created": 1694268190,
    "model": "gpt-3.5-turbo-0125",
    "system_fingerprint": "fp_44709d6fcb",
    "choices": [
      {
        "index": 0,
        "delta": {
          "role": "assistant",
          "content": "Hello"
        },
        "logprobs": null,
        "finish_reason": null
      }
    ]
  }
  ```
- The `delta` object contains the partial response content
- The `finish_reason` indicates if the response is complete (`"stop"`)

## Anthropic API

### Request
- API path: `/anthropic/v1/messages`
- Method: POST
- Headers:
  - `Content-Type: application/json`
- Request body:
  ```json
  {
    "model": "model_name",
    "temperature": 0.6,
    "stream": true,
    "max_tokens": 512,
    "system": "system_message_content",
    "messages": [
      {"role": "user", "content": "..."},
      {"role": "assistant", "content": "..."}
    ]
  }
  ```

### Response
- SSE (Server-Sent Events) stream
- Each event is a JSON object followed by a newline
- Event types:
  - `message_start`: Indicates the start of a new message
  - `content_block_start`: Indicates the start of a new content block
  - `content_block_delta`: Contains a partial response content
  - `content_block_stop`: Indicates the end of a content block
  - `message_delta`: Contains metadata about the message
  - `message_stop`: Indicates the end of the message

Here's a basic version of the chat completion code using the new framework paradigm:

```javascript
import {
  Channel,
  Operation,
  createChannel,
  each,
  main,
  spawn,
  suspend,
} from "effection"

function* chatCompletion(
  apiPath: string,
  requestBody: object,
  messagesChannel: Channel<string, void>,
): Operation<void> {
  const response = yield* fetch(apiPath, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(requestBody),
  })

  if (!response.ok) {
    throw new Error(`HTTP error! status: ${response.status}`)
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder("utf-8")
  let buffer = ""

  while (true) {
    const { done, value } = yield* reader.read()

    if (done) {
      break
    }

    buffer += decoder.decode(value)

    let position
    while ((position = buffer.indexOf("\n")) !== -1) {
      const line = buffer.slice(0, position).trim()
      buffer = buffer.slice(position + 1)

      if (line === "") continue
      if (line.startsWith("event:")) continue

      const message = line.replace(/^data: /, "")
      if (message === "[DONE]") {
        yield* messagesChannel.close()
        break
      }

      yield* messagesChannel.send(message)
    }
  }
}

function* app() {
  const messagesChannel = createChannel<string>()

  yield* spawn(function* () {
    const apiPath = "/openai/v1/chat/completions"
    const requestBody = {
      model: "gpt-3.5-turbo",
      temperature: 0.6,
      stream: true,
      max_tokens: 512,
      messages: [
        { role: "system", content: "You are a helpful assistant." },
        { role: "user", content: "Hello!" },
      ],
    }

    yield* chatCompletion(apiPath, requestBody, messagesChannel)
  })

  for (const message of yield* each(messagesChannel)) {
    console.log("Received message:", message)
    yield* each.next()
  }
}

main(app)
```

This code sets up a `chatCompletion` operation that sends a request to the specified API path with the provided request body. It then reads the SSE stream response, decodes the messages, and sends them to the `messagesChannel`.

The `app` function spawns the `chatCompletion` operation and listens for messages on the `messagesChannel`, logging each message as it is received.

Note that this is a basic example and may need to be adapted to handle the specific event structures and error handling for each API provider.
