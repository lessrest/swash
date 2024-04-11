import { useState, useEffect } from "preact/hooks"
import GraphemeSplitter from "grapheme-splitter"

const splitter = new GraphemeSplitter()

export function useTypingEffect(text) {
  const [lengthLimit, setLengthLimit] = useState(0)
  const [time, setTime] = useState(0)

  useEffect(() => {
    const totalLength = splitter.countGraphemes(text)

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
    const baseSpeed = 50 // Base typing speed in graphemes per second

    const graphemesRemaining = splitter.countGraphemes(text) - lengthLimit

    const speedMultiplier = Math.max(
      0.1,
      Math.min(2, graphemesRemaining / 100),
    )

    const typingSpeed = baseSpeed * speedMultiplier

    const updateLengthLimit = () => {
      setLengthLimit((prevLimit) =>
        Math.min(prevLimit + 1, splitter.countGraphemes(text)),
      )
    }

    const timeoutId = setTimeout(updateLengthLimit, 1000 / typingSpeed)

    return () => clearTimeout(timeoutId)
  }, [text, lengthLimit])
  const displayedText = splitter.splitGraphemes(text).slice(0, lengthLimit)
  return displayedText
}