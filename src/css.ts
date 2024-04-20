export const css = `
* {
  box-sizing: border-box; margin: 0;
}

body {
  background-color: #099;
  margin: 0;
}

app {
  display: grid;
  /* center */
  place-items: center;
}

body,
app {
  position: absolute;
  left: 0;
  top: 0;
  right: 0;
  bottom: 0;
  overflow: hidden;
}

.window {
  width: 50ch;
  opacity: 0.8;
}

.window-body {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  align-items: center;
  height: 100%;
  min-height: 50ch;
  width: 100%;
  margin: 0;
}

.failed {
  background-color: red;
}

.window2 { display: none !important; }

video {
  width: 100%;
  height: 100%;
  object-fit: cover;
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: -1;
  filter: blur(6px) sepia(0.6) hue-rotate(3deg) saturate(1.2) contrast(0.8) brightness(0.6);
}

article {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: -1;
  font: 60px/1.3 "Big Caslon";
  font-weight: 600;
  display: flex;
  justify-content: flex-end;
  flex-direction: column;
  gap: 2rem;
  color: #ffffcc;
  flex-wrap: wrap;
  padding: 2rem 4rem;
  overflow-y: hidden;
  overflow-x: hidden;
  text-shadow: 0 0 0.9rem #0008;
}

p { margin: 0; }

article message::before {
  display: none;
  content: '1 ';
}

article message.started::before {
  content: '2 ';
}

article message.finished::before {
  content: '3 ';
}

article message {
  transition: opacity 1s ease-in-out;
  opacity: 0;
}

article message.started {
  opacity: 1;
}

hr {
  border: none;
  margin: .25em 0;
}
`
