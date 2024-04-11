import { useState, useEffect } from "preact/hooks"

export function useTypingEffect(text) {
  const [lengthLimit, setLengthLimit] = useState(0)
  const [time, setTime] = useState(0)

  useEffect(() => {
    const totalLength = text.length

    if (totalLength === lengthLimit) {
      return
    }

    const delta = totalLength - lengthLimit
    const minRate = 10
    const maxRate = 100

    if (delta < 0) {
      setLengthLimit(totalLength)
    } else if (delta > 0) {
      const intervalId = setInterval(() => {
        setTime((prevTime) => prevTime + 1)
      }, 100)

      return () => clearInterval(intervalId)
    }
  }, [text, lengthLimit])

  useEffect(() => {
    const amplitude = 25
    const frequency = 0.1
    const offset = 75

    const rateOfChange = Math.sin(time * frequency) * amplitude + offset

    const updateLengthLimit = () => {
      setLengthLimit((prevLimit) => Math.min(prevLimit + 1, text.length))
    }

    const timeoutId = setTimeout(updateLengthLimit, 1000 / rateOfChange)

    return () => clearTimeout(timeoutId)
  }, [text, time])

  const displayedText = text.slice(0, lengthLimit)

  return displayedText
}
