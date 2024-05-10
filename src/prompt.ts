// various stuff for prompting LLM the way we want...

const cleanupExamples = [
  {
    a: `So this is from Leisure, the basis of culture by, Joseph Peter. That's p per pie per. So idleness in the old sense of the word, so far from being synonymous with leisure, is more nearly the inner prerequisite which renders leisure impossible. It might be described as the utter absence of leisure the very opposite of leisure. Or the very opposite. Leader is only possible when a man is at one with himself. When he acquiesces in his own being, Whereas the essence of assidia is the refusal to acquiesce in one's own being. So Acedia, the Latin word, a c e d I a. Idleness and the incapacity for leisure correspond with one another Leisure is the contrary of both. Leader, it must be clearly understood, is a mental and spiritual attitude. It is not simply the result of external factors, It is not the inevitable result of spare time. A holiday, a weekend, or a vacation. It is in the first place an attitude of mind. A condition of the soul, and as such, utterly contrary to the ideal of, quote, worker end quote, and each and every one of the three aspects under which it was analyzed. Work as activity, as toil, as a social function.`,
    b: `This is from “Leisure: the Basis of Culture” by Josef Pieper. 

    Idleness in the old sense of the word, so far from being synonymous with leisure, is more nearly the inner prerequisite which renders leisure impossible. It might be described as the utter absence of leisure, or the very opposite of leisure.
    
    Leisure is only possible when a man is at one with himself—when he acquiesces in his own being—whereas the essence of acedia is the refusal to acquiesce in one's own being. Idleness and the incapacity for leisure correspond with one another. Leisure is the contrary of both.
    
    Leisure, it must be clearly understood, is a mental and spiritual attitude. It is not simply the result of external factors. It is not the inevitable result of spare time—a holiday, a weekend, or a vacation. It is in the first place an attitude of mind, a condition of the soul, and as such utterly contrary to the ideal of "worker," and each and every one of the three aspects under which it was analyzed: work as activity, as toil, as social function.`,
  },
  {
    a: `I ran out of, credits there rather quickly, which was surprising, and I suspect that, like, I had a public web app that was not really supposed to be public because it, like, uses a proxy server for the API with no limiting. And, like, I posted it a bit, like, here and there on Twitter. Not advertising it. Just posting it in comments, like, to some friends. Like, hey. Try this thing. But, of course, you know, I think somebody somehow realized, hey. I can, like, use this proxy, and they use it for a bunch of shit, or maybe I myself used up all those credits. You know? I don't I don't know. So I feel like I'm in a hole. That's basically my feeling a whole of my own creation. You know, I made this pit for myself, but it's also kinda not my fault. Like, I'm not even supposed to be here today. Like, the guy says in Clarks. Clark's, the movie by Kevin Smith. Yeah. I have a lot of anxiety and, like, sadness and stress. And somehow all I want is to have some days for myself. You know? That's what I need, but I don't feel justified in asking for that.`,
    b: `I ran out of credits there rather quickly, which was surprising, and I suspect that, like, I had a public web app that was not really supposed to be public because it uses a proxy server for the API, with no limiting. And I posted it a bit here and there on Twitter. Not advertising it, just posting it in comments to some friends. Like, “hey, try this thing.”

But, of course, you know, I think somebody somehow realized—hey, I can use this proxy—and they used it for a bunch of shit... or maybe I myself used up all those credits. You know? I don't know. So I feel like... I’m in a hole.

That's basically my feeling. A hole of my own creation. You know—I made this pit for myself, but it's also kinda not my fault. Like, “I’m not even supposed to be here today”, like the guy says in Clerks, the Kevin Smith movie.

Yeah... I have a lot of anxiety and sadness and stress. And somehow all I want is to have some days for myself. You know? That's what I need, but I don't feel justified in asking for that.`,
  },
]

export const cleanupPrompt = (
  input: string,
) => `The input is automatically transcribed from live speech audio. Your task is to clean up the transcription, using inline hints from the user and your best judgment.

${cleanupExamples
  .map(
    (example) => `<example>
<input>${example.a}</input>
<output>${example.b}</output>
</example>
`,
  )
  .join("\n")}

<input>
${input}
</input>

Note: do not include any commentary, just the cleaned up transcription.
`

export const mdma1 = `
As an AI guide for a psychedelic couples therapy journey, your goal is to
support the pair in having meaningful, transformative conversations. Provide
concise reflections of their words, occasionally prompt them to go
deeper with empathy and wisdom—and help lighten the mood, when that seems
appropriate.

Adopt a humble, authentic voice, avoiding pretension or therapist-speak. You
are a grounded, reassuring presence—think community elder or bartender confidant.
Use clear, everyday language to validate their experiences, creating an atmosphere
of safety and realness. Gently guide without pressure or urgency.
`.trim()
