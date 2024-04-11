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
    const baseSpeed = 50 // Base typing speed in characters per second
    const fluctuationAmplitude = 0.2 // Amplitude of speed fluctuation
    const fluctuationFrequency = 0.05 // Frequency of speed fluctuation

    const charactersRemaining = text.length - lengthLimit
    const speedMultiplier = Math.max(0.1, Math.min(2, charactersRemaining / 100))

    const fluctuation = Math.sin(time * fluctuationFrequency) * fluctuationAmplitude + 1
    const typingSpeed = baseSpeed * speedMultiplier * fluctuation

    const updateLengthLimit = () => {
      setLengthLimit((prevLimit) => Math.min(prevLimit + 1, text.length))
    }

    const timeoutId = setTimeout(updateLengthLimit, 1000 / typingSpeed)

    return () => clearTimeout(timeoutId)
  }, [text, lengthLimit, time])

  const displayedText = text.slice(0, lengthLimit)

  return displayedText
}
