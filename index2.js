import { render } from "preact"
import { html } from "htm/preact"
import { signal } from "@preact/signals"

import { createEventStore, saveEvent, getAllEvents } from "./eventStore.js"
import { state, reducer, setState } from "./state.js"
import { prompts } from "./prompts.js"

const viewState = signal({
  promptName: "captainslog",
  promptEditorOpen: false,
  model: "claude3-opus",
  language: "en-US",
})

function App() {
  return html`
    <${TranscriptView} 
      transcript=${state.transcript}
      viewState=${viewState.value} />
  `
}

function TranscriptView({ transcript, viewState }) {
  // Render Toolbar, Paragraphs, CurrentSegment components
  // Pass relevant props to each component
}

function Toolbar({ viewState, onRecordClick, onLanguageChange, onModelChange, onPromptChange, onEditPrompt }) {
  // Render record button, language select, model select, prompt select, edit prompt button
  // Call respective event handlers when interactions occur
}

function Paragraphs({ segments, viewState }) {
  // Map over segments and render Paragraph components
  // Pass segment, index, and viewState as props
}

function Paragraph({ segment, index, viewState }) {
  // Render segment content, audio player, split button
  // Include ChatCompletion component and pass messages prop
}

function CurrentSegment({ segment, viewState }) {
  // Render current segment with interim results
  // Use AnimatedWords component for interim results
}

function PromptEditor({ promptName, initialPrompt, onSave, onCancel }) {
  // Render prompt editor with text area and save/cancel buttons
  // Call onSave or onCancel when respective buttons are clicked
}

function ChatCompletion({ messages, model }) {
  // Use useChatCompletion hook to fetch and display chat completion
  // Render chat completion with typing effect
}

// Other utility components like AnimatedWords, Player, RecordingStatus, etc.

// Event handlers and other functions

// Initialization code

render(html`<${App} />`, document.getElementById("app"))
