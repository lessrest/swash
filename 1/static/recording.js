import { useCallback, useEffect, useMemo, useState } from "preact/hooks"

export const mimeType = MediaRecorder.isTypeSupported(
  "audio/webm;codecs=opus",
)
  ? "audio/webm;codecs=opus"
  : "audio/mp4"

const audioBitsPerSecond = mimeType === "audio/mp4" ? 128000 : 32000

/**
 * Custom hook for managing speech audio recording.
 * @param {Object} options - The options for the speech audio hook.
 * @param {MediaStream} options.mediaStream - The media stream to record from.
 * @param {function(Blob): void} options.onFrame - Callback function to handle the recorded frame data blob.
 * @param {function(Blob): void} options.onChunk - Callback function to handle the recorded chunk data blob.
 * @param {function(Error): void} options.onFail - Callback function to handle recording errors.
 * @returns {Object} An object containing the recording state and control functions.
 * @returns {boolean} return.isRecording - Indicates if the recording is currently active.
 * @returns {function(): void} return.start - Function to start the recording.
 * @returns {function(): void} return.stop - Function to stop the recording.
 * @returns {function(): void} return.endCurrentChunk - Function to end the current chunk and start a new one.
 */
export function useSpeechAudio({ mediaStream, onFrame, onChunk, onFail }) {
  const frameRecorder = useAudioRecorder({
    mediaStream,
    onBlob: onFrame,
    onFail,
  })

  const chunkRecorder = useAudioRecorder({
    mediaStream,
    onBlob: onChunk,
    onFail,
  })

  const frameRecorderState = useRecorderState(frameRecorder)

  const start = useCallback(() => {
    console.info("Starting frame and chunk MediaRecorders")
    frameRecorder.start(100)
    chunkRecorder.start()
  }, [frameRecorder, chunkRecorder])

  const stop = useCallback(() => {
    console.info("Stopping frame and chunk MediaRecorders")
    frameRecorder.stop()
    chunkRecorder.stop()
  }, [frameRecorder, chunkRecorder])

  const endCurrentChunk = useCallback(() => {
    console.info("Ending current chunk, starting new chunk")
    chunkRecorder.stop()
    chunkRecorder.start()
  }, [chunkRecorder])

  useEffect(() => {
    window.addEventListener("unload", stop)

    return () => {
      window.removeEventListener("unload", stop)
    }
  }, [stop])

  return {
    isRecording: frameRecorderState === "recording",
    start,
    stop,
    endCurrentChunk,
  }
}

/**
 * Custom hook for creating an audio recorder.
 * @param {Object} options - The options for the audio recorder.
 * @param {MediaStream} options.mediaStream - The media stream to record from.
 * @param {function(Blob): void} options.onBlob - Callback function to handle the recorded data blob.
 * @param {function(Error): void} options.onFail - Callback function to handle recording errors.
 * @returns {MediaRecorder} The audio recorder instance.
 */
function useAudioRecorder({ mediaStream, onBlob, onFail }) {
  const recorder = useMemo(() => {
    console.info("Creating new MediaRecorder")
    return new MediaRecorder(mediaStream, {
      audioBitsPerSecond,
      mimeType,
    })
  }, [mediaStream])

  const ondataavailable = useCallback(
    ({ data }) => {
      if (data.size > 0) {
        onBlob(data)
      }
    },
    [onBlob],
  )

  useEffect(() => {
    console.info("Configuring MediaRecorder event handlers")
    recorder.ondataavailable = ondataavailable
    recorder.onerror = onFail
  }, [ondataavailable, onFail])

  useEffect(() => {
    // stop the recorder when the component unmounts
    return () => {
      console.info("Cleaning up MediaRecorder")
      recorder.stop()
    }
  }, [recorder])

  return recorder
}

/**
 * Returns the state of the recorder.
 * @param {MediaRecorder} recorder
 * @returns {RecordingState}
 */
function useRecorderState(recorder) {
  const [state, setState] = useState(recorder.state)

  useEffect(() => {
    console.info("Configuring MediaRecorder state change listeners")
    const onStateChange = () => {
      setState(recorder.state)
    }

    recorder.addEventListener("start", onStateChange)
    recorder.addEventListener("stop", onStateChange)
    recorder.addEventListener("pause", onStateChange)
    recorder.addEventListener("resume", onStateChange)

    return () => {
      console.info("Cleaning up MediaRecorder state change listeners")
      recorder.removeEventListener("start", onStateChange)
      recorder.removeEventListener("stop", onStateChange)
      recorder.removeEventListener("pause", onStateChange)
      recorder.removeEventListener("resume", onStateChange)
    }
  }, [recorder])

  return state
}
