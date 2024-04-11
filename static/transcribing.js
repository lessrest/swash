import { useEffect, useState, useCallback } from "preact/hooks"

const apiUrl = ({ language }) =>
  `${location.protocol === "https:" ? "wss" : "ws"}://${
    location.host
  }/transcribe?language=${language}`

export const useLiveTranscription = ({ language, onUpdate, onError }) => {
  const [socket, setSocket] = useState(null)
  const [readyState, setReadyState] = useState(null)

  useEffect(() => {
    console.info("Creating new WebSocket")

    const socket = new WebSocket(apiUrl({ language }))
    setSocket(socket)
    setReadyState(socket.readyState)

    const onReadyStateChange = () => {
      console.info("WebSocket readyState changed", socket.readyState)
      setReadyState(socket.readyState)
    }

    socket.onopen = onReadyStateChange
    socket.onclose = onReadyStateChange

    return () => {
      socket.close()
    }
  }, [language])

  useEffect(() => {
    if (!socket) return

    const onMessage = (event) => {
      const message = JSON.parse(event.data)
      if (isBoringDeepgramMessage(message)) {
        return
      } else {
        onUpdate(message)
      }
    }

    socket.addEventListener("message", onMessage)

    return () => {
      socket.removeEventListener("message", onMessage)
    }
  }, [socket, onUpdate])

  useEffect(() => {
    if (!socket) return

    socket.addEventListener("error", onError)

    return () => {
      socket.removeEventListener("error", onError)
    }
  }, [socket, onError])

  const send = useCallback((message) => socket.send(message), [socket])

  return {
    readyState,
    send,
  }
}

function isBoringDeepgramMessage(message) {
  if (message.channel && message.channel.alternatives) {
    return (
      message.channel.alternatives[0].confidence === 0 &&
      message.channel.alternatives[0].transcript === ""
    )
  }
  return false
}
