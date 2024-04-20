export const css = `
* { box-sizing: border-box; }
body { background-color: #099; filter: sepia(0.4); }
app { display: flex; gap: 1rem; flex-wrap: wrap; flex-direction: row; }
app { align-items: center; justify-content: space-evenly; }
body, app { position: absolute; left: 0; top: 0; right: 0; bottom: 0; }
.window { min-width: 50ch; max-width: 60ch; }
.window-body { display: flex; flex-wrap: wrap; }
.window-body { justify-content: center; align-items: center; }

.failed {
  background-color: red;
}

video {
object-fit: cover;
filter: sepia(0.4) brightness(0.6);
width: 100%;
height: 100%;
border: 2px inset #0003;
}

article {
font: 80px/1.5 "Big Caslon";
position: absolute; 
top: 0;
left: 0;
right: 0;
bottom: 0;
display: flex;
align-items: center;
gap: 2rem;
  color: #ffffcc;
  flex-wrap: wrap;
  justify-content: space-evenly;
  align-items: center;
  gap: 2rem;
  padding: 8rem 2rem;
  overflow-y: auto;
  /* text shadows both light and dark to help legibility */
  text-shadow: 0 0 1rem skyblue, 0 0 0.9rem #0003;
}

article message {
opacity: 1;
transition: opacity 5s ease-in-out;
}

article message.visible {
opacity: 0;
}

`;
