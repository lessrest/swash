export let state = {
  archive: [],
  transcript: [],
  current: { words: [], timestamp: null },
  interim: [],
}

export const handlers = {
  DeepgramMessage(state, { message }, timestamp) {
    if (message.type !== "Results") return state

    const { words, transcript } = message.channel.alternatives[0]

    if (transcript.trim() === "") return state

    if (message.is_final) {
      // Move interim words to the current segment.
      return {
        ...state,
        current: {
          words: [...state.current.words, ...words],
          timestamp,
        },
        interim: [],
      }
    } else {
      // Update the interim words.
      return {
        ...state,
        interim: words,
      }
    }
  },
  AudioBlob(state, { blob, data }, timestamp) {
    // Move the current segment to the transcript with audio attached.
    return {
      ...state,
      transcript: [
        ...state.transcript,
        {
          words: state.current.words,
          audio: URL.createObjectURL(blob || data),
          blob,
          t0: state.current.timestamp,
          timestamp,
        },
      ],
      current: { words: [], timestamp: null },
    }
  },
  StartedRecording(state) {
    return {
      ...state,
    }
  },
  WhisperResult(state, { result, timestamp }) {
    // The timestamp here identifies the segment that was transcribed.
    // It's not the timestamp of the event itself.
    return {
      ...state,
      transcript: state.transcript.map((entry) =>
        entry.t0 === timestamp ? { ...entry, whisper: result } : entry,
      ),
    }
  },
  SplitTranscript(state, { timestamp }) {
    // Archive everything before the segment with the given timestamp.
    const archived = state.transcript.filter((entry) => entry.t0 < timestamp)
    const remaining = state.transcript.filter(
      (entry) => entry.t0 >= timestamp,
    )
    return {
      ...state,
      archive: [...state.archive, ...archived],
      transcript: remaining,
    }
  },
}

export let state = {
  archive: [],
  transcript: [],
  current: { words: [], timestamp: null },
  interim: [],
}

export const handlers = {
  DeepgramMessage(state, { message }, timestamp) {
    if (message.type !== "Results") return state

    const { words, transcript } = message.channel.alternatives[0]

    if (transcript.trim() === "") return state

    if (message.is_final) {
      // Move interim words to the current segment.
      return {
        ...state,
        current: {
          words: [...state.current.words, ...words],
          timestamp,
        },
        interim: [],
      }
    } else {
      // Update the interim words.
      return {
        ...state,
        interim: words,
      }
    }
  },
  AudioBlob(state, { blob, data }, timestamp) {
    // Move the current segment to the transcript with audio attached.
    return {
      ...state,
      transcript: [
        ...state.transcript,
        {
          words: state.current.words,
          audio: URL.createObjectURL(blob || data),
          blob,
          t0: state.current.timestamp,
          timestamp,
        },
      ],
      current: { words: [], timestamp: null },
    }
  },
  StartedRecording(state) {
    return {
      ...state,
    }
  },
  WhisperResult(state, { result, timestamp }) {
    // The timestamp here identifies the segment that was transcribed.
    // It's not the timestamp of the event itself.
    return {
      ...state,
      transcript: state.transcript.map((entry) =>
        entry.t0 === timestamp ? { ...entry, whisper: result } : entry,
      ),
    }
  },
  SplitTranscript(state, { timestamp }) {
    // Archive everything before the segment with the given timestamp.
    const archived = state.transcript.filter((entry) => entry.t0 < timestamp)
    const remaining = state.transcript.filter(
      (entry) => entry.t0 >= timestamp,
    )
    return {
      ...state,
      archive: [...state.archive, ...archived],
      transcript: remaining,
    }
  },
}

export function reducer(state, { payload, timestamp }) {
  const handler = handlers[payload.type]
  return handler ? handler(state, payload, timestamp) : state
}