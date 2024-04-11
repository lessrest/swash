import { useState, useEffect, useCallback, useReducer } from "preact/hooks"

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

export function useChatCompletion({ model, messages, temperature, onError }) {
  const [isStreaming, setIsStreaming] = useState(false)
  const [isDone, setIsDone] = useState(false)
  const [message, dispatch] = useReducer(messageReducer, {
    role: "",
    content: "",
  })

  const startCompletion = useCallback(async () => {
    setIsStreaming(true)

    const response = await fetch("/openai/v1/chat/completions", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        model,
        messages,
        temperature,
        stream: true,
      }),
    })

    if (!response.ok) {
      onError(new Error(`HTTP error! status: ${response.status}`))
      setIsStreaming(false)
      return
    }

    const reader = response.body.getReader()
    const decoder = new TextDecoder("utf-8")

    while (true) {
      const { done, value } = await reader.read()
      if (done) {
        setIsStreaming(false)
        setIsDone(true)
        break
      }

      const chunk = decoder.decode(value)
      const lines = chunk.split("\n").filter((line) => line.trim() !== "")

      for (const line of lines) {
        const message = line.replace(/^data: /, "")
        if (message === "[DONE]") {
          setIsStreaming(false)
          setIsDone(true)
          break
        }
        try {
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
        } catch (error) {
          console.error("Could not JSON parse stream message", message, error)
        }
      }
    }
  }, [model, messages, temperature, onError])

  useEffect(() => {
    if (!isStreaming && !isDone) {
      startCompletion()
    }
    return () => {
      dispatch({ type: "RESET" })
    }
  }, [isStreaming, isDone])

  return {
    isStreaming,
    isDone,
    message,
  }
}
