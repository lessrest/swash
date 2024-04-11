import { useState, useEffect } from "preact/hooks"

export function useTypingEffect(text) {
  const [lengthLimit, setLengthLimit] = useState(0)
  const [rateOfChange, setRateOfChange] = useState(0)

  useEffect(() => {
    const totalLength = text.length

    if (totalLength === lengthLimit) {
      setRateOfChange(0)
      return
    }

    const delta = totalLength - lengthLimit
    const minRate = 10
    const maxRate = 100

    if (delta < 0) {
      setLengthLimit(totalLength)
      setRateOfChange(0)
    } else if (delta > 0) {
      const newRate = Math.min(maxRate, minRate + delta)
      setRateOfChange(newRate)
    }
  }, [text, lengthLimit])

  useEffect(() => {
    if (rateOfChange === 0) return

    const updateLengthLimit = () => {
      setLengthLimit((prevLimit) => prevLimit + 1)
    }

    const intervalId = setInterval(updateLengthLimit, 1000 / rateOfChange)

    return () => clearInterval(intervalId)
  }, [rateOfChange])

  const displayedText = text.slice(0, lengthLimit)

  return displayedText
}
