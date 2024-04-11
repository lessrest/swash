import { ArrayBufferTarget, Muxer } from "webm-muxer"

/**
 * Concatenate audio buffers
 * @param {Array<Blob>} blobs - Array of audio blobs
 * @returns {Promise<AudioBuffer>} - Concatenated audio buffer
 */
export async function concatenateAudioBuffers(blobs) {
  const audioContext = new AudioContext()

  const buffers = await Promise.all(
    blobs.map(async (blob) => {
      return audioContext.decodeAudioData(await blob.arrayBuffer())
    }),
  )

  const totalLength = buffers.reduce((sum, buffer) => sum + buffer.length, 0)
  const totalChannels = Math.max(
    ...buffers.map((buffer) => buffer.numberOfChannels),
  )

  const result = audioContext.createBuffer(
    totalChannels,
    totalLength,
    audioContext.sampleRate,
  )

  let offset = 0
  for (const buffer of buffers) {
    for (let channel = 0; channel < buffer.numberOfChannels; channel++) {
      result
        .getChannelData(channel)
        .set(buffer.getChannelData(channel), offset)
    }
    offset += buffer.length
  }

  return result
}

/**
 * Encode audio buffer to WebM/Opus with AudioEncoder
 * @param {AudioBuffer} audioBuffer - Audio buffer to encode
 * @returns {Promise<Blob>} - Encoded audio blob
 */
export async function encodeAudioBuffer(audioBuffer) {
  const audioConf = {
    codec: "A_OPUS",
    sampleRate: audioBuffer.sampleRate,
    numberOfChannels: audioBuffer.numberOfChannels,
  }

  const muxer = new Muxer({
    target: new ArrayBufferTarget(),
    audio: audioConf,
  })

  let error = null

  const audioEncoder = new AudioEncoder({
    output: (chunk, metadata) => {
      try {
        muxer.addAudioChunk(chunk, metadata)
      } catch (e) {
        console.error(e)
        error = e
        audioEncoder.close()
      }
    },
    error: (error) => {
      console.error(error)
      error = error
    },
  })

  audioEncoder.configure({
    codec: "opus",
    sampleRate: audioBuffer.sampleRate,
    numberOfChannels: audioBuffer.numberOfChannels,
    bitrate: 32000,
    opus: {
      signal: "voice",
      application: "voip",
    },
  })

  const audioData = new AudioData({
    format: "f32-planar",
    sampleRate: audioBuffer.sampleRate,
    numberOfChannels: audioBuffer.numberOfChannels,
    numberOfFrames: audioBuffer.length,
    timestamp: 0,
    data: planarizeAudioBuffer(audioBuffer),
  })

  audioEncoder.encode(audioData)
  await audioEncoder.flush()

  if (error) {
    throw error
  }

  audioEncoder.close()
  muxer.finalize()

  return new Blob([muxer.target.buffer], { type: "audio/webm" })
}

/**
 * Planarize audio buffer
 * @param {AudioBuffer} audioBuffer - Audio buffer to planarize
 * @returns {Float32Array} - Planarized audio buffer
 */
function planarizeAudioBuffer(audioBuffer) {
  const planarized = new Float32Array(
    audioBuffer.numberOfChannels * audioBuffer.length,
  )

  for (let channel = 0; channel < audioBuffer.numberOfChannels; channel++) {
    planarized.set(
      audioBuffer.getChannelData(channel),
      channel * audioBuffer.length,
    )
  }

  return planarized
}

/**
 * Create a concatenated audio blob from an array of audio blobs
 * @param {Array<Blob>} blobs - Array of audio blobs
 * @returns {Promise<Blob>} - Concatenated audio blob
 */
export async function concatenateAudioBlobs(blobs) {
  const audioBuffer = await concatenateAudioBuffers(blobs)
  return encodeAudioBuffer(audioBuffer)
}
