const { app, BrowserWindow } = require("electron")
const path = require("path")
const url = require("url")

function createWindow() {
  win = new BrowserWindow({
    width: 800,
    height: 600,
    titleBarStyle: "hidden",
  })
  win.loadURL("https://swa.sh/#telegram")
}

app.on("ready", createWindow)
app.on("login", (e, b, c, d, callback) => {
  e.preventDefault()
  callback("Bob", "hiccup")
})
