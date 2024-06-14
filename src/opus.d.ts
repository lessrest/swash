declare global {
  interface AudioEncoderConfig {
    opus?: OpusEncoderConfig
  }

  interface OpusEncoderConfig {
    format?: "opus" | "ogg"
    signal?: "auto" | "music" | "voice"
    application?: "voip" | "audio" | "lowdelay"
    frameDuration?: number
    /**
     * Encoder complexity (0-10). 10 is highest.
     * Default: 5 on mobile, 9 on other platforms.
     */
    complexity?: number
    /** Configures the encoder's expected packet loss percentage (0 to 100). */
    packetlossperc?: number
    /** Enables Opus in-band Forward Error Correction (FEC) */
    useinbandfec?: boolean
    /** Enables Discontinuous Transmission (DTX) */
    usedtx?: boolean
  }
}
