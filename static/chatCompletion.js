import {
  useState,
  useEffect,
  useCallback,
  useReducer,
  useMemo,
} from "preact/hooks"

// example of the chat chunks send by SSE
// {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-3.5-turbo-0125", "system_fingerprint": "fp_44709d6fcb", "choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}

// {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-3.5-turbo-0125", "system_fingerprint": "fp_44709d6fcb", "choices":[{"index":0,"delta":{"content":"Hello"},"logprobs":null,"finish_reason":null}]}

// ....

// {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-3.5-turbo-0125", "system_fingerprint": "fp_44709d6fcb", "choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop"}]}

function messageReducer(state, action) {
  switch (action.type) {
    case "SET_ROLE":
      return { ...state, role: action.role }
    case "APPEND_CONTENT":
      return { ...state, content: state.content + action.content }
    case "RESET":
      return { role: "", content: "" }
    default:
      return state
  }
}

export function useChatCompletion({
  model: { provider, model },
  messages,
  temperature,
  onError,
}) {
  const [isStreaming, setIsStreaming] = useState(false)
  const [isDone, setIsDone] = useState(false)
  const [message, dispatch] = useReducer(messageReducer, {
    role: "",
    content: "",
  })

  const startCompletion = useCallback(async () => {
    setIsStreaming(true)

    console.log("startCompletion", messages)

    const apiPath =
      provider === "anthropic"
        ? "/anthropic/v1/messages"
        : "/openai/v1/chat/completions"
    const requestBody = {
      model,
      temperature: 1,
      stream: true,
      max_tokens: 1024,
    }

    if (provider === "anthropic") {
      const systemMessage = messages.find((msg) => msg.role === "system")
      if (systemMessage) {
        requestBody.system = systemMessage.content
        requestBody.messages = messages.filter((msg) => msg.role !== "system")
      } else {
        requestBody.messages = messages
      }
    } else {
      requestBody.messages = messages
    }

    let retries = 3
    let response

    while (retries > 0) {
      try {
        console.info({ requestBody })
        response = await fetch(apiPath, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
          },
          body: JSON.stringify(requestBody),
        })

        if (response.ok) {
          break
        } else {
          throw new Error(`HTTP error! status: ${response.status}`)
        }
      } catch (error) {
        retries--
        if (retries === 0) {
          onError(error)
          setIsStreaming(false)
          return
        }
        await new Promise((resolve) => setTimeout(resolve, 3000)) // Delay for 3 seconds before retrying
      }
    }

    const reader = response.body.getReader()
    const decoder = new TextDecoder("utf-8")
    let buffer = ""

    while (true) {
      const { done, value } = await reader.read()

      if (done) {
        break
      }

      buffer += decoder.decode(value)

      let position
      while ((position = buffer.indexOf("\n")) !== -1) {
        const line = buffer.slice(0, position).trim()
        buffer = buffer.slice(position + 1)

        if (line === "") continue

        const message = line.replace(/^data: /, "")
        if (message === "[DONE]") {
          setIsStreaming(false)
          setIsDone(true)
          break
        }

        try {
          if (provider === "anthropic") {
            const parsed = JSON.parse(message)
            if (parsed.type === "message_start") {
              dispatch({ type: "SET_ROLE", role: parsed.message.role })
            } else if (parsed.type === "content_block_delta") {
              const text = parsed.delta.text
              dispatch({ type: "APPEND_CONTENT", content: text })
            } else if (parsed.type === "message_stop") {
              setIsStreaming(false)
              setIsDone(true)
              break
            }
          } else {
            const parsed = JSON.parse(message)
            const delta = parsed.choices[0].delta
            if (delta.role === "assistant") {
              dispatch({ type: "SET_ROLE", role: delta.role })
            }
            if (delta.content) {
              dispatch({ type: "APPEND_CONTENT", content: delta.content })
            }
            if (parsed.choices[0].finish_reason === "stop") {
              setIsStreaming(false)
              setIsDone(true)
              break
            }
          }
        } catch (error) {
          console.error("Could not JSON parse stream message", message, error)
        }
      }
    }

    setIsStreaming(false)
    setIsDone(true)
  }, [model, JSON.stringify(messages), temperature, onError])

  useEffect(() => {
    startCompletion()
    return () => {
      dispatch({ type: "RESET" })
    }
  }, [model, JSON.stringify(messages), temperature])

  return {
    isStreaming,
    isDone,
    message,
  }
}

// okay that was OpenAI, now we also want to implement the same thing for Anthropic
//
// here is an example of Anthropic's SSE syntax
// event: message_start
// data: {"type": "message_start", "message": {"id": "msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY", "type": "message", "role": "assistant", "content": [], "model": "claude-3-opus-20240229", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 25, "output_tokens": 1}}}

// event: content_block_start
// data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

// event: ping
// data: {"type": "ping"}

// event: content_block_delta
// data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}

// event: content_block_delta
// data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "!"}}

// event: content_block_stop
// data: {"type": "content_block_stop", "index": 0}

// event: message_delta
// data: {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence":null}, "usage": {"output_tokens": 15}}

// event: message_stop
// data: {"type": "message_stop"}
