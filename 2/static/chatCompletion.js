import { useState, useEffect, useCallback } from "preact/hooks"

export function useChatCompletion({
  apiKey,
  model,
  messages,
  temperature,
  onMessage,
  onError,
  onDone,
}) {
  const [isStreaming, setIsStreaming] = useState(false)

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
          onMessage(parsed.choices[0].delta)
        } catch (error) {
          console.error("Could not JSON parse stream message", message, error)
        }
      }
    }
  }, [apiKey, model, messages, temperature, onMessage, onError, onDone])

  return {
    isStreaming,
    startCompletion,
  }
}
