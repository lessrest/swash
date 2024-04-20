import GraphemeSplitter from "grapheme-splitter"
import { useEffect, useState } from "preact/hooks"

const splitter = new GraphemeSplitter()
const typingSpeed = 50 // Typing speed in graphemes per second

const graphemeDelayTable = {
  ",": 200,
  ".": 300,
  "?": 400,
  "!": 400,
  "—": 400,
}

const graphemeDelay = (grapheme) => {
  return graphemeDelayTable[grapheme] || 1000 / typingSpeed
}

export function useTypingEffect(text) {
  const [displayedLength, setDisplayedLength] = useState(0)
  const graphemes = splitter.splitGraphemes(text)
  const maxLength = graphemes.length
  const delta = maxLength - displayedLength
  const lastGrapheme =
    displayedLength > 0 ? graphemes[displayedLength - 1] : ""
  let delay = graphemeDelay(lastGrapheme)

  if (delta > 20) {
    delay = Math.max(delay * 0.8, 20)
  }

  useEffect(() => {
    let intervalId = null

    if (delta > 0) {
      intervalId = setInterval(() => {
        setDisplayedLength((prevLength) => prevLength + 1)
      }, delay)
    } else if (delta < -1) {
      setDisplayedLength(maxLength)
    }

    return () => {
      clearInterval(intervalId)
    }
  }, [delta, maxLength, setDisplayedLength, delay])

  useEffect(() => {
    window.scrollTo(0, document.body.scrollHeight)
  }, [displayedLength])

  const displayedText = graphemes.slice(0, displayedLength).join("")
  return displayedText
}
