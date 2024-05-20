console.log("Requesting user media")
let stream = await navigator.mediaDevices.getUserMedia({ audio: true })

console.log("Creating MediaRecorder instance")
let recorder = new MediaRecorder(stream, {
  mimeType: "audio/webm;codecs=opus",
  audioBitsPerSecond: 128000,
})

console.log("Opening WebSocket connection")
let socket = new WebSocket("ws://localhost:8080/transcribe?lang=sv")

console.log("Waiting for WebSocket connection to open")
await new Promise((ok, no) => {
  socket.onopen = ok
  socket.onerror = no
})

console.log("Starting MediaRecorder")
recorder.ondataavailable = (e) => {
  socket.send(e.data)
}
socket.onmessage = (e) => {
  let data = JSON.parse(e.data)
  console.log("socket message", data)
}
recorder.start(100)
