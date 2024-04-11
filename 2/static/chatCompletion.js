import { useState, useEffect, useCallback, useReducer } from "preact/hooks"

function messageReducer(state, action) {
  switch (action.type) {
    case 'APPEND_CONTENT':
      return state + action.content
    case 'RESET':
      return ''
    default:
      return state
  }
}

export function useChatCompletion({
  apiKey,
  model,
  messages,
  temperature,
  onError,
  onDone,
}) {
  const [isStreaming, setIsStreaming] = useState(false)
  const [message, dispatch] = useReducer(messageReducer, '')

  const startCompletion = useCallback(async () => {
    setIsStreaming(true)

    const response = await fetch("/openai/v1/chat/completions", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Authorization": `Bearer ${apiKey}`,
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
        onDone()
        setIsStreaming(false)
        break
      }

      const chunk = decoder.decode(value)
      const lines = chunk.split("\n").filter((line) => line.trim() !== "")

      for (const line of lines) {
        const message = line.replace(/^data: /, "")
        if (message === "[DONE]") {
          onDone()
          setIsStreaming(false)
          break
        }
        try {
          const parsed = JSON.parse(message)
          const delta = parsed.choices[0].delta
          if (delta.content) {
            dispatch({ type: 'APPEND_CONTENT', content: delta.content })
          }
        } catch (error) {
          console.error("Could not JSON parse stream message", message, error)
        }
      }
    }
  }, [apiKey, model, messages, temperature, onMessage, onError, onDone])

  useEffect(() => {
    return () => {
      dispatch({ type: 'RESET' })
    }
  }, [])

  return {
    isStreaming,
    message,
    startCompletion,
  }
}
